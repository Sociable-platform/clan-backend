// Package uuidv7 makes UUIDv7 identifiers (RFC 9562): the first 48 bits are the time in
// milliseconds, so IDs sort in the order they were made. Message history is paged by ID, so the
// order must hold even for two messages made in the same millisecond.
package uuidv7

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// Generator makes UUIDv7 values that never go backwards within one process. Two IDs made in
// the same millisecond differ in a 12-bit counter, so the later one sorts later.
type Generator struct {
	now func() time.Time

	mu      sync.Mutex
	lastMs  int64
	counter uint16 // 12 bits
}

// New returns a Generator that reads the time from now (time.Now in production).
func New(now func() time.Time) *Generator {
	return &Generator{now: now}
}

// maxCounter is the largest value the 12-bit counter holds.
const maxCounter = 0x0fff

// New returns the next UUID as text, for example "0193a6c0-1111-7222-8333-944455556666",
// together with the time that is inside it (to the millisecond). If the clock went backwards
// or the counter ran out within one millisecond, the time moves forward by 1 ms instead of
// repeating, so the order is kept.
func (g *Generator) New() (id string, at time.Time) {
	g.mu.Lock()
	ms := g.now().UnixMilli()
	switch {
	case ms > g.lastMs:
		g.lastMs, g.counter = ms, randomCounter()
	case g.counter < maxCounter:
		g.counter++
	default: // same millisecond (or clock moved back) and no counter values left
		g.lastMs++
		g.counter = 0
	}
	ms, counter := g.lastMs, g.counter
	g.mu.Unlock()

	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand.Read does not fail on supported platforms
	}
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = 0x70 | byte(counter>>8) // version 7, top 4 bits of the counter
	b[7] = byte(counter)
	b[8] = b[8]&0x3f | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), time.UnixMilli(ms).UTC()
}

// randomCounter starts the counter of a new millisecond below the middle of its range, so
// there is room to count up and IDs are not predictable.
func randomCounter() uint16 {
	var r [2]byte
	if _, err := rand.Read(r[:]); err != nil {
		panic(err)
	}
	return (uint16(r[0])<<8 | uint16(r[1])) & 0x07ff
}
