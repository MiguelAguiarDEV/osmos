package main

import (
    "context"
    "flag"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/signal"
    "strconv"
    "time"

    "clip-sync/server/internal/app"
)

func envOr(name, def string) string {
    if v := os.Getenv(name); v != "" {
        return v
    }
    return def
}

func main() {
    // Flags con fallback a env
    addr := flag.String("addr", envOr("CLIPSYNC_ADDR", ":8080"), "listen address, e.g. :8080 or 127.0.0.1:8080")
    uploadDir := flag.String("upload-dir", envOr("CLIPSYNC_UPLOAD_DIR", "./uploads"), "directory for uploaded files")
    uploadMax := flag.Int("upload-max-bytes", func() int { if v := os.Getenv("CLIPSYNC_UPLOAD_MAXBYTES"); v != "" { if n, err := strconv.Atoi(v); err == nil { return n } }; return 50 << 20 }(), "max bytes accepted by /upload")
    uploadAllowed := flag.String("upload-allowed", envOr("CLIPSYNC_UPLOAD_ALLOWED", ""), "comma-separated list of allowed MIME types (e.g. text/plain,image/*). Empty disables whitelist")
    inlineMax := flag.Int("inline-max-bytes", func() int { if v := os.Getenv("CLIPSYNC_INLINE_MAXBYTES"); v != "" { if n, err := strconv.Atoi(v); err == nil { return n } }; return 64 << 10 }(), "max inline clip size")
    uploadTTL := flag.Duration("upload-ttl", func() time.Duration { if v := os.Getenv("CLIPSYNC_UPLOAD_TTL"); v != "" { if d, err := time.ParseDuration(v); err == nil { return d } }; return 0 }(), "delete uploads older than this; 0 disables (e.g. 24h)")
    logLevel := flag.String("log-level", envOr("CLIPSYNC_LOG_LEVEL", "info"), "log level: debug|info|error|off")
    pprofEn := flag.Bool("pprof", envOr("CLIPSYNC_PPROF", "") != "", "enable /debug/pprof endpoints")
    expvarEn := flag.Bool("expvar", envOr("CLIPSYNC_EXPVAR", "") != "", "enable /debug/vars endpoint")
    flag.Parse()

    // Pasar flags a env para que NewApp los tome
    _ = os.Setenv("CLIPSYNC_UPLOAD_DIR", *uploadDir)
    _ = os.Setenv("CLIPSYNC_UPLOAD_MAXBYTES", fmt.Sprintf("%d", *uploadMax))
    _ = os.Setenv("CLIPSYNC_INLINE_MAXBYTES", fmt.Sprintf("%d", *inlineMax))
    _ = os.Setenv("CLIPSYNC_UPLOAD_ALLOWED", *uploadAllowed)
    _ = os.Setenv("CLIPSYNC_UPLOAD_TTL", uploadTTL.String())
    _ = os.Setenv("CLIPSYNC_LOG_LEVEL", *logLevel)
    if *pprofEn { _ = os.Setenv("CLIPSYNC_PPROF", "1") } else { _ = os.Unsetenv("CLIPSYNC_PPROF") }
    if *expvarEn { _ = os.Setenv("CLIPSYNC_EXPVAR", "1") } else { _ = os.Unsetenv("CLIPSYNC_EXPVAR") }

    a := app.NewApp()

    janitorCtx, janitorStop := context.WithCancel(context.Background())
    a.Uploads.StartJanitor(janitorCtx, 10*time.Minute)

    srv := &http.Server{
        Addr:    *addr,
        Handler: app.WithHTTPLogging(a.Mux),
    }

    go func() {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("listen: %v", err)
        }
    }()
    log.Printf("clip-sync server listening on %s\n", *addr)

    stop := make(chan os.Signal, 1)
    signal.Notify(stop, os.Interrupt)
    <-stop

    log.Println("shutting down...")
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    janitorStop()
    a.WSS.Shutdown(ctx)
    if err := srv.Shutdown(ctx); err != nil {
        log.Printf("http shutdown: %v", err)
    }
    log.Println("bye")
}
