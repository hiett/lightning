package invalidation_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hiett/lightning/invalidation"
	"github.com/hiett/lightning/reactive"
	"github.com/stretchr/testify/require"
)

// runs collects the results of a live computation so a test can wait for one.
type runs struct {
	ch chan int
}

func newRuns() *runs { return &runs{ch: make(chan int, 16)} }

func (r *runs) next(t *testing.T, what string) int {
	t.Helper()
	select {
	case v := <-r.ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return 0
	}
}

func (r *runs) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case v := <-r.ch:
		t.Fatalf("expected no rerun, but the computation ran and produced %d", v)
	case <-time.After(d):
	}
}

// store is a toy data source whose reads record an invalidation key and whose
// writes announce one.
type store struct {
	invalidator *invalidation.Invalidator

	mu     sync.Mutex
	values map[string]int
}

func (s *store) read(ctx context.Context, key string) int {
	s.invalidator.Depend(ctx, "value:"+key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *store) write(ctx context.Context, key string, value int) error {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	return s.invalidator.Invalidate(ctx, "value:"+key)
}

func newStore(t *testing.T) (*store, *invalidation.Invalidator) {
	t.Helper()

	invalidator := invalidation.New(invalidation.NewMemorySource())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = invalidator.Run(ctx)
	}()
	<-ready

	return &store{invalidator: invalidator, values: map[string]int{}}, invalidator
}

// TestInvalidationRerunsLiveComputation is the core of the adapter: writing
// through the store re-runs a computation that read the same key.
func TestInvalidationRerunsLiveComputation(t *testing.T) {
	s, _ := newStore(t)
	observed := newRuns()

	require.NoError(t, s.write(context.Background(), "a", 1))

	runner := reactive.NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		observed.ch <- s.read(ctx, "a")
		return nil, nil
	}, 0, false)
	defer runner.Stop()

	require.Equal(t, 1, observed.next(t, "the first run"))

	require.NoError(t, s.write(context.Background(), "a", 2))
	require.Equal(t, 2, observed.next(t, "a rerun after the value changed"))

	require.NoError(t, s.write(context.Background(), "a", 3))
	require.Equal(t, 3, observed.next(t, "a second rerun"))
}

// TestInvalidationIsKeyed checks that an unrelated key does not re-run a
// computation. Without this the adapter would be a broadcast, and every live
// query in the process would re-run on every write.
func TestInvalidationIsKeyed(t *testing.T) {
	s, _ := newStore(t)
	observed := newRuns()

	runner := reactive.NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		observed.ch <- s.read(ctx, "watched")
		return nil, nil
	}, 0, false)
	defer runner.Stop()

	observed.next(t, "the first run")

	require.NoError(t, s.write(context.Background(), "unwatched", 99))
	observed.none(t, 200*time.Millisecond)

	require.NoError(t, s.write(context.Background(), "watched", 5))
	require.Equal(t, 5, observed.next(t, "a rerun on the watched key"))
}

// TestInvalidationOutsideALiveQuery checks that Depend is harmless when there
// is no rerunner, so a resolver need not know which it is running under.
func TestInvalidationOutsideALiveQuery(t *testing.T) {
	s, _ := newStore(t)

	require.NotPanics(t, func() {
		s.read(context.Background(), "a")
	})
	require.NoError(t, s.write(context.Background(), "a", 1))
}

// TestKeysReportsWhatAComputationWatched checks the reporting hooks, which are
// what give reactive's dependency set a purpose.
func TestKeysReportsWhatAComputationWatched(t *testing.T) {
	s, _ := newStore(t)

	done := make(chan []string, 1)
	watched := make(chan []string, 1)

	ctx := invalidation.WatchKeys(context.Background(), func(key string) {
		select {
		case existing := <-watched:
			watched <- append(existing, key)
		default:
			watched <- []string{key}
		}
	})

	runner := reactive.NewRerunner(ctx, func(ctx context.Context) (interface{}, error) {
		s.read(ctx, "a")
		s.read(ctx, "b")
		done <- invalidation.Keys(ctx)
		return nil, nil
	}, 0, false)
	defer runner.Stop()

	select {
	case keys := <-done:
		require.ElementsMatch(t, []string{"value:a", "value:b"}, keys)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the computation")
	}

	select {
	case keys := <-watched:
		require.ElementsMatch(t, []string{"value:a", "value:b"}, keys)
	case <-time.After(time.Second):
		t.Fatal("the dependency callback never fired")
	}
}

// TestInvalidateWithNoKeysIsANoOp guards a trivial edge.
func TestInvalidateWithNoKeysIsANoOp(t *testing.T) {
	_, invalidator := newStore(t)
	require.NoError(t, invalidator.Invalidate(context.Background()))
}

// TestMemorySourceDeliversToEverySubscriber checks the fan-out, which is what
// makes a multi-process Source a drop-in replacement.
func TestMemorySourceDeliversToEverySubscriber(t *testing.T) {
	source := invalidation.NewMemorySource()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	received := map[int][]string{}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = source.Subscribe(ctx, func(keys []string) {
				mu.Lock()
				received[i] = append(received[i], keys...)
				mu.Unlock()
			})
		}(i)
	}

	// Wait for all three to register.
	require.Eventually(t, func() bool {
		_ = source.Publish(context.Background(), []string{"probe"})
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 3
	}, 3*time.Second, 10*time.Millisecond)

	mu.Lock()
	received = map[int][]string{}
	mu.Unlock()

	require.NoError(t, source.Publish(context.Background(), []string{"x", "y"}))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 3)
	for _, keys := range received {
		require.Equal(t, []string{"x", "y"}, keys)
	}

	cancel()
	wg.Wait()
}
