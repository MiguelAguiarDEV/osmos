package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"clip-sync/server/internal/app"
	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hammer the hub with many devices connecting, sending, reconnecting (same
// device_id) and disconnecting concurrently. Run with -race. Afterwards the
// connection count must settle back to zero (catches lifecycle/metric drift).
func TestStressConcurrentChurn(t *testing.T) {
	srv := httptest.NewServer(app.WithHTTPLogging(app.NewMux()))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	httpBase := srv.URL

	const devices = 20
	var wg sync.WaitGroup
	for i := 0; i < devices; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dev := fmt.Sprintf("D%d", id)
			for round := 0; round < 3; round++ { // reconnect a few times with same device_id
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				c, _, err := websocket.Dial(ctx, wsURL, nil)
				if err != nil {
					cancel()
					return
				}
				c.SetReadLimit(types.WSReadLimit(types.MaxInlineBytes))
				_ = wsjson.Write(ctx, c, types.Envelope{Type: "hello", Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: dev}})
				for n := 0; n < 5; n++ {
					payload := []byte(fmt.Sprintf("d%d-r%d-n%d", id, round, n))
					_ = wsjson.Write(ctx, c, types.Envelope{Type: "clip", Clip: &types.Clip{MsgID: fmt.Sprintf("%s-%d-%d", dev, round, n), Mime: "text/plain", Size: len(payload), Data: payload}})
				}
				// drain anything pending briefly, then drop the connection
				drainCtx, dcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				var env types.Envelope
				for {
					if err := wsjson.Read(drainCtx, c, &env); err != nil {
						break
					}
				}
				dcancel()
				_ = c.Close(websocket.StatusNormalClosure, "")
				cancel()
			}
		}(i)
	}
	wg.Wait()

	// Give the server a moment to process all the read-loop teardowns.
	deadline := time.Now().Add(3 * time.Second)
	var cur int64
	for time.Now().Before(deadline) {
		cur = currentConns(t, httpBase)
		if cur == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cur != 0 {
		t.Fatalf("conns_current did not settle to 0 after churn: got %d", cur)
	}
}

func currentConns(t *testing.T, httpBase string) int64 {
	t.Helper()
	resp, err := http.Get(httpBase + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]int64
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m["conns_current"]
}
