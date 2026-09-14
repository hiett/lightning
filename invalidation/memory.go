package invalidation

import (
	"context"
	"sync"
)

// MemorySource delivers invalidation events within one process.
//
// It is all a single-process server needs, and it is what the tests and the
// example server use. A server running on more than one machine needs a Source
// that crosses process boundaries, because a live query on one machine must
// re-run when another machine changes the data.
type MemorySource struct {
	mu          sync.Mutex
	next        int
	subscribers map[int]func([]string)
}

// NewMemorySource returns an empty in-process source.
func NewMemorySource() *MemorySource {
	return &MemorySource{subscribers: make(map[int]func([]string))}
}

// Publish delivers keys to every current subscriber.
//
// Delivery is synchronous: when Publish returns, every subscriber has been
// called. That makes a test that changes data and then waits for a live query
// to re-run deterministic up to the reactive core's own scheduling.
func (s *MemorySource) Publish(ctx context.Context, keys []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	deliverers := make([]func([]string), 0, len(s.subscribers))
	for _, deliver := range s.subscribers {
		deliverers = append(deliverers, deliver)
	}
	s.mu.Unlock()

	// Outside the lock: a subscriber is free to publish again.
	for _, deliver := range deliverers {
		deliver(keys)
	}
	return nil
}

// Subscribe delivers published keys until ctx is done.
func (s *MemorySource) Subscribe(ctx context.Context, deliver func(keys []string)) error {
	// done guards the caller's deliver: Publish snapshots the subscriber list
	// and then calls out of the lock, so without this a publish that began
	// before Subscribe returned could still call deliver after it had.
	var done bool
	var mu sync.Mutex
	guarded := func(keys []string) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		deliver(keys)
	}

	s.mu.Lock()
	id := s.next
	s.next++
	s.subscribers[id] = guarded
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()

		mu.Lock()
		done = true
		mu.Unlock()
	}()

	<-ctx.Done()
	return ctx.Err()
}

var _ Source = (*MemorySource)(nil)
