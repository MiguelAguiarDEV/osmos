package ws

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clip-sync/server/internal/hub"
	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

/* -------- rate limit -------- */

type limiter struct {
	mu       sync.Mutex
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
	l.mu.Lock()
	defer l.mu.Unlock()
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

/* -------- conexión con cola de salida -------- */

// outQueueSize acota cuántos clips pendientes se guardan por device antes de
// empezar a descartar. Escribir directamente desde el goroutine lector deja
// que un cliente lento bloquee a todos los demás, así que cada conexión tiene
// su propio escritor.
const outQueueSize = 64

type conn struct {
	ws      *websocket.Conn
	out     chan []byte
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
	helloAt time.Time
}

func newConn(c *websocket.Conn) *conn {
	return &conn{
		ws:      c,
		out:     make(chan []byte, outQueueSize),
		done:    make(chan struct{}),
		helloAt: time.Now(),
	}
}

// enqueue devuelve false si la cola está llena (backpressure) o ya cerrada.
func (c *conn) enqueue(payload []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.out <- payload:
		return true
	default:
		return false
	}
}

func (c *conn) stop() {
	c.closeMu.Lock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	c.closeMu.Unlock()
}

// writeLoop serializa las escrituras de una conexión y mantiene el orden.
func (c *conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.out:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.ws.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				c.stop()
				return
			}
		}
	}
}

type Server struct {
	Hub                *hub.Hub
	Auth               func(token string) (string, bool)
	MaxInlineBytes     int
	RateLimitPerSecond int

	// logger: si es nil, no loggea
	Log func(event string, fields map[string]any)

	mu    sync.RWMutex
	conns map[string]map[string]*conn // userID -> deviceID -> conn

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

// readLimit acota el tamaño de un mensaje entrante. Un clip inline viaja
// como JSON con el payload en base64, así que necesita ~4/3 del tamaño
// original más el resto del sobre.
func (s *Server) readLimit() int64 {
	max := s.MaxInlineBytes
	if max <= 0 {
		max = types.MaxInlineBytes
	}
	return int64(max)*2 + 4096
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(s.readLimit())

	var userID, deviceID string
	var self *conn
	defer func() {
		if self != nil {
			self.stop()
		}
		if userID != "" && deviceID != "" {
			s.removeConn(userID, deviceID, self)
		}
	}()

	for {
		var env types.Envelope
		if err := wsjson.Read(r.Context(), c, &env); err != nil {
			return
		}

		switch env.Type {
		case "hello":
			if env.Hello == nil {
				continue
			}
			tok := env.Hello.Token
			uid := env.Hello.UserID
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
				// el userID sale siempre del token verificado: el campo
				// user_id del cliente es informativo y no se puede usar
				// para entrar en la sala de otro usuario.
				uid = got
			}
			if uid == "" {
				_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
				return
			}
			if self != nil && userID == uid && deviceID == dev {
				// hello repetido sobre la misma conexión: nada que hacer
				continue
			}
			if self != nil {
				self.stop()
				s.removeConn(userID, deviceID, self)
			}
			userID, deviceID = uid, dev
			self = newConn(c)
			s.addConn(uid, dev, self)
			go self.writeLoop()
			s.log("ws_hello", map[string]any{"user_id": uid, "device_id": dev})

		case "clip":
			if env.Clip == nil {
				continue
			}
			if self == nil {
				// clip antes del hello: sin sala a la que enviarlo
				atomic.AddInt64(&s.metrics.drops, 1)
				s.log("ws_drop_no_hello", map[string]any{"msg_id": env.Clip.MsgID})
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
			s.broadcast(userID, deviceID, self, out)
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
	s.ddmu.Lock()
	defer s.ddmu.Unlock()
	if s.ddcap <= 0 || msgID == "" {
		return false
	}
	if s.dd == nil {
		s.dd = make(map[string]*dedupeCache)
	}
	d := s.dd[userID]
	if d == nil {
		d = newDedupe(s.ddcap)
		s.dd[userID] = d
	}
	return d.ExistsOrAdd(msgID)
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

func (s *Server) addConn(userID, deviceID string, c *conn) {
	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[string]map[string]*conn)
	}
	if s.conns[userID] == nil {
		s.conns[userID] = make(map[string]*conn)
	}
	prev := s.conns[userID][deviceID]
	s.conns[userID][deviceID] = c
	if prev == nil {
		atomic.AddInt64(&s.metrics.conns, 1)
	}
	s.mu.Unlock()

	if prev != nil && prev != c {
		// mismo device_id reconectando: cerrar la sesión anterior.
		// StatusPolicyViolation (y no un cierre normal) para que el cliente
		// lo trate como error de configuración y NO reintente: si dos
		// procesos comparten device_id y ambos reconectan, se echan
		// mutuamente en bucle infinito.
		prev.stop()
		_ = prev.ws.Close(websocket.StatusPolicyViolation, "duplicate_device_id")
	}
}

// removeConn sólo desregistra si la conexión guardada sigue siendo `c`.
// Sin esta comprobación, el cierre tardío de una conexión vieja borraba del
// registro a la conexión nueva del mismo device tras reconectar.
func (s *Server) removeConn(userID, deviceID string, c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.conns[userID]
	if m == nil {
		return
	}
	cur, ok := m[deviceID]
	if !ok {
		return
	}
	if c != nil && cur != c {
		return
	}
	delete(m, deviceID)
	atomic.AddInt64(&s.metrics.conns, -1)
	if len(m) == 0 {
		delete(s.conns, userID)
	}
}

func (s *Server) peers(userID, fromDevice string) []*conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	room := s.conns[userID]
	if len(room) == 0 {
		return nil
	}
	list := make([]*conn, 0, len(room))
	for dev, c := range room {
		if dev == fromDevice {
			continue
		}
		list = append(list, c)
	}
	return list
}

func (s *Server) deviceOf(userID string, target *conn) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for dev, c := range s.conns[userID] {
		if c == target {
			return dev
		}
	}
	return ""
}

// joinGrace es la ventana en la que un emisor recién conectado espera a que
// aparezca algún peer. Cubre la carrera de "todos los devices arrancan a la
// vez"; fuera de esa ventana el envío no bloquea nunca.
const joinGrace = 300 * time.Millisecond

func (s *Server) broadcast(userID, fromDevice string, from *conn, env types.Envelope) {
	payload, err := marshalEnvelope(env)
	if err != nil {
		atomic.AddInt64(&s.metrics.drops, 1)
		return
	}

	targets := s.peers(userID, fromDevice)
	if len(targets) == 0 && from != nil && time.Since(from.helloAt) < joinGrace {
		deadline := time.Now().Add(50 * time.Millisecond)
		for len(targets) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
			targets = s.peers(userID, fromDevice)
		}
	}

	for _, c := range targets {
		if c.enqueue(payload) {
			continue
		}
		atomic.AddInt64(&s.metrics.drops, 1)
		dev := s.deviceOf(userID, c)
		s.incDeviceDrop(userID, dev)
		s.log("ws_drop_backpressure", map[string]any{
			"user_id": userID, "device_id": dev, "error": "out queue full",
		})
	}
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
	var list []*conn
	for _, devs := range s.conns {
		for _, c := range devs {
			list = append(list, c)
		}
	}
	total := int64(len(list))
	s.conns = make(map[string]map[string]*conn)
	atomic.AddInt64(&s.metrics.conns, -total)
	s.mu.Unlock()

	for _, c := range list {
		c.stop()
		_ = c.ws.Close(websocket.StatusNormalClosure, "server_shutdown")
	}
}
