package app

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "expvar"
    "net/http"
    "net/http/pprof"
    "os"
    "strconv"
    "strings"
    "time"

    "clip-sync/server/internal/broker"
    "clip-sync/server/internal/httpapi"
    "clip-sync/server/internal/logx"
    "clip-sync/server/internal/ws"
)

type App struct {
	Mux     *http.ServeMux
	WSS     *ws.Server
	Uploads *httpapi.UploadServer
}

func NewApp() *App {
    mux := http.NewServeMux()
    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    // configurar nivel de logs
    if lvl := os.Getenv("CLIPSYNC_LOG_LEVEL"); lvl != "" {
        logx.SetLevel(lvl)
    }
    // Shared authentication: MVP (token == userID) or HMAC if a secret is set.
    auth := func(token string) (string, bool) {
        secret := os.Getenv("CLIPSYNC_HMAC_SECRET")
        if secret == "" {
            if token == "" {
                return "", false
            }
            // modo MVP: token == userID
            return token, true
        }
        return verifyHMACToken(token, secret)
    }
    wss := &ws.Server{
        Auth:               auth,
        MaxInlineBytes:     envInt("CLIPSYNC_INLINE_MAXBYTES", 64<<10),
        // Generous per-device cap: real clipboard traffic is well under this
        // (watch polls a few times/sec), but it bounds a pathological flood.
        // Set CLIPSYNC_RATE_LPS=0 / --rate-lps 0 to disable.
        RateLimitPerSecond: envInt("CLIPSYNC_RATE_LPS", 100),
        Log: func(event string, fields map[string]any) {
            logx.Info(event, fields)
        },
    }
	// dedupe: capacidad LRU por usuario desde env (0 = off)
	wss.SetDedupeCapacity(envInt("CLIPSYNC_DEDUPE", 128))

	// fan-out broker: Redis for multi-instance, else single-instance Local.
	if url := os.Getenv("CLIPSYNC_REDIS_URL"); url != "" {
		if rb, err := broker.NewRedis(url, envStr("CLIPSYNC_REDIS_CHANNEL", "clipsync")); err != nil {
			logx.Error("redis_broker_init_failed", map[string]any{"error": err.Error(), "fallback": "local"})
			wss.SetBroker(broker.NewLocal())
		} else {
			logx.Info("redis_broker", map[string]any{"channel": envStr("CLIPSYNC_REDIS_CHANNEL", "clipsync")})
			wss.SetBroker(rb)
		}
	} else {
		wss.SetBroker(broker.NewLocal())
	}

	mux.Handle("/ws", wss)

    up := &httpapi.UploadServer{
        Dir:      envStr("CLIPSYNC_UPLOAD_DIR", "./uploads"),
        MaxBytes: int64(envInt("CLIPSYNC_UPLOAD_MAXBYTES", 50<<20)),
        Allowed:  splitCSV(envStr("CLIPSYNC_UPLOAD_ALLOWED", "")),
        TTL:      envDuration("CLIPSYNC_UPLOAD_TTL", 0),
        Auth:     auth,
    }
    mux.HandleFunc("POST /upload", up.Upload)
    mux.HandleFunc("GET /d/{id}", up.Download)

    // debug endpoints (opt-in)
    if envInt("CLIPSYNC_PPROF", 0) != 0 {
        mux.HandleFunc("/debug/pprof/", pprof.Index)
        mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
        mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
        mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
        mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
    }
    if envInt("CLIPSYNC_EXPVAR", 0) != 0 {
        mux.HandleFunc("/debug/vars", expvar.Handler().ServeHTTP)
    }

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wss.MetricsSnapshot())
	})

	return &App{Mux: mux, WSS: wss, Uploads: up}
}

// Back-compat
func NewMux() http.Handler { return NewApp().Mux }

func envInt(name string, def int) int {
    v := os.Getenv(name)
    if v == "" {
        return def
    }
    n, err := strconv.Atoi(v)
    if err != nil {
        return def
    }
    return n
}

func envStr(name, def string) string {
    v := os.Getenv(name)
    if v == "" {
        return def
    }
    return v
}

func envDuration(name string, def time.Duration) time.Duration {
    v := os.Getenv(name)
    if v == "" {
        return def
    }
    d, err := time.ParseDuration(v)
    if err != nil {
        return def
    }
    return d
}

func splitCSV(s string) []string {
    if s == "" {
        return nil
    }
    parts := strings.Split(s, ",")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}

// verifyHMACToken valida tokens con formato: userID:exp_unix:hex(hmac_sha256(secret, userID|exp_unix))
func verifyHMACToken(token, secret string) (string, bool) {
    parts := strings.Split(token, ":")
    if len(parts) != 3 {
        return "", false
    }
    uid, expStr, macHex := parts[0], parts[1], parts[2]
    if uid == "" || expStr == "" || macHex == "" {
        return "", false
    }
    exp, err := strconv.ParseInt(expStr, 10, 64)
    if err != nil {
        return "", false
    }
    if time.Now().Unix() > exp {
        return "", false
    }
    payload := uid + "|" + expStr
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(payload))
    sum := mac.Sum(nil)
    want := hex.EncodeToString(sum)
    if !hmac.Equal([]byte(macHex), []byte(want)) {
        return "", false
    }
    return uid, true
}
