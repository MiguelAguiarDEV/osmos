package tests

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"clip-sync/server/internal/app"
)

// End-to-end test of the real CLI in sync mode against a real server, using
// fake clipboard backends (wl-copy/wl-paste backed by per-device files).
// Guards the echo-suppression and propagation logic that unit tests can't reach.
func TestCLISyncEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses bash clipboard stubs")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	tmp := t.TempDir()

	// Build the CLI binary.
	cli := filepath.Join(tmp, "cli")
	build := exec.Command("go", "build", "-o", cli, ".")
	build.Dir = "../../clients/cli"
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build cli (%v): %s", err, out)
	}

	// Fake clipboard backends keyed by $CLIPBOARD_FILE.
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(binDir, "wl-copy"), "#!/usr/bin/env bash\ncat > \"$CLIPBOARD_FILE\"\n")
	writeScript(t, filepath.Join(binDir, "wl-paste"), "#!/usr/bin/env bash\ncat \"$CLIPBOARD_FILE\" 2>/dev/null || true\n")

	srv := httptest.NewServer(app.NewMux())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	clipL := filepath.Join(tmp, "clipL")
	clipW := filepath.Join(tmp, "clipW")
	_ = os.WriteFile(clipL, nil, 0o644)
	_ = os.WriteFile(clipW, nil, 0o644)

	runDev := func(device, clipFile string) *exec.Cmd {
		c := exec.Command(cli, "--mode", "sync", "--addr", wsURL, "--token", "u1", "--device", device, "--poll-ms", "150")
		c.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"CLIPBOARD_FILE="+clipFile,
		)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	cmdL := runDev("L1", clipL)
	defer func() { _ = cmdL.Process.Kill() }()
	cmdW := runDev("W1", clipW)
	defer func() { _ = cmdW.Process.Kill() }()

	time.Sleep(700 * time.Millisecond) // let both connect

	if err := os.WriteFile(clipW, []byte("hello from W"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Poll clipL for propagation.
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(clipL)
		got = strings.TrimRight(string(b), "\r\n")
		if got == "hello from W" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != "hello from W" {
		t.Fatalf("clip did not propagate W->L: clipL=%q", got)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
