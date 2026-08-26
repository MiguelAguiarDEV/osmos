package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"clip-sync/server/pkg/types"
)

/* ---------- helpers ---------- */

const (
	exitOK     = 0
	exitUsage  = 2
	exitConn   = 10
	exitUpload = 11
	exitSend   = 12
	exitPolicy = 13
)

// terminalCloseError distingue los cierres que son culpa de la configuración
// (token inválido, device_id mal formado o duplicado) de los cortes de red.
// Reintentar en esos casos sólo produce un bucle infinito de reconexiones:
// dos procesos con el mismo device_id se echan mutuamente para siempre.
func terminalCloseError(err error) (string, bool) {
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
		return ce.Reason, true
	}
	return "", false
}

func fatalf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

func sleepBackoff(attempt int) { time.Sleep(computeBackoff(attempt)) }

// sleepBackoffCtx espera el backoff pero corta si se cancela el contexto.
// Devuelve false si hay que salir.
func sleepBackoffCtx(ctx context.Context, attempt int) bool {
	t := time.NewTimer(computeBackoff(attempt))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func computeBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond
	for i := 0; i < attempt && d < 5*time.Second; i++ {
		d *= 2
		if d > 5*time.Second {
			d = 5 * time.Second
		}
	}
	return d
}

// clientReadLimit debe cubrir un clip inline al máximo tamaño: el payload
// viaja en base64 dentro del JSON, así que ocupa ~4/3 del original. El límite
// por defecto de coder/websocket son 32 KiB, con lo que un clip grande cerraba
// la conexión del cliente en vez de entregarse.
const clientReadLimit = int64(types.MaxInlineBytes)*2 + 4096

func dialAndHello(ctx context.Context, addr, token, device string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(clientReadLimit)
	hello := types.Envelope{
		Type:  "hello",
		Hello: &types.Hello{Token: token, UserID: token, DeviceID: device},
	}
	if err := wsjson.Write(ctx, c, hello); err != nil {
		c.Close(websocket.StatusNormalClosure, "")
		return nil, err
	}
	return c, nil
}

// session es un modo de larga duración que corre sobre una conexión ya
// saludada. Devuelve error cuando la conexión muere; supervise reconecta.
type session func(ctx context.Context, c *websocket.Conn) error

// supervise mantiene la conexión viva indefinidamente con backoff exponencial.
//
// Antes sólo `listen` reconectaba: recv/watch/sync abrían la conexión una vez
// y al primer corte del servidor el proceso moría (o, en sync, el goroutine de
// recepción moría en silencio y watch se quedaba escupiendo errores de envío
// contra un socket muerto para siempre).
func supervise(ctx context.Context, addr, token, device, label string, verbose bool, run session) error {
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return nil
		}
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c, err := dialAndHello(dialCtx, addr, token, device)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "connect failed: %v (retry in %s)\n", err, computeBackoff(attempt))
			if !sleepBackoffCtx(ctx, attempt) {
				return nil
			}
			continue
		}
		fmt.Printf("connected to %s as %s (%s)\n", addr, device, label)
		attempt = 0

		runErr := run(ctx, c)
		_ = c.Close(websocket.StatusNormalClosure, "")
		if ctx.Err() != nil {
			return nil
		}
		if reason, terminal := terminalCloseError(runErr); terminal {
			fatalf(exitPolicy, "server rejected this session: %s", reason)
		}
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "%s error: %v (reconnecting)\n", label, runErr)
		}
		if !sleepBackoffCtx(ctx, attempt) {
			return nil
		}
	}
}

// syncSession corre recepción y vigilancia sobre la misma conexión; en cuanto
// una de las dos falla se cancela la otra para que supervise reconecte.
func syncSession(wsAddr string, interval time.Duration, verbose bool) session {
	return func(ctx context.Context, c *websocket.Conn) error {
		inner, cancel := context.WithCancel(ctx)
		defer cancel()

		var mu sync.Mutex
		lr := ""
		getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
		clearLR := func() { mu.Lock(); lr = ""; mu.Unlock() }
		mark := func(h string) { mu.Lock(); lr = h; mu.Unlock() }

		errCh := make(chan error, 2)
		go func() { errCh <- runRecvApply(inner, c, wsAddr, mark, verbose) }()
		go func() { errCh <- runWatchLoop(inner, c, wsAddr, interval, getLR, clearLR, verbose) }()

		err := <-errCh
		cancel()
		<-errCh
		return err
	}
}

