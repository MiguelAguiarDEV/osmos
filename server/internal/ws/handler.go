package ws

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

/* -------- rate limit -------- */

type limiter struct {
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
}

func newLimiter(rps int) *limiter {
	r := float64(rps)
	if r <= 0 {
		r = 1e9
	}
	now := time.Now()
	return &limiter{rate: r, capacity: r, tokens: r, last: now}
}

func (l *limiter) allow() bool {
	now := time.Now()
	el := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens += el * l.rate
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	if l.tokens >= 1 {
		l.tokens -= 1
		return true
	}
	return false
}

/* -------- dedupe LRU corta -------- */

type dedupeCache struct {
	cap  int
	keys []string
	set  map[string]struct{}
}

func newDedupe(capacity int) *dedupeCache {
	if capacity <= 0 {
		capacity = 0
	}
	return &dedupeCache{cap: capacity, set: make(map[string]struct{}, capacity)}
}
func (d *dedupeCache) ExistsOrAdd(id string) bool {
	if d.cap == 0 || id == "" {
		return false
	}
	if _, ok := d.set[id]; ok {
		return true
	}
	d.set[id] = struct{}{}
	d.keys = append(d.keys, id)
	if len(d.keys) > d.cap {
		ev := d.keys[0]
		d.keys = d.keys[1:]
		delete(d.set, ev)
	}
	return false
}

type Server struct {
	Auth               func(token string) (string, bool)
	MaxInlineBytes     int
	RateLimitPerSecond int

	// helloTimeout bounds how long an unauthenticated connection may stay open
	// before sending a valid hello (0 = defaultHelloTimeout).
	helloTimeout time.Duration

	// logger: si es nil, no loggea
	Log func(event string, fields map[string]any)

	mu    sync.RWMutex
	conns map[string]map[string]*websocket.Conn // userID -> deviceID -> conn

	rlmu sync.Mutex
	rl   map[string]*limiter // key: userID|deviceID

	ddmu    sync.Mutex
	ddcap   int                     // capacidad LRU por usuario
	dd      map[string]*dedupeCache // userID -> LRU
	metrics struct {
		clips int64
		drops int64
		conns int64
	}

	// backpressure visible: drops por device (userID|deviceID)
	dropsMu       sync.Mutex
	dropsByDevice map[string]int64
}

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const defaultHelloTimeout = 10 * time.Second

func (s *Server) SetDedupeCapacity(n int) {
	s.ddmu.Lock()
	s.ddcap = n
	s.dd = nil
	s.ddmu.Unlock()
}

