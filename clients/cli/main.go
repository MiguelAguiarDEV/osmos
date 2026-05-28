package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
)

// maxClipDownload caps how much a received upload_url clip may pull into memory
// before being applied to the clipboard.
const maxClipDownload = 64 << 20 // 64 MiB

// cliToken is the auth token for HTTP upload/download, set once in main.
var cliToken string

func fatalf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

func sleepBackoff(attempt int) { time.Sleep(computeBackoff(attempt)) }

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

func dialAndHello(ctx context.Context, addr, token, device string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(types.WSReadLimit(types.MaxInlineBytes))
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

// httpBaseFromWS derives the HTTP base (scheme://host) from a ws endpoint.
// The /upload and /d/{id} routes live at the root, so the ws path (e.g. "/ws")
// must be dropped — otherwise uploads hit "/ws/upload" and 404.
func httpBaseFromWS(wsAddr string) string {
	s := wsAddr
	switch {
	case strings.HasPrefix(s, "wss://"):
		s = "https://" + strings.TrimPrefix(s, "wss://")
	case strings.HasPrefix(s, "ws://"):
		s = "http://" + strings.TrimPrefix(s, "ws://")
	case !strings.Contains(s, "://"):
		s = "http://" + s
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return s
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
	if cliToken != "" {
		req.Header.Set("Authorization", "Bearer "+cliToken)
	}

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
				dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
				req, _ := http.NewRequestWithContext(dctx, http.MethodGet, u, nil)
				if cliToken != "" {
					req.Header.Set("Authorization", "Bearer "+cliToken)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					dcancel()
					fmt.Fprintln(os.Stderr, "download failed:", err)
					continue
				}
				// Bound the read: don't let a huge/slow blob OOM us or block the
				// read loop indefinitely.
				b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxClipDownload+1))
				resp.Body.Close()
				dcancel()
				if resp.StatusCode != http.StatusOK {
					fmt.Fprintf(os.Stderr, "download failed: status=%d\n", resp.StatusCode)
					continue
				}
				if rerr != nil {
					fmt.Fprintln(os.Stderr, "download read failed:", rerr)
					continue
				}
				if len(b) > maxClipDownload {
					fmt.Fprintf(os.Stderr, "download too large (> %d bytes), skipping\n", maxClipDownload)
					continue
				}
				data = b
			}
			if len(data) == 0 {
				continue
			}
			if verbose {
				fmt.Printf("[recv] applying to clipboard: from=%s bytes=%d backend=%s\n", env.From, len(data), clipboardWriteBackend())
			}
			if err := setClipboardText(string(data)); err != nil {
				fmt.Fprintln(os.Stderr, "set clipboard failed:", err)
				continue
			}
			markRemote(hashBytes(data))
			// Re-read the clipboard once: mark the hash of what it actually holds
			// after applying, not the bytes we received. Some backends are
			// non-idempotent on read-back (e.g. Windows Get-Clipboard appends a
			// trailing newline), which would otherwise make the watcher treat the
			// applied value as a new local change and re-send it — a sync loop.
			applied := string(data)
			readback, rerr := getClipboardText()
			if rerr == nil {
				applied = readback
				markRemote(hashBytes([]byte(applied)))
			}
			if verbose {
				if rerr == nil {
					ok := applied == string(data)
					prev := applied
					if len(prev) > 80 {
						prev = prev[:80] + "…"
					}
					fmt.Printf("[recv] clipboard updated from %s, verify=%v len=%d preview=%q\n", env.From, ok, len(applied), prev)
				} else {
					fmt.Printf("[recv] clipboard updated from %s (unable to read back: %v)\n", env.From, rerr)
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			txt, err := getClipboardText()
			if err != nil {
				return err
			}
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
					// a websocket write failure means the connection is gone;
					// return so the caller can reconnect.
					return fmt.Errorf("send text: %w", err)
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

/* ---------- session lifecycle ---------- */

// keepAlive pings periodically; on failure (dead or half-open connection) it
// closes the socket and cancels the connection context so the active loop
// returns and the caller can reconnect.
func keepAlive(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				_ = c.Close(websocket.StatusGoingAway, "ping timeout")
				cancel()
				return
			}
		}
	}
}

// drainReads consumes and discards incoming messages. Needed when a mode only
// writes (watch): without an active reader, coder/websocket never processes
// PONG frames (so keep-alive would falsely time out) and the server's write
// buffer to this client would back up. Cancels when the connection errors.
func drainReads(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	for {
		if _, _, err := c.Read(ctx); err != nil {
			cancel()
			return
		}
	}
}