// httpBaseFromWS convierte el endpoint WebSocket en la base HTTP del servidor.
//
// El path del endpoint (normalmente "/ws") tiene que desaparecer: si se
// conserva, /upload y /d/{id} se piden bajo "http://host/ws/upload" y el
// servidor responde 404. Eso dejaba inservible toda la ruta de payloads
// grandes (--file, y los clips >64 KiB en watch/recv).
func httpBaseFromWS(wsAddr string) string {
	scheme := "http"
	rest := wsAddr
	switch {
	case strings.HasPrefix(wsAddr, "wss://"):
		scheme, rest = "https", strings.TrimPrefix(wsAddr, "wss://")
	case strings.HasPrefix(wsAddr, "ws://"):
		scheme, rest = "http", strings.TrimPrefix(wsAddr, "ws://")
	case strings.HasPrefix(wsAddr, "https://"):
		scheme, rest = "https", strings.TrimPrefix(wsAddr, "https://")
	case strings.HasPrefix(wsAddr, "http://"):
		scheme, rest = "http", strings.TrimPrefix(wsAddr, "http://")
	}
	// quedarse sólo con el authority (host[:puerto]), descartando path,
	// query y fragmento
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return scheme + "://" + rest
}

func uploadFile(ctx context.Context, httpBase, path, contentType string) (uploadURL string, size int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(httpBase, "/")+"/upload", f)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("upload failed: status=%d body=%s", resp.StatusCode, string(b))
	}

	var out struct {
		UploadURL string `json:"upload_url"`
		Size      int    `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.UploadURL, out.Size, nil
}

/* ---------- modes ---------- */

func runListen(ctx context.Context, c *websocket.Conn) error {
	for {
		var env types.Envelope
		if err := wsjson.Read(ctx, c, &env); err != nil {
			return err
		}
		if env.Type != "clip" || env.Clip == nil {
			continue
		}
		cl := env.Clip
		if len(cl.Data) > 0 {
			if strings.HasPrefix(cl.Mime, "text/") {
				fmt.Printf("[from %s] %s\n", env.From, string(cl.Data))
			} else {
				fmt.Printf("[from %s] %s (%d bytes inline)\n", env.From, cl.Mime, len(cl.Data))
			}
		} else if cl.UploadURL != "" {
			fmt.Printf("[from %s] large clip: %s (%d bytes)\n", env.From, cl.UploadURL, cl.Size)
		}
	}
}

// runRecvApply listens and applies incoming text clips to the OS clipboard.
func runRecvApply(ctx context.Context, c *websocket.Conn, wsAddr string, markRemote func(hash string), verbose bool) error {
	base := httpBaseFromWS(wsAddr)
	dd := newDD(512)
	for {
		var env types.Envelope
		if err := wsjson.Read(ctx, c, &env); err != nil {
			return err
		}
		if env.Type != "clip" || env.Clip == nil {
			continue
		}
		cl := env.Clip
		if dd.ExistsOrAdd(cl.MsgID) {
			continue
		}
		if strings.HasPrefix(strings.ToLower(cl.Mime), "text/") {
			var data []byte
			if len(cl.Data) > 0 {
				data = cl.Data
			} else if cl.UploadURL != "" {
				u := strings.TrimRight(base, "/") + cl.UploadURL
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Fprintln(os.Stderr, "download failed:", err)
					continue
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					fmt.Fprintf(os.Stderr, "download failed: status=%d\n", resp.StatusCode)
					continue
				}
				data = b
			}
			if len(data) == 0 {
				continue
			}
			// canonicalizar antes de aplicar y de marcar el hash: si no, el
			// valor que luego lee el watcher no coincide con el marcado y el
			// clip se reenvía en bucle entre plataformas.
			text := normalizeClipboard(string(data))
			if verbose {
				fmt.Printf("[recv] applying to clipboard: from=%s bytes=%d backend=%s\n", env.From, len(text), clipboardWriteBackend())
			}
			if err := setClipboardText(text); err != nil {
				fmt.Fprintln(os.Stderr, "set clipboard failed:", err)
				continue
			}
			markRemote(hashString(text))
			if verbose {
				// read-back validation
				if rb, err := getClipboardText(); err == nil {
					ok := rb == text
					prev := rb
					if len(prev) > 80 {
						prev = prev[:80] + "…"
					}
					fmt.Printf("[recv] clipboard updated from %s, verify=%v len=%d preview=%q\n", env.From, ok, len(rb), prev)
				} else {
					fmt.Printf("[recv] clipboard updated from %s (unable to read back: %v)\n", env.From, err)
				}
			} else {
				fmt.Println("clipboard updated from", env.From)
			}
		} else {
			// non-text: skip for v1
			if verbose {
				fmt.Printf("[recv] skipping non-text from %s: mime=%s bytes=%d\n", env.From, cl.Mime, cl.Size)
			}
		}
	}
}

// runWatchLoop polls the clipboard and sends updates. Uses lastRemote to avoid echo.
func runWatchLoop(ctx context.Context, c *websocket.Conn, wsAddr string, interval time.Duration, lastRemote func() string, clearRemote func(), verbose bool) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastLocal string
	clipErrs := 0
	const maxClipErrs = 10
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			txt, err := getClipboardText()
			if err != nil {
				// un fallo puntual del backend (portapapeles vacío, compositor
				// reiniciando) no debe tirar la sesión entera; sólo se aborta
				// si el error es persistente.
				clipErrs++
				if clipErrs >= maxClipErrs {
					return fmt.Errorf("clipboard unreadable after %d attempts: %w", clipErrs, err)
				}
				if verbose {
					fmt.Fprintf(os.Stderr, "[watch] clipboard read failed (%d/%d): %v\n", clipErrs, maxClipErrs, err)
				}
				continue
			}
			clipErrs = 0
			h := hashString(txt)
			if lr := lastRemote(); lr != "" && lr == h {
				clearRemote()
				lastLocal = h
				continue
			}
			if h == lastLocal || txt == "" {
				continue
			}
			data := []byte(txt)
			if len(data) <= types.MaxInlineBytes {
				// stable msg_id for dedupe across devices
				msgID := "h-" + hashString(txt)
				if verbose {
					prev := txt
					if len(prev) > 80 {
						prev = prev[:80] + "…"
					}
					fmt.Printf("[watch] sending inline bytes=%d hash=%s preview=%q\n", len(data), msgID, prev)
				}
				if err := runSendTextWithMsgID(ctx, c, txt, msgID); err != nil {
					fmt.Fprintln(os.Stderr, "send text failed:", err)
					continue
				}
			} else {
				tmp, err := os.CreateTemp("", "clipsync-*.txt")
				if err != nil {
					fmt.Fprintln(os.Stderr, "tmp file error:", err)
					continue
				}
				tmpPath := tmp.Name()
				_, _ = tmp.Write(data)
				_ = tmp.Close()
				msgID := "h-" + hashString(txt)
				if verbose {
					fmt.Printf("[watch] sending large via upload bytes=%d hash=%s tmp=%s\n", len(data), msgID, tmpPath)
				}
				if err := runSendFileWithMsgID(ctx, c, wsAddr, tmpPath, "text/plain", msgID); err != nil {
					fmt.Fprintln(os.Stderr, "send file failed:", err)
				}
				_ = os.Remove(tmpPath)
			}
			lastLocal = h
		}
	}
}

func runSendText(ctx context.Context, c *websocket.Conn, text string) error {
	data := []byte(text)
	if len(data) > types.MaxInlineBytes {
		return fmt.Errorf("text payload is %d bytes; exceeds MaxInlineBytes=%d — use --file",
			len(data), types.MaxInlineBytes)
	}
	env := types.Envelope{
		Type: "clip",
		Clip: &types.Clip{
			MsgID: "m-" + time.Now().UTC().Format("20060102T150405.000Z0700"),
			Mime:  "text/plain",
			Size:  len(data),
			Data:  data,
		},
	}
	return wsjson.Write(ctx, c, env)
}

func runSendTextWithMsgID(ctx context.Context, c *websocket.Conn, text, msgID string) error {
	data := []byte(text)
	if len(data) > types.MaxInlineBytes {
		return fmt.Errorf("text payload is %d bytes; exceeds MaxInlineBytes=%d — use --file",
			len(data), types.MaxInlineBytes)
	}
	if msgID == "" {
		msgID = "m-" + time.Now().UTC().Format("20060102T150405.000Z0700")
	}
	env := types.Envelope{
		Type: "clip",
		Clip: &types.Clip{
			MsgID: msgID,
			Mime:  "text/plain",
			Size:  len(data),
			Data:  data,
		},
	}
	return wsjson.Write(ctx, c, env)
}

func runSendFile(ctx context.Context, c *websocket.Conn, wsAddr, path, mimeType string) error {
	base := httpBaseFromWS(wsAddr)
	upCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if mimeType == "" {
		mimeType = detectMime(path, "application/octet-stream")
	}
	uploadURL, size, err := uploadFile(upCtx, base, path, mimeType)
	if err != nil {
		return err
	}

	env := types.Envelope{
		Type: "clip",
		Clip: &types.Clip{
			MsgID:     "m-" + time.Now().UTC().Format("20060102T150405.000Z0700"),
			Mime:      mimeType,
			Size:      size,
			UploadURL: uploadURL,
		},
	}
	if err := wsjson.Write(ctx, c, env); err != nil {
		return err
	}
	fmt.Printf("sent file: %s (%d bytes) url=%s\n", path, size, uploadURL)
	return nil
}

func runSendFileWithMsgID(ctx context.Context, c *websocket.Conn, wsAddr, path, mimeType, msgID string) error {
	base := httpBaseFromWS(wsAddr)
	upCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if mimeType == "" {
		mimeType = detectMime(path, "application/octet-stream")
	}
	uploadURL, size, err := uploadFile(upCtx, base, path, mimeType)
	if err != nil {
		return err
	}
	if msgID == "" {
		msgID = "m-" + time.Now().UTC().Format("20060102T150405.000Z0700")
	}
	env := types.Envelope{
		Type: "clip",
		Clip: &types.Clip{
			MsgID:     msgID,
			Mime:      mimeType,
			Size:      size,
			UploadURL: uploadURL,
		},
	}
	if err := wsjson.Write(ctx, c, env); err != nil {
		return err
	}
	fmt.Printf("sent file: %s (%d bytes) url=%s\n", path, size, uploadURL)
	return nil
}

/* ---------- main ---------- */

// version se puede fijar en el build: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	addr := flag.String("addr", "ws://localhost:8080/ws", "WebSocket endpoint")
	token := flag.String("token", "u1", "user token (MVP: token == userID)")
	device := flag.String("device", "A", "device id (unique per device)")
	mode := flag.String("mode", "listen", "listen|send|recv|watch|sync")
	text := flag.String("text", "", "text to send (send mode). If empty, read from stdin")
	file := flag.String("file", "", "path to file to send (uses HTTP /upload)")
	mimeFlag := flag.String("mime", "", "mime type for --file (auto-detect if empty)")
	poll := flag.Int("poll-ms", 400, "clipboard poll interval for watch/sync")
	verbose := flag.Bool("v", false, "verbose logging (debug)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *poll < 50 {
		fatalf(exitUsage, "--poll-ms must be >= 50 (got %d)", *poll)
	}

	// Ctrl-C / SIGTERM cierran limpiamente en vez de dejar procesos colgados.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	interval := time.Duration(*poll) * time.Millisecond

	switch *mode {
	case "listen":
		_ = supervise(ctx, *addr, *token, *device, "listen", *verbose, runListen)

	case "recv":
		_ = supervise(ctx, *addr, *token, *device, "recv", *verbose,
			func(c context.Context, w *websocket.Conn) error {
				// recv-only no necesita evitar el eco local
				return runRecvApply(c, w, *addr, func(string) {}, *verbose)
			})

	case "watch":
		_ = supervise(ctx, *addr, *token, *device, "watch", *verbose,
			func(c context.Context, w *websocket.Conn) error {
				var mu sync.Mutex
				lr := ""
				getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
				clearLR := func() { mu.Lock(); lr = ""; mu.Unlock() }
				return runWatchLoop(c, w, *addr, interval, getLR, clearLR, *verbose)
			})

	case "sync":
		_ = supervise(ctx, *addr, *token, *device, "sync", *verbose, syncSession(*addr, interval, *verbose))

	case "send":
		runSendMode(ctx, *addr, *token, *device, *text, *file, *mimeFlag)

	default:
		fatalf(exitUsage, "unknown -mode=%q (use listen|send|recv|watch|sync)", *mode)
	}
}

// runSendMode es one-shot: reintenta la conexión un número acotado de veces y
// termina con un código de salida estable.
func runSendMode(ctx context.Context, addr, token, device, text, file, mimeType string) {
	var c *websocket.Conn
	var err error
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ct, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, err = dialAndHello(ct, addr, token, device)
		cancel()
		if err == nil {
			break
		}
		if attempt == maxAttempts-1 {
			fatalf(exitConn, "connect failed: %v", err)
		}
		fmt.Fprintln(os.Stderr, "connect failed:", err)
		sleepBackoff(attempt)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if file != "" {
		if err := runSendFile(ctx, c, addr, file, mimeType); err != nil {
			fatalf(exitUpload, "%v", err)
		}
		return
	}

	// --text se envía tal cual: recortarlo cambiaba silenciosamente lo que
	// el usuario pidió enviar.
	if text != "" {
		if err := runSendText(ctx, c, text); err != nil {
			fatalf(exitSend, "%v", err)
		}
		fmt.Println("sent")
		return
	}

	// stable pipe mode: if stdin is piped, read and decide inline vs upload
	if isInputFromPipe() {
		data, tmpPath, _, mt, err := readToBufferOrFile(os.Stdin, types.MaxInlineBytes)
		if err != nil {
			fatalf(exitUsage, "stdin read error: %v", err)
		}
		if tmpPath != "" {
			defer os.Remove(tmpPath)
			if err := runSendFile(ctx, c, addr, tmpPath, mt); err != nil {
				fatalf(exitUpload, "%v", err)
			}
			return
		}
		if len(data) == 0 {
			fatalf(exitUsage, "send mode: stdin was empty")
		}
		if err := runSendText(ctx, c, string(data)); err != nil {
			fatalf(exitSend, "%v", err)
		}
		fmt.Println("sent")
		return
	}

	fatalf(exitUsage, "send mode: provide --text or --file (or pipe stdin)")
}

// hashing helpers for dedupe
func hashString(s string) string { return hashBytes([]byte(s)) }
func hashBytes(b []byte) string {
	var x uint64 = 1469598103934665603
	const prime = 1099511628211
	for _, c := range b {
		x ^= uint64(c)
		x *= prime
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = hexdigits[x&0xF]
		x >>= 4
	}
	return string(out)
}

// isInputFromPipe returns true if stdin is not a TTY/character device (i.e., data is being piped).
func isInputFromPipe() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

// readToBufferOrFile reads from r. If size <= maxInline, returns data in memory and empty tmpPath.
// If size > maxInline, spills to a temp file and returns its path and a guessed MIME.
func readToBufferOrFile(r io.Reader, maxInline int) (data []byte, tmpPath string, size int, mimeType string, err error) {
	// heuristic for text vs binary: we will track a rolling UTF-8 validity while reading
	buf := make([]byte, 32*1024)
	var mem []byte
	var f *os.File
	total := 0
	textLikely := true
	for {
		n, er := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			total += n
			if textLikely && !utf8.Valid(chunk) {
				textLikely = false
			}
			if f == nil && len(mem)+n <= maxInline {
				mem = append(mem, chunk...)
			} else {
				if f == nil {
					// spill
					var e error
					f, e = os.CreateTemp("", "clipsync-pipe-*.bin")
					if e != nil {
						return nil, "", 0, "", e
					}
					if len(mem) > 0 {
						if _, e = f.Write(mem); e != nil {
							f.Close()
							os.Remove(f.Name())
							return nil, "", 0, "", e
						}
						mem = nil
					}
				}
				if _, e := f.Write(chunk); e != nil {
					f.Close()
					os.Remove(f.Name())
					return nil, "", 0, "", e
				}
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return nil, "", 0, "", er
		}
	}
	if f != nil {
		_ = f.Sync()
		_ = f.Close()
		mt := "application/octet-stream"
		if textLikely {
			mt = "text/plain"
		}
		return nil, f.Name(), total, mt, nil
	}
	mt := "application/octet-stream"
	if textLikely {
		mt = "text/plain"
	}
	return mem, "", total, mt, nil
}

// detectMime returns mime type by file extension; fallback if unknown.
func detectMime(path, fallback string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if v := extMime[ext]; v != "" {
		return v
	}
	if t := mime.TypeByExtension(ext); t != "" {
		if semi := strings.IndexByte(t, ';'); semi > 0 {
			t = strings.TrimSpace(t[:semi])
		}
		return t
	}
	if fallback == "" {
		return "application/octet-stream"
	}
	return fallback
}

var extMime = map[string]string{
	".txt":  "text/plain",
	".md":   "text/markdown",
	".json": "application/json",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".html": "text/html",
}
