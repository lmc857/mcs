package sipserver

import (
	"sync"
	"time"
)

// Event is pushed to WebSocket/console subscribers.
type Event struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Data any       `json:"data"`
}

type eventBus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[int]chan Event{}}
}

func (b *eventBus) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

func (b *eventBus) publish(e Event) {
	b.mu.Lock()
	subs := make([]chan Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow console: drop; the dashboard also polls REST and resyncs.
		}
	}
}

func (s *Server) emit(typ string, data any) {
	s.events.publish(Event{Type: typ, TS: time.Now(), Data: data})
}

// Subscribe exposes the event stream for the HTTP/WebSocket layer.
func (s *Server) Subscribe() (<-chan Event, func()) {
	return s.events.subscribe()
}