// runWithReconnect keeps a long-running session alive: dial, run fn until it
// returns (connection lost), then reconnect with exponential backoff. fn gets a
// context cancelled when keep-alive detects a dead connection.
func runWithReconnect(addr, token, device string, fn func(ctx context.Context, c *websocket.Conn) error) {
	for attempt := 0; ; attempt++ {
		ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := dialAndHello(ct, addr, token, device)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "connect failed:", err)
			sleepBackoff(attempt)
			continue
		}
		fmt.Fprintf(os.Stderr, "connected to %s as %s\n", addr, device)
		connCtx, connCancel := context.WithCancel(context.Background())
		go keepAlive(connCtx, c, connCancel)
		start := time.Now()
		err = fn(connCtx, c)
		connCancel()
		_ = c.Close(websocket.StatusNormalClosure, "")
		// Permanent rejection (bad token, expired HMAC, invalid device_id): stop
		// instead of reconnecting forever and hammering the server.
		if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
			fatalf(exitConn, "rejected by server: %v", err)
		}
		if err != nil && connCtx.Err() == nil {
			fmt.Fprintln(os.Stderr, "session ended:", err)
		}
		// Only reset the backoff if the session was actually healthy for a while;
		// otherwise instant-fail sessions would reconnect in a tight loop.
		if time.Since(start) >= 5*time.Second {
			attempt = 0
		}
		sleepBackoff(attempt)
	}
}

/* ---------- main ---------- */

func main() {
	addr := flag.String("addr", "ws://localhost:8080/ws", "WebSocket endpoint")
	token := flag.String("token", "u1", "user token (MVP: token == userID)")
	device := flag.String("device", "A", "device id (unique per device)")
	mode := flag.String("mode", "listen", "listen|send|recv|watch|sync")
	text := flag.String("text", "", "text to send (send mode). If empty, read from stdin")
	file := flag.String("file", "", "path to file to send (uses HTTP /upload)")
	mime := flag.String("mime", "", "mime type for --file (auto-detect if empty)")
	poll := flag.Int("poll-ms", 400, "clipboard poll interval for watch/sync")
	verbose := flag.Bool("v", false, "verbose logging (debug)")
	flag.Parse()
	cliToken = *token

	switch *mode {
	case "listen":
		runWithReconnect(*addr, *token, *device, func(ctx context.Context, c *websocket.Conn) error {
			return runListen(ctx, c)
		})

	case "send":
		var c *websocket.Conn
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c, err = dialAndHello(ct, *addr, *token, *device)
			cancel()
			if err == nil {
				break
			}
			if attempt == 4 {
				fatalf(exitConn, "connect failed: %v", err)
			}
			fmt.Fprintln(os.Stderr, "connect failed:", err)
			sleepBackoff(attempt)
		}
		defer c.Close(websocket.StatusNormalClosure, "")

		if *file != "" {
			if err := runSendFile(context.Background(), c, *addr, *file, *mime); err != nil {
				fatalf(exitUpload, "%v", err)
			}
			return
		}

		payload := strings.TrimSpace(*text)
		if payload != "" {
			if err := runSendText(context.Background(), c, payload); err != nil {
				fatalf(exitSend, "%v", err)
			}
			fmt.Println("sent")
			return
		}

		// stable pipe mode: if stdin is piped, read and decide inline vs upload
		if isInputFromPipe() {
			data, tmpPath, size, mimeType, err := readToBufferOrFile(os.Stdin, types.MaxInlineBytes)
			if err != nil {
				fatalf(exitUsage, "stdin read error: %v", err)
			}
			if tmpPath != "" {
				defer os.Remove(tmpPath)
				if err := runSendFile(context.Background(), c, *addr, tmpPath, mimeType); err != nil {
					fatalf(exitUpload, "%v", err)
				}
				return
			}
			_ = size // already len(data)
			if err := runSendText(context.Background(), c, string(data)); err != nil {
				fatalf(exitSend, "%v", err)
			}
			fmt.Println("sent")
			return
		}

		fatalf(exitUsage, "send mode: provide --text or --file (or pipe stdin)")

	case "recv":
		runWithReconnect(*addr, *token, *device, func(ctx context.Context, c *websocket.Conn) error {
			return runRecvApply(ctx, c, *addr, func(string) {}, *verbose)
		})

	case "watch":
		var mu sync.Mutex
		lr := ""
		getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
		clearLR := func() { mu.Lock(); lr = ""; mu.Unlock() }
		runWithReconnect(*addr, *token, *device, func(ctx context.Context, c *websocket.Conn) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			// Drain incoming so PONGs are processed (keep-alive) and broadcasts
			// from other devices don't back up on the server.
			go drainReads(ctx, c, cancel)
			return runWatchLoop(ctx, c, *addr, time.Duration(*poll)*time.Millisecond, getLR, clearLR, *verbose)
		})

	case "sync":
		var mu sync.Mutex
		lr := ""
		getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
		clearLR := func() { mu.Lock(); lr = ""; mu.Unlock() }
		mark := func(h string) { mu.Lock(); lr = h; mu.Unlock() }
		runWithReconnect(*addr, *token, *device, func(ctx context.Context, c *websocket.Conn) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			// If recv stops (the connection dropped on the read side), cancel so
			// the watch loop returns and the session reconnects — otherwise a
			// passive sender would not notice the drop until the next keep-alive.
			go func() {
				_ = runRecvApply(ctx, c, *addr, mark, *verbose)
				cancel()
			}()
			return runWatchLoop(ctx, c, *addr, time.Duration(*poll)*time.Millisecond, getLR, clearLR, *verbose)
		})

	default:
		fatalf(exitUsage, "unknown -mode=%q (use listen|send|recv|watch|sync)", *mode)
	}
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