func (s *Server) log(event string, fields map[string]any) {
	if s.Log != nil {
		s.Log(event, fields)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	c.SetReadLimit(types.WSReadLimit(s.MaxInlineBytes))
	defer c.Close(websocket.StatusNormalClosure, "")

	pingCtx, pingCancel := context.WithCancel(r.Context())
	defer pingCancel()

	var userID, deviceID string
	pingerStarted := false
	// Absolute deadline for completing authentication. Using a fixed deadline
	// (not a per-read timeout) means a client can't keep an unauthenticated
	// connection alive forever by dribbling junk messages.
	hto := s.helloTimeout
	if hto <= 0 {
		hto = defaultHelloTimeout
	}
	authDeadline := time.Now().Add(hto)

	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[string]map[string]*websocket.Conn)
	}
	s.mu.Unlock()

	for {
		var env types.Envelope
		// Until authenticated, bound the read so connections that upgrade but
		// never send a valid hello can't pile up (slowloris). Afterwards the
		// pinger handles liveness.
		readCtx := r.Context()
		var rcancel context.CancelFunc
		if userID == "" {
			readCtx, rcancel = context.WithDeadline(r.Context(), authDeadline)
		}
		err := wsjson.Read(readCtx, c, &env)
		if rcancel != nil {
			rcancel()
		}
		if err != nil {
			if userID != "" && deviceID != "" {
				s.removeConn(userID, deviceID, c)
			}
			return
		}

		switch env.Type {
		case "hello":
			if env.Hello == nil {
				continue
			}
			tok := env.Hello.Token
			uid := strings.TrimSpace(env.Hello.UserID)
			dev := strings.TrimSpace(env.Hello.DeviceID)
			if !deviceIDRe.MatchString(dev) {
				_ = c.Close(websocket.StatusPolicyViolation, "invalid device_id")
				return
			}
			if s.Auth != nil {
				got, ok := s.Auth(tok)
				if !ok {
					_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
					return
				}
				// The authenticated identity is authoritative; the client-provided
				// user_id is only a hint (the CLI sends the full token there).
				uid = got
			}
			if uid == "" {
				_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
				return
			}
			userID, deviceID = uid, dev
			s.addConn(userID, deviceID, c)
			if !pingerStarted {
				pingerStarted = true
				go s.pinger(pingCtx, c)
			}
			s.log("ws_hello", map[string]any{"user_id": userID, "device_id": deviceID})

		case "clip":
			if env.Clip == nil {
				continue
			}
			clip := env.Clip
			// 1) validar
			if !s.validateClip(clip) {
				atomic.AddInt64(&s.metrics.drops, 1)
				s.log("ws_drop_invalid", map[string]any{
					"user_id": userID, "device_id": deviceID, "msg_id": clip.MsgID,
				})
				continue
			}
			// 2) dedupe
			if s.isDup(userID, clip.MsgID) {
				atomic.AddInt64(&s.metrics.drops, 1)
				s.log("ws_drop_dup", map[string]any{
					"user_id": userID, "device_id": deviceID, "msg_id": clip.MsgID,
				})
				continue
			}
			// 3) rate limit
			if !s.allow(userID, deviceID) {
				atomic.AddInt64(&s.metrics.drops, 1)
				s.log("ws_drop_rate", map[string]any{
					"user_id": userID, "device_id": deviceID, "msg_id": clip.MsgID,
				})
				continue
			}
			atomic.AddInt64(&s.metrics.clips, 1)

			out := types.Envelope{
				Type: "clip",
				From: deviceID,
				Clip: clip,
			}
			s.broadcast(userID, deviceID, out)
			s.log("ws_clip", map[string]any{
				"user_id": userID, "device_id": deviceID, "msg_id": clip.MsgID,
				"mime": clip.Mime, "size": clip.Size, "has_data": len(clip.Data) > 0,
				"has_url": clip.UploadURL != "",
			})

		default:
			// ignore
		}
	}
}

func (s *Server) isDup(userID, msgID string) bool {
	if s.ddcap <= 0 || msgID == "" {
		return false
	}
	s.ddmu.Lock()
	if s.dd == nil {
		s.dd = make(map[string]*dedupeCache)
	}
	d := s.dd[userID]
	if d == nil {
		d = newDedupe(s.ddcap)
		s.dd[userID] = d
	}
	hit := d.ExistsOrAdd(msgID)
	s.ddmu.Unlock()
	return hit
}

func (s *Server) allow(userID, deviceID string) bool {
	if s.RateLimitPerSecond <= 0 {
		return true
	}
	key := userID + "|" + deviceID
	s.rlmu.Lock()
	if s.rl == nil {
		s.rl = make(map[string]*limiter)
	}
	lim := s.rl[key]
	if lim == nil {
		lim = newLimiter(s.RateLimitPerSecond)
		s.rl[key] = lim
	}
	s.rlmu.Unlock()
	return lim.allow()
}

