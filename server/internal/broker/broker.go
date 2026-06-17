// Package broker decouples clip fan-out from local delivery so the server can
// run as a single instance (Local) or scale horizontally behind a shared
// pub/sub (Redis): every instance delivers a published clip to its own
// connections, skipping the originating device.
package broker

import "time"

// Handler delivers a published payload to the local connections of userID,
// excluding fromDevice, and returns how many local recipients it had. payload
// is the already-marshalled clip envelope.
type Handler func(userID, fromDevice string, payload []byte) int

type Broker interface {
	// SetHandler registers local delivery. Called once before Publish.
	SetHandler(Handler)
	// Publish makes payload available for delivery on every instance.
	Publish(userID, fromDevice string, payload []byte)
	Close() error
}

// Local is the default single-instance broker: Publish delivers synchronously
// to local connections. No external dependencies.
type Local struct{ h Handler }

func NewLocal() *Local { return &Local{} }

func (l *Local) SetHandler(h Handler) { l.h = h }

func (l *Local) Publish(userID, fromDevice string, payload []byte) {
	if l.h == nil {
		return
	}
	// Single-instance: if a peer that just connected isn't registered yet
	// (a sub-ms race between its hello and this clip), retry once briefly.
	if l.h(userID, fromDevice, payload) == 0 {
		time.Sleep(50 * time.Millisecond)
		l.h(userID, fromDevice, payload)
	}
}

func (l *Local) Close() error { return nil }
