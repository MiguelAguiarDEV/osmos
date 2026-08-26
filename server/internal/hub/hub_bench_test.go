package hub

import (
	"strconv"
	"testing"
)

// Benchmark broadcasting to N subscribers with small payloads.
func BenchmarkHub_Broadcast_Fanout(b *testing.B) {
	for _, subs := range []int{1, 2, 4, 8, 32, 128} {
		b.Run("subs="+strconv.Itoa(subs), func(b *testing.B) {
			h := New(64)
			// join N subscribers for same user
			var leaves []func()
			for i := 0; i < subs; i++ {
				_, leave := h.Join("u", strconv.Itoa(i))
				leaves = append(leaves, leave)
			}
			payload := make([]byte, 256)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.Broadcast("u", "from", payload)
			}
			b.StopTimer()
			for _, f := range leaves {
				f()
			}
		})
	}
}
