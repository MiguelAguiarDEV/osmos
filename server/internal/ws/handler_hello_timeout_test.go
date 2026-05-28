package ws

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// A connection that upgrades but never sends a valid hello must be closed once
// helloTimeout elapses, so unauthenticated connections can't pile up.
func TestHelloTimeoutClosesIdleConn(t *testing.T) {
	s := &Server{
		Auth:         func(tok string) (string, bool) { return tok, tok != "" },
		helloTimeout: 200 * time.Millisecond,
	}
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

// Dribbling junk (non-hello) messages must not keep an unauthenticated
// connection alive past the absolute auth deadline.
func TestHelloDeadlineIgnoresJunk(t *testing.T) {
	s := &Server{
		Auth:         func(tok string) (string, bool) { return tok, tok != "" },
		helloTimeout: 300 * time.Millisecond,
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(websocket.StatusInternalError, "")

	start := time.Now()
	closed := make(chan struct{})
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				close(closed)
				return
			}
		}
	}()
	// Dribble junk every 100ms (< helloTimeout) for a while.
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-closed:
				return
			case <-t.C:
				_ = wsjson.Write(ctx, c, types.Envelope{Type: "noise"})
			}
		}
	}()

	select {
	case <-closed:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("junk kept the connection alive too long: %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection was kept alive by junk past the auth deadline")
	}
}
