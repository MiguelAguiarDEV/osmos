package ws

import (
	"sync/atomic"
	"testing"
)

// Verifica que MetricsSnapshot incluya los contadores por dispositivo
// con la clave plana "drops_device:<user>|<device>" y que se mantengan
// los contadores totales.
func TestMetricsSnapshotIncludesDeviceDrops(t *testing.T) {
	var s Server

	// Simular 2 drops para el dispositivo B del usuario u1
	s.incDeviceDrop("u1", "B")
	s.incDeviceDrop("u1", "B")
	atomic.AddInt64(&s.metrics.drops, 2)

	m := s.MetricsSnapshot()

	if got := m["drops_device:u1|B"]; got != 2 {
		t.Fatalf("drops_device:u1|B = %d, want 2", got)
	}
	if total := m["drops_total"]; total != 2 {
		t.Fatalf("drops_total = %d, want 2", total)
	}

	if cur := m["conns_current"]; cur != 0 {
		t.Fatalf("conns_current = %d, want 0", cur)
	}
	if clips := m["clips_total"]; clips != 0 {
		t.Fatalf("clips_total = %d, want 0", clips)
	}
}
