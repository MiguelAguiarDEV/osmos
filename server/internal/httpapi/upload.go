package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type UploadServer struct {
	Dir      string
	MaxBytes int64
	Allowed  []string      // whitelist de mimes permitidos; vacío = desactivado
	TTL      time.Duration // borra blobs más viejos que esto; 0 = desactivado
}

type uploadResp struct {
	UploadURL string `json:"upload_url"`
	Size      int    `json:"size"`
}

var idRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *UploadServer) Upload(w http.ResponseWriter, r *http.Request) {
	if s.MaxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.MaxBytes)
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// validar Content-Type contra whitelist si está configurada
	if len(s.Allowed) > 0 {
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		if media, _, err := mime.ParseMediaType(ct); err == nil {
			ct = media
		}
		if !isAllowedMime(s.Allowed, ct) {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
	}

	id := randHex(16)
	final := filepath.Join(s.Dir, id)

	// escribir a tmp y luego rename
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		_ = os.Remove(tmpName) // no pasa nada si ya se renombró
	}()

	n, err := io.Copy(tmp, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}

	// fsync para asegurar persistencia antes del rename (opcional)
	if err := tmp.Sync(); err != nil {
		http.Error(w, "fsync error", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "close error", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpName, final); err != nil {
		http.Error(w, "rename error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uploadResp{
		UploadURL: "/d/" + id,
		Size:      int(n),
	})
}

func (s *UploadServer) Download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	fp := filepath.Join(s.Dir, id)
	f, err := os.Open(fp)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "inline; filename="+id)
	_, _ = io.Copy(w, f)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StartJanitor runs cleanup every interval until ctx is done. No-op if TTL<=0.
func (s *UploadServer) StartJanitor(ctx context.Context, interval time.Duration) {
	if s.TTL <= 0 || interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.cleanup(time.Now())
			}
		}
	}()
}

// cleanup removes stored blobs and stale temp files older than TTL.
func (s *UploadServer) cleanup(now time.Time) int {
	if s.TTL <= 0 {
		return 0
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !idRe.MatchString(name) && !strings.HasPrefix(name, ".upload-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > s.TTL {
			if os.Remove(filepath.Join(s.Dir, name)) == nil {
				removed++
			}
		}
	}
	return removed
}

func isAllowedMime(allowed []string, ct string) bool {
	if len(allowed) == 0 {
		return true
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	for _, p := range allowed {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "/*") {
			base := strings.TrimSuffix(p, "/*")
			if strings.HasPrefix(ct, base+"/") {
				return true
			}
			continue
		}
		if p == ct {
			return true
		}
	}
	return false
}
