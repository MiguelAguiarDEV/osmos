package ws

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A connection that upgrades but never sends a valid hello must be closed once
// helloTimeout elapses, so unauthenticated connections can't pile up.
func TestHelloTimeoutClosesIdleConn(t *testing.T) {
	old := helloTimeout
	helloTimeout = 200 * time.Millisecond
	defer func() { helloTimeout = old }()

	s := &Server{Auth: func(tok string) (string, bool) { return tok, tok != "" }}
	srv := httptest.NewServer(s)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(websocket.StatusInternalError, "")

	// Send no hello; the server must close us shortly after helloTimeout.
	start := time.Now()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("expected the idle (no-hello) connection to be closed")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle connection took too long to close: %v", elapsed)
	}
}
