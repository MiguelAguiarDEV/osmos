package main

import (
    "context"
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "mime"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "unicode/utf8"
    "time"

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

func httpBaseFromWS(wsAddr string) string {
	if strings.HasPrefix(wsAddr, "wss://") {
		return "https://" + strings.TrimPrefix(wsAddr, "wss://")
	}
	if strings.HasPrefix(wsAddr, "ws://") {
		return "http://" + strings.TrimPrefix(wsAddr, "ws://")
	}
	return "http://" + wsAddr
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
        if dd.ExistsOrAdd(cl.MsgID) { continue }
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
            if verbose {
                fmt.Printf("[recv] applying to clipboard: from=%s bytes=%d backend=%s\n", env.From, len(data), clipboardWriteBackend())
            }
            if err := setClipboardText(string(data)); err != nil {
                fmt.Fprintln(os.Stderr, "set clipboard failed:", err)
                continue
            }
            markRemote(hashBytes(data))
            if verbose {
                // read-back validation
                if rb, err := getClipboardText(); err == nil {
                    ok := rb == string(data)
                    prev := rb
                    if len(prev) > 80 { prev = prev[:80] + "…" }
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
                    if len(prev) > 80 { prev = prev[:80] + "…" }
                    fmt.Printf("[watch] sending inline bytes=%d hash=%s preview=%q\n", len(data), msgID, prev)
                }
                if err := runSendTextWithMsgID(ctx, c, txt, msgID); err != nil {
                    fmt.Fprintln(os.Stderr, "send text failed:", err)
                    continue
                }
            } else {
                tmp, err := os.CreateTemp("", "clipsync-*.txt")
                if err != nil { fmt.Fprintln(os.Stderr, "tmp file error:", err); continue }
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
    if msgID == "" { msgID = "m-" + time.Now().UTC().Format("20060102T150405.000Z0700") }
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
    if mimeType == "" { mimeType = detectMime(path, "application/octet-stream") }
    uploadURL, size, err := uploadFile(upCtx, base, path, mimeType)
    if err != nil { return err }
    if msgID == "" { msgID = "m-" + time.Now().UTC().Format("20060102T150405.000Z0700") }
    env := types.Envelope{
        Type: "clip",
        Clip: &types.Clip{
            MsgID:     msgID,
            Mime:      mimeType,
            Size:      size,
            UploadURL: uploadURL,
        },
    }
    if err := wsjson.Write(ctx, c, env); err != nil { return err }
    fmt.Printf("sent file: %s (%d bytes) url=%s\n", path, size, uploadURL)
    return nil
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

	switch *mode {
	case "listen":
        for attempt := 0; ; attempt++ {
            ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            c, err := dialAndHello(ct, *addr, *token, *device)
            cancel()
            if err != nil {
                fmt.Fprintln(os.Stderr, "connect failed:", err)
                sleepBackoff(attempt)
                continue
            }
            fmt.Printf("connected to %s as %s (listening)\n", *addr, *device)
            attempt = 0 // reset after a successful connect
            if err := runListen(context.Background(), c); err != nil {
                fmt.Fprintln(os.Stderr, "listen error:", err)
            }
            sleepBackoff(attempt)
        }

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
			// small payload fits inline
			_ = size // already len(data)
			if err := runSendText(context.Background(), c, string(data)); err != nil {
				fatalf(exitSend, "%v", err)
			}
			fmt.Println("sent")
			return
		}

		fatalf(exitUsage, "send mode: provide --text or --file (or pipe stdin)")

    default:
        switch *mode {
        case "recv":
            var c *websocket.Conn
            var err error
            for attempt := 0; attempt < 5; attempt++ {
                ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                c, err = dialAndHello(ct, *addr, *token, *device)
                cancel()
                if err == nil { break }
                if attempt == 4 { fatalf(exitConn, "connect failed: %v", err) }
                fmt.Fprintln(os.Stderr, "connect failed:", err)
                sleepBackoff(attempt)
            }
            defer c.Close(websocket.StatusNormalClosure, "")
            // recv-only does not need local echo prevention state
            mark := func(string){}
            if err := runRecvApply(context.Background(), c, *addr, mark, *verbose); err != nil {
                fatalf(exitSend, "%v", err)
            }
        case "watch":
            var c *websocket.Conn
            var err error
            for attempt := 0; attempt < 5; attempt++ {
                ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                c, err = dialAndHello(ct, *addr, *token, *device)
                cancel()
                if err == nil { break }
                if attempt == 4 { fatalf(exitConn, "connect failed: %v", err) }
                fmt.Fprintln(os.Stderr, "connect failed:", err)
                sleepBackoff(attempt)
            }
            defer c.Close(websocket.StatusNormalClosure, "")
            var mu sync.Mutex
            lr := ""
            getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
            clearLR := func(){ mu.Lock(); lr = ""; mu.Unlock() }
            if err := runWatchLoop(context.Background(), c, *addr, time.Duration(*poll)*time.Millisecond, getLR, clearLR, *verbose); err != nil {
                fatalf(exitSend, "%v", err)
            }
        case "sync":
            var c *websocket.Conn
            var err error
            for attempt := 0; attempt < 5; attempt++ {
                ct, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                c, err = dialAndHello(ct, *addr, *token, *device)
                cancel()
                if err == nil { break }
                if attempt == 4 { fatalf(exitConn, "connect failed: %v", err) }
                fmt.Fprintln(os.Stderr, "connect failed:", err)
                sleepBackoff(attempt)
            }
            defer c.Close(websocket.StatusNormalClosure, "")
            var mu sync.Mutex
            lr := ""
            getLR := func() string { mu.Lock(); defer mu.Unlock(); return lr }
            clearLR := func(){ mu.Lock(); lr = ""; mu.Unlock() }
            mark := func(h string){ mu.Lock(); lr = h; mu.Unlock() }
            ctx := context.Background()
            go func(){ _ = runRecvApply(ctx, c, *addr, mark, *verbose) }()
            if err := runWatchLoop(ctx, c, *addr, time.Duration(*poll)*time.Millisecond, getLR, clearLR, *verbose); err != nil {
                fatalf(exitSend, "%v", err)
            }
        default:
            fatalf(exitUsage, "unknown -mode=%q (use listen|send|recv|watch|sync)", *mode)
        }
    }
}

// hashing helpers for dedupe
func hashString(s string) string { return hashBytes([]byte(s)) }
func hashBytes(b []byte) string {
    var x uint64 = 1469598103934665603
    const prime = 1099511628211
    for _, c := range b { x ^= uint64(c); x *= prime }
    const hexdigits = "0123456789abcdef"
    out := make([]byte, 16)
    for i := 15; i >= 0; i-- { out[i] = hexdigits[x&0xF]; x >>= 4 }
    return string(out)
}

// isInputFromPipe returns true if stdin is not a TTY/character device (i.e., data is being piped).
func isInputFromPipe() bool {
    fi, err := os.Stdin.Stat()
    if err != nil { return false }
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
            if textLikely && !utf8.Valid(chunk) { textLikely = false }
            if f == nil && len(mem)+n <= maxInline {
                mem = append(mem, chunk...)
            } else {
                if f == nil {
                    // spill
                    var e error
                    f, e = os.CreateTemp("", "clipsync-pipe-*.bin")
                    if e != nil { return nil, "", 0, "", e }
                    if len(mem) > 0 {
                        if _, e = f.Write(mem); e != nil { f.Close(); os.Remove(f.Name()); return nil, "", 0, "", e }
                        mem = nil
                    }
                }
                if _, e := f.Write(chunk); e != nil {
                    f.Close(); os.Remove(f.Name()); return nil, "", 0, "", e
                }
            }
        }
        if er == io.EOF { break }
        if er != nil { return nil, "", 0, "", er }
    }
    if f != nil {
        _ = f.Sync(); _ = f.Close()
        mt := "application/octet-stream"
        if textLikely { mt = "text/plain" }
        return nil, f.Name(), total, mt, nil
    }
    mt := "application/octet-stream"
    if textLikely { mt = "text/plain" }
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
