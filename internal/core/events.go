package core

import "sync"

// Event is what gets pushed to SSE subscribers. Data is any JSON-serialisable
// value; the encoder decides the shape.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: map[chan Event]struct{}{}}
}

func (b *Bus) Subscribe() chan Event {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Publish never blocks. A subscriber whose buffer is full drops the event,
// which is the intended trade: the file list is re-fetched on reconnect, so a
// dropped notification is recoverable but a blocked publisher is not.
func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
