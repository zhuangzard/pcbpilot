package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one message on the live stream.
type Event struct {
	Seq  uint64    `json:"seq"`
	Type string    `json:"type"` // activity | ask | run | project | status
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// bus fans events out to SSE subscribers and keeps a short ring for late
// joiners. Slow subscribers drop events instead of blocking the daemon.
type bus struct {
	mu   sync.Mutex
	seq  uint64
	ring []Event
	max  int
	subs map[chan Event]struct{}
}

func newBus(max int) *bus { return &bus{max: max, subs: map[chan Event]struct{}{}} }

func (b *bus) publish(typ string, data any) Event {
	b.mu.Lock()
	b.seq++
	ev := Event{Seq: b.seq, Type: typ, At: time.Now().UTC(), Data: data}
	b.ring = append(b.ring, ev)
	if len(b.ring) > b.max {
		b.ring = b.ring[len(b.ring)-b.max:]
	}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	b.mu.Unlock()
	return ev
}

func (b *bus) recent(typ string, n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Event
	for i := len(b.ring) - 1; i >= 0 && len(out) < n; i-- {
		if typ == "" || b.ring[i].Type == typ {
			out = append(out, b.ring[i])
		}
	}
	return out
}

func (b *bus) subscribe() (chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *bus) subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// handleEvents is the SSE stream. It sends a hello with the daemon identity
// (so a reconnecting page can tell a restarted daemon by pid/startedAt), then
// events, with a heartbeat every 15 s.
func (c *Console) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "NO_STREAM", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := c.bus.subscribe()
	defer cancel()
	send := func(ev Event) bool {
		b, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	fmt.Fprintf(w, "retry: 2000\n\n")
	send(Event{Type: "hello", At: time.Now().UTC(), Data: c.daemonInfo()})
	hb := time.NewTicker(c.heartbeat)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case ev := <-ch:
			if !send(ev) {
				return
			}
		case <-hb.C:
			if !send(Event{Type: "heartbeat", At: time.Now().UTC(), Data: map[string]any{"subscribers": c.bus.subscribers()}}) {
				return
			}
		}
	}
}
