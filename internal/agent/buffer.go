// Package agent implements the agent's collect/push loop.
package agent

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"sync"

	"monitor.uz/internal/model"
)

// Buffer is a bounded FIFO of samples waiting to be delivered. When full,
// the oldest samples are dropped. Safe for concurrent use.
type Buffer struct {
	mu      sync.Mutex
	items   []model.Sample
	max     int
	dropped uint64
}

// NewBuffer creates a buffer holding at most max samples.
func NewBuffer(max int) *Buffer { return &Buffer{max: max} }

// Push appends a sample, evicting the oldest if full.
func (b *Buffer) Push(s model.Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.max {
		n := len(b.items) - b.max + 1
		b.items = append(b.items[:0], b.items[n:]...)
		b.dropped += uint64(n)
	}
	b.items = append(b.items, s)
}

// Peek returns up to n oldest samples without removing them.
func (b *Buffer) Peek(n int) []model.Sample {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, len(b.items))
	out := make([]model.Sample, n)
	copy(out, b.items[:n])
	return out
}

// Ack removes delivered samples: everything with TS <= lastTS. Using a
// timestamp instead of a count stays correct if eviction happened while
// the batch was in flight.
func (b *Buffer) Ack(lastTS int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := 0
	for i < len(b.items) && b.items[i].TS <= lastTS {
		i++
	}
	b.items = append(b.items[:0], b.items[i:]...)
}

// Len returns the number of buffered samples.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Dropped returns how many samples were evicted because the buffer was full.
func (b *Buffer) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Save writes the buffer to path (gzip JSON) so that undelivered samples
// survive an agent restart.
func (b *Buffer) Save(path string) error {
	b.mu.Lock()
	items := append([]model.Sample(nil), b.items...)
	b.mu.Unlock()
	if len(items) == 0 {
		os.Remove(path)
		return nil
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if err := json.NewEncoder(zw).Encode(items); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load restores samples saved by Save and removes the file.
func (b *Buffer) Load(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	var items []model.Sample
	if err := json.NewDecoder(zr).Decode(&items); err != nil {
		return 0, err
	}
	for _, s := range items {
		b.Push(s)
	}
	os.Remove(path)
	return len(items), nil
}
