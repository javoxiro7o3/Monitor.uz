package agent

import (
	"path/filepath"
	"testing"
	"time"

	"monitor.uz/internal/model"
)

func TestBufferEvictsOldest(t *testing.T) {
	b := NewBuffer(3)
	for i := int64(1); i <= 5; i++ {
		b.Push(model.Sample{TS: i})
	}
	got := b.Peek(10)
	if len(got) != 3 || got[0].TS != 3 || got[2].TS != 5 || b.Dropped() != 2 {
		t.Fatalf("got %v dropped %d", got, b.Dropped())
	}
}

func TestBufferAckByTimestamp(t *testing.T) {
	b := NewBuffer(3)
	for i := int64(1); i <= 3; i++ {
		b.Push(model.Sample{TS: i})
	}
	batch := b.Peek(2) // TS 1,2 in flight
	b.Push(model.Sample{TS: 4})
	b.Push(model.Sample{TS: 5}) // evicts 1 and 2 while in flight
	b.Ack(batch[len(batch)-1].TS)
	got := b.Peek(10)
	if len(got) != 3 || got[0].TS != 3 {
		t.Fatalf("ack removed unsent samples: %v", got)
	}
}

func TestBufferSaveLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "buf.json.gz")
	b := NewBuffer(10)
	b.Push(model.Sample{TS: 1, CPU: &model.CPU{Usage: 5}})
	b.Push(model.Sample{TS: 2})
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	c := NewBuffer(10)
	n, err := c.Load(p)
	if err != nil || n != 2 || c.Peek(1)[0].CPU.Usage != 5 {
		t.Fatalf("load: %d %v", n, err)
	}
	if n, _ := c.Load(p); n != 0 {
		t.Fatal("buffer file must be removed after load")
	}
}

func TestBackoff(t *testing.T) {
	d := time.Duration(0)
	for i := 0; i < 10; i++ {
		d = nextBackoff(d, false)
	}
	if d != 10*time.Second {
		t.Fatalf("max backoff %v", d)
	}
	if nextBackoff(0, true) != 5*time.Minute {
		t.Fatal("unauthorized backoff")
	}
}
