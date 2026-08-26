package tests

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"clip-sync/server/internal/app"
	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func makeHMACToken(uid string, exp time.Time, secret string) string {
	payload := uid + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sum := mac.Sum(nil)
	return uid + ":" + strconv.FormatInt(exp.Unix(), 10) + ":" + hex.EncodeToString(sum)
}

func TestWSAuthHMAC_AcceptsValidToken(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cA, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cA.Close(websocket.StatusNormalClosure, "")

	cB, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cB.Close(websocket.StatusNormalClosure, "")

	tok := makeHMACToken("u1", time.Now().Add(1*time.Minute), "s3cr3t")
	_ = wsjson.Write(ctx, cA, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: "u1", DeviceID: "A"}})
	_ = wsjson.Write(ctx, cB, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: "u1", DeviceID: "B"}})

	// enviar un clip pequeño y verificar recepción
	payload := []byte("ok")
	if err := wsjson.Write(ctx, cA, types.Envelope{Type: "clip", Clip: &types.Clip{Mime: "text/plain", Size: len(payload), Data: payload}}); err != nil {
		t.Fatal(err)
	}
	var got types.Envelope
	if err := wsjson.Read(ctx, cB, &got); err != nil || got.Type != "clip" {
		t.Fatalf("esperaba clip; got=%+v err=%v", got, err)
	}
}

func TestWSAuthHMAC_RejectsBadMAC(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cA, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cA.Close(websocket.StatusNormalClosure, "")

	// token con mac alterada
	tok := makeHMACToken("u1", time.Now().Add(1*time.Minute), "other")
	_ = wsjson.Write(ctx, cA, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: "u1", DeviceID: "A"}})

	// siguiente write debería fallar porque el server cierra por unauthorized
	payload := []byte("x")
	err = wsjson.Write(ctx, cA, types.Envelope{Type: "clip", Clip: &types.Clip{Mime: "text/plain", Size: len(payload), Data: payload}})
	if err == nil {
		// si no falló al escribir, leer debe fallar rápidamente
		short, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel2()
		var dummy types.Envelope
		if err := wsjson.Read(short, cA, &dummy); err == nil {
			t.Fatal("esperaba cierre por unauthorized")
		}
	}
}

func TestWSAuthHMAC_RejectsExpired(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cA, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cA.Close(websocket.StatusNormalClosure, "")

	// token expirado
	tok := makeHMACToken("u1", time.Now().Add(-1*time.Minute), "s3cr3t")
	_ = wsjson.Write(ctx, cA, types.Envelope{Type: "hello", Hello: &types.Hello{Token: tok, UserID: "u1", DeviceID: "A"}})

	// write siguiente debe fallar o lectura inmediata debe fallar
	payload := []byte("x")
	err = wsjson.Write(ctx, cA, types.Envelope{Type: "clip", Clip: &types.Clip{Mime: "text/plain", Size: len(payload), Data: payload}})
	if err == nil {
		short, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel2()
		var dummy types.Envelope
		if err := wsjson.Read(short, cA, &dummy); err == nil {
			t.Fatal("esperaba cierre por token expirado")
		}
	}
}
