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

func dialHello(t *testing.T, ctx context.Context, wsURL, token, user, device string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, c, types.Envelope{
		Type:  "hello",
		Hello: &types.Hello{Token: token, UserID: user, DeviceID: device},
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return c
}

// TestWSInlineClipAtMaxSize: un clip inline del tamaño máximo permitido
// (64 KiB) debe entregarse. Antes fallaba porque el límite de lectura por
// defecto de coder/websocket es 32 KiB y la conexión se cerraba.
func TestWSInlineClipAtMaxSize(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cA := dialHello(t, ctx, wsURL, "u1", "u1", "A")
	defer cA.Close(websocket.StatusNormalClosure, "")
	cB := dialHello(t, ctx, wsURL, "u1", "u1", "B")
	defer cB.Close(websocket.StatusNormalClosure, "")

	payload := []byte(strings.Repeat("x", types.MaxInlineBytes))
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "big", Mime: "text/plain", Size: len(payload), Data: payload},
	}); err != nil {
		t.Fatalf("write big clip: %v", err)
	}

	var got types.Envelope
	if err := wsjson.Read(ctx, cB, &got); err != nil {
		t.Fatalf("read big clip: %v", err)
	}
	if got.Clip == nil || len(got.Clip.Data) != types.MaxInlineBytes {
		t.Fatalf("clip incompleto: %+v", got.Clip)
	}
}

// TestWSAuthHMACUserIDFromToken: el CLI manda UserID == token completo.
// El server debe derivar el userID del token autenticado, no confiar en
// el campo user_id del cliente.
func TestWSAuthHMACUserIDFromToken(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tok := makeHMACToken("u1", time.Now().Add(time.Minute), "s3cr3t")
	// tal cual lo manda clients/cli: UserID = token
	cA := dialHello(t, ctx, wsURL, tok, tok, "A")
	defer cA.Close(websocket.StatusNormalClosure, "")
	cB := dialHello(t, ctx, wsURL, tok, tok, "B")
	defer cB.Close(websocket.StatusNormalClosure, "")

	payload := []byte("ok")
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "m1", Mime: "text/plain", Size: len(payload), Data: payload},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got types.Envelope
	if err := wsjson.Read(ctx, cB, &got); err != nil {
		t.Fatalf("B no recibió el clip: %v", err)
	}
	if string(got.Clip.Data) != "ok" {
		t.Fatalf("data=%q", got.Clip.Data)
	}
}

// TestWSHMACIgnoresSpoofedUserID: un cliente no puede meterse en la sala de
// otro usuario declarando un user_id distinto al del token.
func TestWSHMACIgnoresSpoofedUserID(t *testing.T) {
	t.Setenv("CLIPSYNC_HMAC_SECRET", "s3cr3t")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tokVictim := makeHMACToken("victim", time.Now().Add(time.Minute), "s3cr3t")
	tokAttacker := makeHMACToken("attacker", time.Now().Add(time.Minute), "s3cr3t")

	victim := dialHello(t, ctx, wsURL, tokVictim, "victim", "V")
	defer victim.Close(websocket.StatusNormalClosure, "")
	// el atacante declara user_id=victim pero su token dice attacker
	attacker := dialHello(t, ctx, wsURL, tokAttacker, "victim", "X")
	defer attacker.Close(websocket.StatusNormalClosure, "")

	payload := []byte("secreto")
	_ = wsjson.Write(ctx, victim, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "m1", Mime: "text/plain", Size: len(payload), Data: payload},
	})

	short, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	var got types.Envelope
	if err := wsjson.Read(short, attacker, &got); err == nil {
		t.Fatalf("el atacante recibió el clip de la víctima: %+v", got.Clip)
	}
}

// TestWSReconnectSameDeviceID: al reconectar con el mismo device_id, la
// conexión nueva debe quedar registrada; el cierre de la vieja no puede
// desregistrar a la nueva.
func TestWSReconnectSameDeviceID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cB := dialHello(t, ctx, wsURL, "u1", "u1", "B")
	defer cB.Close(websocket.StatusNormalClosure, "")

	old := dialHello(t, ctx, wsURL, "u1", "u1", "A")
	fresh := dialHello(t, ctx, wsURL, "u1", "u1", "A")
	defer fresh.Close(websocket.StatusNormalClosure, "")

	// la vieja se cae (simula un corte de red seguido de reconexión)
	_ = old.Close(websocket.StatusAbnormalClosure, "")
	time.Sleep(200 * time.Millisecond)

	payload := []byte("post-reconnect")
	if err := wsjson.Write(ctx, cB, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{MsgID: "m1", Mime: "text/plain", Size: len(payload), Data: payload},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	read, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	var got types.Envelope
	if err := wsjson.Read(read, fresh, &got); err != nil {
		t.Fatalf("la conexión reconectada no recibió el clip: %v", err)
	}
	if string(got.Clip.Data) != "post-reconnect" {
		t.Fatalf("data=%q", got.Clip.Data)
	}
}

// TestWSSingleDeviceNoStall: con un solo device conectado el envío no debe
// bloquear el bucle de lectura. Antes había un time.Sleep(50ms) por clip.
func TestWSSingleDeviceNoStall(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cA := dialHello(t, ctx, wsURL, "u-solo", "u-solo", "A")
	defer cA.Close(websocket.StatusNormalClosure, "")

	// warm-up: asegura que el hello ya se procesó
	time.Sleep(100 * time.Millisecond)

	const n = 20
	start := time.Now()
	for i := 0; i < n; i++ {
		payload := []byte{byte('a' + i%26)}
		if err := wsjson.Write(ctx, cA, types.Envelope{
			Type: "clip",
			Clip: &types.Clip{MsgID: string(rune('A' + i)), Mime: "text/plain", Size: 1, Data: payload},
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// el server debe seguir vivo y responder rápido
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type:  "hello",
		Hello: &types.Hello{Token: "u-solo", UserID: "u-solo", DeviceID: "A"},
	}); err != nil {
		t.Fatalf("ping hello: %v", err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("%d clips con un solo device tardaron %v (esperado <500ms)", n, el)
	}
}

// TestWSDuplicateDeviceIDClosesOldWithPolicy: reconectar con un device_id ya
// en uso expulsa a la sesión anterior con StatusPolicyViolation, para que el
// cliente lo trate como error de configuración y no reintente en bucle.
func TestWSDuplicateDeviceIDClosesOldWithPolicy(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	old := dialHello(t, ctx, wsURL, "u-dup", "u-dup", "SAME")
	defer old.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)
	fresh := dialHello(t, ctx, wsURL, "u-dup", "u-dup", "SAME")
	defer fresh.Close(websocket.StatusNormalClosure, "")

	read, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	var env types.Envelope
	err := wsjson.Read(read, old, &env)
	if err == nil {
		t.Fatal("la sesión vieja debería haber sido cerrada")
	}
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("esperaba StatusPolicyViolation, got %v (%v)", websocket.CloseStatus(err), err)
	}
}
