package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hub fans live events out to SSE clients. Every event gets an id
// "<boot>-<seq>"; the last ringSize events are kept so a client that
// reconnects with Last-Event-ID gets what it missed, or a "reset" telling
// it to refetch snapshots when that is no longer possible (restart, gap).
type Hub struct {
	mu      sync.Mutex
	boot    string
	seq     uint64
	ring    []sseEvent
	next    int // ring write position
	full    bool
	subs    map[*sseSub]struct{}
	maxSubs func() int
	ping    time.Duration
}

type sseEvent struct {
	seq     uint64
	typ     string
	data    []byte
	monitor string // the monitor it is about, "" for none
}

// sseFilter decides what one client receives: the event as is, a changed
// copy, or nothing (false).
type sseFilter func(ev sseEvent) (sseEvent, bool)

type sseSub struct {
	ch      chan sseEvent
	dropped bool
}

// subBuffer is how many events a slow client may lag behind before it is
// disconnected (it reconnects and replays from the ring).
const subBuffer = 256

// NewHub creates a hub. boot must differ between process runs.
func NewHub(boot string, ringSize int, maxSubs func() int) *Hub {
	return &Hub{boot: boot, ring: make([]sseEvent, max(1, ringSize)), subs: map[*sseSub]struct{}{}, maxSubs: maxSubs, ping: 15 * time.Second}
}

// Publish sends an event of type typ to every client.
func (h *Hub) Publish(typ string, v any) { h.PublishFor(typ, "", v) }

// PublishFor sends an event about a monitor ("" for none), so that clients
// that may not see that monitor can be left out.
func (h *Hub) PublishFor(typ, monitor string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := sseEvent{seq: h.seq, typ: typ, data: data, monitor: monitor}
	h.ring[h.next] = ev
	h.next = (h.next + 1) % len(h.ring)
	if h.next == 0 {
		h.full = true
	}
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			// Too slow: cut it loose; it will reconnect and replay.
			s.dropped = true
			close(s.ch)
			delete(h.subs, s)
		}
	}
}

// Clients returns the number of connected clients.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// replayFrom returns the buffered events after lastID, or reset=true when
// they are not all available any more.
func (h *Hub) replayFrom(lastID string) (evs []sseEvent, reset bool) {
	if lastID == "" {
		return nil, false
	}
	boot, seqStr, ok := strings.Cut(lastID, "-")
	if !ok || boot != h.boot {
		return nil, true
	}
	last, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil || last > h.seq {
		return nil, true
	}
	oldest := h.seq + 1 // when the ring is empty
	n := h.next
	if h.full {
		n = len(h.ring)
	}
	if n > 0 {
		oldest = h.seq - uint64(n) + 1
	}
	if last+1 < oldest {
		return nil, true // gap: events were overwritten
	}
	for i := 0; i < n; i++ {
		idx := i
		if h.full {
			idx = (h.next + i) % len(h.ring)
		}
		if ev := h.ring[idx]; ev.seq > last {
			evs = append(evs, ev)
		}
	}
	return evs, false
}

func (h *Hub) subscribe(lastID string) (*sseSub, []sseEvent, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.maxSubs != nil && len(h.subs) >= h.maxSubs() {
		return nil, nil, false, false
	}
	evs, reset := h.replayFrom(lastID)
	s := &sseSub{ch: make(chan sseEvent, subBuffer)}
	h.subs[s] = struct{}{}
	return s, evs, reset, true
}

func (h *Hub) unsubscribe(s *sseSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.ch)
	}
}

func (h *Hub) id(seq uint64) string { return h.boot + "-" + strconv.FormatUint(seq, 10) }

// ServeHTTP streams events. Authentication happens before this handler.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.ServeFiltered(w, r, nil) }

// ServeFiltered streams events through filter (nil = everything).
func (h *Hub) ServeFiltered(w http.ResponseWriter, r *http.Request, filter sseFilter) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, Errorf(http.StatusInternalServerError, CodeInternal, "streaming unsupported"))
		return
	}
	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = r.URL.Query().Get("last_event_id")
	}
	sub, replay, reset, ok := h.subscribe(lastID)
	if !ok {
		writeError(w, rateLimited(5*time.Second))
		return
	}
	defer h.unsubscribe(sub)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	if reset {
		fmt.Fprintf(w, "event: reset\ndata: {}\n\n")
	}
	for _, ev := range replay {
		if filter != nil {
			var keep bool
			if ev, keep = filter(ev); !keep {
				continue
			}
		}
		h.write(w, ev)
	}
	fl.Flush()

	ping := time.NewTicker(h.ping)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.ch:
			if !ok {
				return // dropped as too slow
			}
			if filter != nil {
				var keep bool
				if ev, keep = filter(ev); !keep {
					continue
				}
			}
			h.write(w, ev)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, "event: ping\ndata: {}\n\n")
			fl.Flush()
		}
	}
}

func (h *Hub) write(w http.ResponseWriter, ev sseEvent) {
	fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", h.id(ev.seq), ev.typ, ev.data)
}
