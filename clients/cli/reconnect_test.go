package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestNormalizeClipboard(t *testing.T) {
	cases := map[string]string{
		"hola":         "hola",
		"hola\n":       "hola",
		"hola\r\n":     "hola",
		"a\r\nb\r\n":   "a\nb",
		"a\nb\n\n\n":   "a\nb",
		"":             "",
		"\r\n":         "",
		"  espacios  ": "  espacios  ",
	}
	for in, want := range cases {
		if got := normalizeClipboard(in); got != want {
			t.Errorf("normalizeClipboard(%q) = %q, want %q", in, got, want)
		}
	}
}

// Un clip inline del tamaño máximo viaja en base64 dentro del JSON, así que el
// límite de lectura del cliente debe dejar sitio para esa expansión.
func TestClientReadLimitCoversMaxInline(t *testing.T) {
	// base64 del payload + sobre JSON
	min := int64(types.MaxInlineBytes)*4/3 + 256
	if clientReadLimit < min {
		t.Fatalf("clientReadLimit=%d es menor que el mínimo necesario %d", clientReadLimit, min)
	}
}

// TestSuperviseReconnects: si el servidor tira la conexión, supervise debe
// volver a conectar en vez de que el proceso muera.
func TestSuperviseReconnects(t *testing.T) {
	var accepted int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		n := atomic.AddInt64(&accepted, 1)
		// consumir el hello y cerrar de golpe para forzar la reconexión
		var env types.Envelope
		_ = wsjson.Read(r.Context(), c, &env)
		if n < 3 {
			_ = c.Close(websocket.StatusAbnormalClosure, "boom")
			return
		}
		// la tercera conexión se mantiene abierta
		<-r.Context().Done()
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = supervise(ctx, wsURL, "u1", "A", "test", false, func(c context.Context, w *websocket.Conn) error {
			var env types.Envelope
			return wsjson.Read(c, w, &env)
		})
		close(done)
	}()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&accepted) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&accepted); got < 3 {
		t.Fatalf("supervise sólo conectó %d veces; esperaba >=3 (no reconecta)", got)
	}

	// cancelar el contexto debe hacer que supervise termine
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("supervise no salió al cancelar el contexto")
	}
}

func TestHTTPBaseFromWSStripsPath(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:8080/ws":    "http://127.0.0.1:8080",
		"wss://clip.example.com/ws": "https://clip.example.com",
		"ws://host:8080":            "http://host:8080",
		"wss://host/a/b/ws?x=1":     "https://host",
		"host:8080/ws":              "http://host:8080",
		"http://host:8080/ws":       "http://host:8080",
		"ws://[::1]:8080/ws":        "http://[::1]:8080",
	}
	for in, want := range cases {
		if got := httpBaseFromWS(in); got != want {
			t.Errorf("httpBaseFromWS(%q) = %q, want %q", in, got, want)
		}
	}
}

// Un cierre por política (token inválido, device_id duplicado) no debe
// reintentarse: si se reintenta, dos procesos con el mismo device_id se
// expulsan mutuamente en bucle infinito.
func TestTerminalCloseError(t *testing.T) {
	reason, terminal := terminalCloseError(websocket.CloseError{
		Code: websocket.StatusPolicyViolation, Reason: "duplicate_device_id",
	})
	if !terminal || reason != "duplicate_device_id" {
		t.Fatalf("policy violation debería ser terminal; got reason=%q terminal=%v", reason, terminal)
	}
	if _, terminal := terminalCloseError(websocket.CloseError{Code: websocket.StatusAbnormalClosure}); terminal {
		t.Fatal("un corte de red no debe ser terminal")
	}
	if _, terminal := terminalCloseError(context.Canceled); terminal {
		t.Fatal("context.Canceled no debe ser terminal")
	}
}
