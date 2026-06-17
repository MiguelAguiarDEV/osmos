package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clip-sync/server/internal/app"
	"clip-sync/server/pkg/types"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestUploadAndSignal(t *testing.T) {
	t.Parallel()

	// Server en memoria
	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()

	// 1) Upload ~100 KB
	body := bytes.Repeat([]byte("X"), 100_000)
	upReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload", bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/octet-stream")
	upReq.Header.Set("Authorization", "Bearer u1")
	resp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, string(b))
	}
	var up struct {
		UploadURL string `json:"upload_url"`
		Size      int    `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	if up.UploadURL == "" || up.Size != len(body) {
		t.Fatalf("upload resp invalida: %+v", up)
	}

	// 2) WS A y B con hello
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type:  "hello",
		Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, cB, types.Envelope{
		Type:  "hello",
		Hello: &types.Hello{Token: "u1", UserID: "u1", DeviceID: "B"},
	}); err != nil {
		t.Fatal(err)
	}

	// 3) A envía clip con URL
	if err := wsjson.Write(ctx, cA, types.Envelope{
		Type: "clip",
		Clip: &types.Clip{
			MsgID:     "m2",
			Mime:      "application/octet-stream",
			Size:      len(body),
			UploadURL: up.UploadURL,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4) B recibe y descarga
	var got types.Envelope
	if err := wsjson.Read(ctx, cB, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "clip" || got.Clip == nil || got.Clip.UploadURL == "" {
		t.Fatalf("esperaba clip con upload_url, got=%+v", got)
	}
	if got.Clip.Size != len(body) {
		t.Fatalf("size esperado=%d got=%d", len(body), got.Clip.Size)
	}

	dlReq, _ := http.NewRequest(http.MethodGet, srv.URL+got.Clip.UploadURL, nil)
	dlReq.Header.Set("Authorization", "Bearer u1")
	dl, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	n, _ := io.Copy(io.Discard, dl.Body)
	if int(n) != len(body) {
		t.Fatalf("download size esperado=%d got=%d", len(body), n)
	}
}
