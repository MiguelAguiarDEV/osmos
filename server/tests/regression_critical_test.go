package tests

import (
	"bytes"
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

// dialRaw connects and raises the read limit the same way the real client does,
// so the test can receive maximally-sized inline clips.
func dialRaw(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(types.WSReadLimit(types.MaxInlineBytes))
	return c
}

// Regression for the 32 KiB websocket read-limit bug: an inline clip up to
// MaxInlineBytes must be delivered. Before SetReadLimit was added, anything
// whose base64 envelope exceeded 32768 bytes (~24 KiB of raw data) was dropped.
func TestWSDeliversMaxInlineClip(t *testing.T) {
	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cA := dialRaw(t, ctx, wsURL)
	defer cA.Close(websocket.StatusInternalError, "")
	cB := dialRaw(t, ctx, wsURL)
	defer cB.Close(websocket.StatusInternalError, "")

	_ = wsjson.Write(ctx, cA, types.Envelope{Type: "hello", Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: "A"}})
	_ = wsjson.Write(ctx, cB, types.Envelope{Type: "hello", Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: "B"}})
	time.Sleep(100 * time.Millisecond)

	payload := bytes.Repeat([]byte("x"), types.MaxInlineBytes)
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "big", Mime: "text/plain", Size: len(payload), Data: payload},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	var got types.Envelope
	if err := wsjson.Read(rctx, cB, &got); err != nil {
		t.Fatalf("did not receive max inline clip: %v", err)
	}
	if got.Clip == nil || len(got.Clip.Data) != types.MaxInlineBytes {
		t.Fatalf("payload truncated: got %d bytes", len(got.Clip.Data))
	}
}

// Regression for HMAC auth via the CLI: the CLI sends user_id == token (the full
// "user:exp:mac" string). The server must authenticate on the token and route by
// the authenticated user, not reject because user_id != bare user.
func TestWSAuthHMAC_AcceptsClientStyleHello(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tok := makeHMACToken("u1", time.Now().Add(time.Minute), "s3cr3t")

	cA := dialRaw(t, ctx, wsURL)
	defer cA.Close(websocket.StatusInternalError, "")
	cB := dialRaw(t, ctx, wsURL)
	defer cB.Close(websocket.StatusInternalError, "")

	// user_id set to the full token, exactly how clients/cli/main.go builds hello.
	_ = wsjson.Write(ctx, cA, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: tok, DeviceID: "A"}})
	_ = wsjson.Write(ctx, cB, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: tok, DeviceID: "B"}})
	time.Sleep(100 * time.Millisecond)

	payload := []byte("via-cli-hmac")
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "m1", Mime: "text/plain", Size: len(payload), Data: payload},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	var got types.Envelope
	if err := wsjson.Read(rctx, cB, &got); err != nil {
		t.Fatalf("client-style HMAC hello did not authenticate / route: %v", err)
	}
	if got.Clip == nil || string(got.Clip.Data) != "via-cli-hmac" {
		t.Fatalf("unexpected clip: %+v", got.Clip)
	}
}
