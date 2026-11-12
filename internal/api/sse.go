package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// event is one Server-Sent Event, pre-encoded once for all clients.
type event struct {
	name string
	data []byte
}

// broker fans events out to connected panels. Slow clients lose events
// rather than blocking ingestion; the panel refetches history on reconnect.
type broker struct {
	mu      sync.Mutex
	clients map[chan event]struct{}
}

func newBroker() *broker { return &broker{clients: map[chan event]struct{}{}} }

func (b *broker) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := event{name, data}
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		select {
		case c <- ev:
		default: // client too slow: drop
		}
	}
}

func (b *broker) subscribe() chan event {
	c := make(chan event, 256)
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	return c
}

func (b *broker) unsubscribe(c chan event) {
	b.mu.Lock()
	delete(b.clients, c)
	b.mu.Unlock()
}

// handleStream serves GET /api/v1/stream as text/event-stream.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	c := s.broker.subscribe()
	defer s.broker.unsubscribe(c)

	// Initial snapshot so the panel is complete without a separate request.
	snap, _ := json.Marshal(s.snapshot())
	fmt.Fprintf(w, "retry: 3000\nevent: snapshot\ndata: %s\n\n", snap)
	fl.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-c:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data); err != nil {
				return
			}
			// Drain whatever else is queued before flushing.
			for n := len(c); n > 0; n-- {
				ev = <-c
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
			}
			fl.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
