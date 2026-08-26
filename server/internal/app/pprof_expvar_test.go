package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDebugEndpoints_DisabledByDefault(t *testing.T) {
	srv := httptest.NewServer(NewMux())
	defer srv.Close()

	for _, path := range []string{"/debug/pprof/", "/debug/vars"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestDebugEndpoints_Enabled(t *testing.T) {
	t.Setenv("CLIPSYNC_PPROF", "1")
	t.Setenv("CLIPSYNC_EXPVAR", "1")

	srv := httptest.NewServer(NewMux())
	defer srv.Close()

	// pprof index
	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pprof index status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// expvar should return JSON
	resp2, err := http.Get(srv.URL + "/debug/vars")
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expvar status=%d", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct == "" {
		t.Fatalf("expvar missing content-type")
	}
	resp2.Body.Close()
}
