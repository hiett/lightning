package reactive

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestResourceSurvivesItsLastDependent checks that a Resource can be depended
// on again after everything that depended on it has gone away.
//
// A node is released when its out set empties, and release() invalidates before
// it tears down. Because the invalidated flag is sticky, a released Resource
// used to invalidate every new dependent the instant it attached: the
// computation re-ran, depended on the same dead Resource, was invalidated
// again, and spun for ever, burning a full query execution each time. Holding a
// Resource on a struct for the life of a row is the documented pattern, so this
// was reachable by doing the ordinary thing.
func TestResourceSurvivesItsLastDependent(t *testing.T) {
	r := NewResource()

	var firstRuns int64
	first := NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		atomic.AddInt64(&firstRuns, 1)
		AddDependency(ctx, r, nil)
		return nil, nil
	}, 0, false)
	time.Sleep(200 * time.Millisecond)

	// Stopping the only computation that depended on r releases r.
	first.Stop()
	time.Sleep(300 * time.Millisecond)

	var secondRuns int64
	second := NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		atomic.AddInt64(&secondRuns, 1)
		AddDependency(ctx, r, nil)
		return nil, nil
	}, 0, false)
	defer second.Stop()

	time.Sleep(time.Second)

	if got := atomic.LoadInt64(&secondRuns); got != 1 {
		t.Errorf("the second computation ran %d times; nothing invalidated the resource, so it should have run once", got)
	}

	// And the resource still works: strobing it reruns the new computation.
	r.Strobe()
	time.Sleep(300 * time.Millisecond)

	if got := atomic.LoadInt64(&secondRuns); got != 2 {
		t.Errorf("after a strobe the second computation should have run twice, ran %d times", got)
	}
}

// TestResourceUsedOutsideARerunnerStaysUsable checks that resolving a field
// outside a live query — an ordinary HTTP request — does not kill a Resource
// that a live query depends on.
//
// AddDependency outside a rerunner attaches a pre-released node, which drops
// the resource's reference count straight to zero and releases it.
func TestResourceUsedOutsideARerunnerStaysUsable(t *testing.T) {
	r := NewResource()

	// An ordinary request: no rerunner in the context.
	AddDependency(context.Background(), r, nil)
	time.Sleep(200 * time.Millisecond)

	var runs int64
	runner := NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		atomic.AddInt64(&runs, 1)
		AddDependency(ctx, r, nil)
		return nil, nil
	}, 0, false)
	defer runner.Stop()

	time.Sleep(time.Second)

	if got := atomic.LoadInt64(&runs); got != 1 {
		t.Errorf("the live query ran %d times; a plain request must not poison the resource", got)
	}
}
