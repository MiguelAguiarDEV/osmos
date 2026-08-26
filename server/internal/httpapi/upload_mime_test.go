package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestUpload_RejectsUnsupportedMime(t *testing.T) {
	dir := t.TempDir()
	s := &UploadServer{Dir: dir, MaxBytes: 1 << 20}
	// configurar whitelist: solo text/plain
	s.Allowed = []string{"text/plain"}

	body := bytes.Repeat([]byte("X"), 100)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	// enviar con mime no permitido
	req.Header.Set("Content-Type", "application/octet-stream")
	rr := httptest.NewRecorder()

	http.HandlerFunc(s.Upload).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d, want 415", rr.Code)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("no debe guardar archivos cuando el mime no es aceptado")
	}
}

func TestUpload_AcceptsExactAndWildcard(t *testing.T) {
	dir := t.TempDir()
	s := &UploadServer{Dir: dir, MaxBytes: 1 << 20}

	// Caso 1: exacto application/octet-stream
	s.Allowed = []string{"application/octet-stream"}
	body := bytes.Repeat([]byte("X"), 10)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	rr := httptest.NewRecorder()
	http.HandlerFunc(s.Upload).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	// limpiar
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.Remove(dir + "/" + e.Name())
	}

	// Caso 2: wildcard image/* acepta image/png
	s.Allowed = []string{"image/*"}
	body2 := bytes.Repeat([]byte("Y"), 10)
	req2 := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "image/png")
	rr2 := httptest.NewRecorder()
	http.HandlerFunc(s.Upload).ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status=%d", rr2.Code)
	}
}
