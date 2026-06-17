package httpapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRemovesOldBlobs(t *testing.T) {
	dir := t.TempDir()
	s := &UploadServer{Dir: dir, TTL: time.Hour}

	old := filepath.Join(dir, "abcdef0123456789abcdef0123456789") // valid 32-hex id
	fresh := filepath.Join(dir, "0123456789abcdef0123456789abcdef")
	tmp := filepath.Join(dir, ".upload-stale")
	keep := filepath.Join(dir, "notes.txt") // not managed by the server

	for _, f := range []string{old, fresh, tmp, keep} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	for _, f := range []string{old, tmp} {
		if err := os.Chtimes(f, twoHoursAgo, twoHoursAgo); err != nil {
			t.Fatal(err)
		}
	}

	if n := s.cleanup(time.Now()); n != 2 {
		t.Fatalf("expected 2 removals, got %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old blob should be deleted")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("stale temp file should be deleted")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh blob should be kept: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unmanaged file should be kept: %v", err)
	}
}

func TestCleanupDisabledWhenNoTTL(t *testing.T) {
	dir := t.TempDir()
	s := &UploadServer{Dir: dir} // TTL == 0
	f := filepath.Join(dir, "abcdef0123456789abcdef0123456789")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(f, old, old)
	if n := s.cleanup(time.Now()); n != 0 {
		t.Fatalf("TTL=0 must disable cleanup, got %d removals", n)
	}
	if _, err := os.Stat(f); err != nil {
		t.Errorf("file should remain when TTL disabled")
	}
}
