package main

// ddCache is a tiny FIFO-based set with a fixed capacity.
// It returns true if the id already existed; otherwise stores it and returns false.
type ddCache struct {
	cap int
	buf []string
	set map[string]struct{}
}

func newDD(capacity int) *ddCache {
	if capacity <= 0 {
		capacity = 0
	}
	return &ddCache{cap: capacity, set: make(map[string]struct{}, capacity)}
}

func (d *ddCache) ExistsOrAdd(id string) bool {
	if d.cap == 0 || id == "" {
		return false
	}
	if _, ok := d.set[id]; ok {
		return true
	}
	d.set[id] = struct{}{}
	d.buf = append(d.buf, id)
	if len(d.buf) > d.cap {
		ev := d.buf[0]
		d.buf = d.buf[1:]
		delete(d.set, ev)
	}
	return false
}