// pinger keeps a client connection liveness-checked: a failed ping (dead or
// half-open client) closes the connection so the read loop ends and the conn is
// removed, instead of lingering and stalling every broadcast on a 1s timeout.
func (s *Server) pinger(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				_ = c.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func (s *Server) addConn(userID, deviceID string, c *websocket.Conn) {
	s.mu.Lock()
	if s.conns[userID] == nil {
		s.conns[userID] = make(map[string]*websocket.Conn)
	}
	old := s.conns[userID][deviceID]
	s.conns[userID][deviceID] = c
	if old == nil {
		atomic.AddInt64(&s.metrics.conns, 1)
	}
	s.mu.Unlock()
	// A device_id is single-connection: the newest wins. Close any prior
	// connection for this device so its read loop ends (its removeConn is a
	// no-op thanks to the identity check below). Done outside the lock.
	if old != nil && old != c {
		_ = old.Close(websocket.StatusPolicyViolation, "replaced by new connection")
	}
}

func (s *Server) removeConn(userID, deviceID string, c *websocket.Conn) {
	s.mu.Lock()
	removed := false
	if m := s.conns[userID]; m != nil {
		// Only remove if the stored connection is the one leaving; otherwise a
		// stale old connection would evict the live reconnected one.
		if cur, ok := m[deviceID]; ok && cur == c {
			delete(m, deviceID)
			atomic.AddInt64(&s.metrics.conns, -1)
			removed = true
		}
		if len(m) == 0 {
			delete(s.conns, userID)
		}
	}
	_, roomAlive := s.conns[userID]
	s.mu.Unlock()

	if !removed {
		return
	}
	// Free per-device/per-user state so these maps don't grow without bound as
	// devices churn. Only runs when the leaving conn was the live one.
	key := userID + "|" + deviceID
	s.rlmu.Lock()
	delete(s.rl, key)
	s.rlmu.Unlock()
	s.dropsMu.Lock()
	delete(s.dropsByDevice, key)
	s.dropsMu.Unlock()
	if !roomAlive {
		s.ddmu.Lock()
		delete(s.dd, userID)
		s.ddmu.Unlock()
	}
}

func (s *Server) broadcast(userID, fromDevice string, env types.Envelope) {
	buildTargets := func() [][2]interface{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		list := make([][2]interface{}, 0, 4)
		if peers := s.conns[userID]; peers != nil {
			for dev, c := range peers {
				if dev == fromDevice {
					continue
				}
				list = append(list, [2]interface{}{dev, c})
			}
		}
		return list
	}

	targets := buildTargets()
	if len(targets) == 0 {
		time.Sleep(50 * time.Millisecond)
		targets = buildTargets()
	}

	// Write to peers in parallel: a slow or stalled peer must not delay delivery
	// to the others (nor block the sender's read loop beyond one timeout).
	var wg sync.WaitGroup
	for _, pair := range targets {
		dev := pair[0].(string)
		c := pair[1].(*websocket.Conn)
		wg.Add(1)
		go func(dev string, c *websocket.Conn) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			if err := wsjson.Write(ctx, c, env); err != nil {
				// contar como drop por backpressure/error de escritura
				atomic.AddInt64(&s.metrics.drops, 1)
				s.incDeviceDrop(userID, dev)
				s.log("ws_drop_backpressure", map[string]any{
					"user_id": userID, "device_id": dev, "error": err.Error(),
				})
			}
		}(dev, c)
	}
	wg.Wait()
}

func (s *Server) incDeviceDrop(userID, deviceID string) {
	key := userID + "|" + deviceID
	s.dropsMu.Lock()
	if s.dropsByDevice == nil {
		s.dropsByDevice = make(map[string]int64, 8)
	}
	s.dropsByDevice[key]++
	s.dropsMu.Unlock()
}

func (s *Server) validateClip(c *types.Clip) bool {
	if c == nil {
		return false
	}
	if c.Mime == "" {
		c.Mime = "application/octet-stream"
	}
	if len(c.Data) > 0 {
		if len(c.Data) != c.Size {
			return false
		}
		if s.MaxInlineBytes > 0 && c.Size > s.MaxInlineBytes {
			return false
		}
		return true
	}
	if c.UploadURL == "" || c.Size <= 0 {
		return false
	}
	return true
}

func (s *Server) MetricsSnapshot() map[string]int64 {
	m := map[string]int64{
		"clips_total":   atomic.LoadInt64(&s.metrics.clips),
		"drops_total":   atomic.LoadInt64(&s.metrics.drops),
		"conns_current": atomic.LoadInt64(&s.metrics.conns),
	}
	// incluir drops por device de forma plana, para mantener tipo map[string]int64
	s.dropsMu.Lock()
	for k, v := range s.dropsByDevice {
		m["drops_device:"+k] = v
	}
	s.dropsMu.Unlock()
	return m
}

// Graceful shutdown
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	var list []*websocket.Conn
	for _, devs := range s.conns {
		for _, c := range devs {
			list = append(list, c)
		}
	}
	total := int64(len(list))
	s.conns = make(map[string]map[string]*websocket.Conn)
	atomic.AddInt64(&s.metrics.conns, -total)
	s.mu.Unlock()

	for _, c := range list {
		_ = c.Close(websocket.StatusNormalClosure, "server_shutdown")
	}
}
