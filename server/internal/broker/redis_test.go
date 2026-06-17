package broker

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

type captured struct {
	user, from string
	payload    []byte
}

// A clip published on one instance must reach another instance subscribed to
// the same channel (the basis for horizontal scale-out).
func TestRedisCrossInstanceDelivery(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	url := "redis://" + mr.Addr()

	b1, err := NewRedis(url, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer b1.Close()
	b2, err := NewRedis(url, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	recv := make(chan captured, 4)
	b2.SetHandler(func(u, f string, p []byte) int {
		recv <- captured{u, f, p}
		return 1
	})
	b1.SetHandler(func(u, f string, p []byte) int { return 0 }) // publisher has no local devices

	// Give both subscriptions a moment to register, then publish from b1.
	deadline := time.Now().Add(3 * time.Second)
	for {
		b1.Publish("u1", "devA", []byte("hello-across"))
		select {
		case got := <-recv:
			if got.user != "u1" || got.from != "devA" || string(got.payload) != "hello-across" {
				t.Fatalf("unexpected message: %+v", got)
			}
			return
		case <-time.After(150 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("instance 2 never received the cross-instance clip")
			}
		}
	}
}

// A nil-URL / bad URL fails clearly instead of silently.
func TestRedisBadURL(t *testing.T) {
	if _, err := NewRedis("not-a-url", "x"); err == nil {
		t.Fatal("expected error for invalid redis URL")
	}
}
