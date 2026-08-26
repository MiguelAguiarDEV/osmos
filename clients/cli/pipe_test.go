package main

import (
	"bytes"
	"os"
	"testing"
)

func TestReadToBufferOrFile_Small(t *testing.T) {
	src := bytes.Repeat([]byte("A"), 1024)
	data, tmp, n, mt, err := readToBufferOrFile(bytes.NewReader(src), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if tmp != "" {
		t.Fatalf("unexpected tmp=%s", tmp)
	}
	if n != len(src) || len(data) != len(src) {
		t.Fatalf("size mismatch: n=%d len=%d", n, len(data))
	}
	if mt != "text/plain" {
		t.Fatalf("mime=%s", mt)
	}
}

func TestReadToBufferOrFile_LargeSpillsToFile(t *testing.T) {
	src := bytes.Repeat([]byte("B"), (64<<10)+1024)
	data, tmp, n, mt, err := readToBufferOrFile(bytes.NewReader(src), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatalf("expected nil data when spilling to file")
	}
	if tmp == "" {
		t.Fatalf("expected tmp file path")
	}
	defer os.Remove(tmp)
	if n != len(src) {
		t.Fatalf("size mismatch: n=%d want=%d", n, len(src))
	}
	if mt != "text/plain" {
		t.Fatalf("mime=%s", mt)
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() != int64(n) {
		t.Fatalf("tmp size: %v %v", fi, err)
	}
}
