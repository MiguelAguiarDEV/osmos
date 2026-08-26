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

func TestWSRejectsInvalidDeviceID(t *testing.T) {
	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// IDs inválidos que deben provocar cierre por policy violation
	bads := []string{"", " ", "A!", strings.Repeat("a", 65)}
	for _, dev := range bads {
		// reabrir por cada caso para aislar
		c2, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}

		// hello con device inválido
		_ = wsjson.Write(ctx, c2, types.Envelope{Type: "hello", Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: dev}})

		// siguiente write debería fallar rápido porque el server cerró la conexión
		short, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err = wsjson.Write(short, c2, types.Envelope{Type: "clip", Clip: &types.Clip{Mime: "text/plain", Size: 1, Data: []byte("x")}})
		cancel2()
		if err == nil {
			// si no falló el write, leer debe fallar
			shortR, cancelR := context.WithTimeout(context.Background(), 100*time.Millisecond)
			var dummy types.Envelope
			if err := wsjson.Read(shortR, c2, &dummy); err == nil {
				cancelR()
				_ = c2.Close(websocket.StatusNormalClosure, "")
				t.Fatalf("device_id %q no fue rechazado", dev)
			}
			cancelR()
		}
		_ = c2.Close(websocket.StatusNormalClosure, "")
	}
}
