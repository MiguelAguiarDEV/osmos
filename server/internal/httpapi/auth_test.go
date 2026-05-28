package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadDownloadAuth(t *testing.T) {
	dir := t.TempDir()
	auth := func(tok string) (string, bool) { return tok, tok == "good" }
	s := &UploadServer{Dir: dir, MaxBytes: 1 << 20, Auth: auth}

	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("hello")))
		req.Header.Set("Content-Type", "text/plain")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		s.Upload(rr, req)
		return rr
	}

	if rr := post(""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("upload without token: got %d, want 401", rr.Code)
	}
	if rr := post("bad"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("upload with bad token: got %d, want 401", rr.Code)
	}
	rr := post("good")
	if rr.Code != http.StatusOK {
		t.Fatalf("upload with good token: got %d, want 200", rr.Code)
	}
	var up uploadResp
	if err := json.NewDecoder(rr.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(up.UploadURL, "/d/")

	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, up.UploadURL, nil)
		req.SetPathValue("id", id)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		s.Download(rr, req)
		return rr
	}

	if rr := get(""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("download without token: got %d, want 401", rr.Code)
	}
	if rr := get("good"); rr.Code != http.StatusOK {
		t.Fatalf("download with good token: got %d, want 200", rr.Code)
	} else if rr.Body.String() != "hello" {
		t.Fatalf("download body = %q", rr.Body.String())
	}
}

// With no Auth configured, requests pass (back-compat).
func TestUploadNoAuthConfigured(t *testing.T) {
	s := &UploadServer{Dir: t.TempDir(), MaxBytes: 1 << 20}
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("x")))
	rr := httptest.NewRecorder()
	s.Upload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("no-auth upload: got %d, want 200", rr.Code)
	}
}
