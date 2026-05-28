package tests

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clip-sync/server/internal/app"
	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Reconnecting with the same device_id must not orphan the new connection when
// the old one later disconnects. Reproduces the removeConn identity bug.
func TestReconnectSameDeviceKeepsReceiving(t *testing.T) {
	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadLimit(types.WSReadLimit(types.MaxInlineBytes))
		return c
	}
	hello := func(c *websocket.Conn, dev string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wsjson.Write(ctx, c, types.Envelope{Type: "hello", Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: dev}})
	}

	cA1 := dial()
	hello(cA1, "A")
	time.Sleep(100 * time.Millisecond)

	cA2 := dial() // same device id "A" reconnects
	defer cA2.Close(websocket.StatusInternalError, "")
	hello(cA2, "A")
	time.Sleep(100 * time.Millisecond)

	// Old connection drops (what happens on a real reconnect).
	_ = cA1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(150 * time.Millisecond)

	cB := dial()
	defer cB.Close(websocket.StatusInternalError, "")
	hello(cB, "B")
	time.Sleep(100 * time.Millisecond)

	payload := []byte("after-reconnect")
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	if err := wsjson.Write(wctx, cB, types.Envelope{Type: "clip", Clip: &types.Clip{MsgID: "m1", Mime: "text/plain", Size: len(payload), Data: payload}}); err != nil {
		t.Fatal(err)
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	var got types.Envelope
	if err := wsjson.Read(rctx, cA2, &got); err != nil {
		t.Fatalf("reconnected device A did not receive broadcast (orphaned): %v", err)
	}
	if got.Clip == nil || string(got.Clip.Data) != "after-reconnect" {
		t.Fatalf("unexpected: %+v", got.Clip)
	}
}
