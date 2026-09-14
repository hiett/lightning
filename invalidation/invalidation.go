// Package invalidation connects a source of change events to lightning's
// reactive core, so that a live query re-runs when the data behind it moves.
//
// The reactive core tracks, for each running computation, which resources it
// read; invalidating a resource re-runs everything that read it. What it does
// not have is any opinion about where change events come from. This package
// supplies that missing half:
//
//   - a resolver calls Depend to say which keys it is about to read, and then
//     reads them;
//   - something that changes data calls Invalidate with the same keys;
//   - every live query that read those keys re-runs.
//
// The order matters: Depend before the read, never after. Invalidating a key
// only reaches the computations already registered against it, so a resolver
// that read at one moment and registered at a later one misses anything that
// happened in between and serves a stale value until the next, unrelated
// change to the same key.
//
// A key is an opaque string, and its meaning is entirely yours: "user:42",
// "table:orders", a tenant id, whatever granularity you want to invalidate at.
//
// Across processes, events travel through a Source. MemorySource is the
// in-process implementation, which is all a single-process server needs; see
// the package documentation on Source for how to write one over a message bus
// or Postgres LISTEN/NOTIFY.
package invalidation

import (
	"context"
	"sync"

	"github.com/hiett/lightning/reactive"
)

// Source carries invalidation events between the processes that serve a
// schema. A single-process server can use MemorySource; a fleet needs
// something that fans events out to every process, because a live query on one
// machine must re-run when another machine changes the data.
//
// A Postgres implementation is a thin wrapper over LISTEN/NOTIFY: Publish
// issues NOTIFY on a channel with the keys as its payload, and Subscribe holds
// a dedicated connection issuing LISTEN and calls deliver for each
// notification. It is not included here because it would put a database driver
// in a GraphQL library's dependency list, which is the over-reach this fork
// exists to undo.
type Source interface {
	// Publish announces that the values behind keys have changed. It must
	// reach every subscriber, including subscribers in the publishing process.
	Publish(ctx context.Context, keys []string) error

	// Subscribe delivers published keys to deliver until ctx is done, then
	// returns. deliver may be called concurrently and must not block for long.
	Subscribe(ctx context.Context, deliver func(keys []string)) error
}

// Dependency is what an Invalidator records with the reactive core for each
// key a computation depends on. It is visible to a reactive dependency
// callback, which is how a caller can observe or forward the key set of a
// running computation.
type Dependency struct {
	Key string
}

// An Invalidator maps invalidation keys onto reactive resources.
//
// The zero value is not usable; call New.
type Invalidator struct {
	source Source

	mu        sync.Mutex
	resources map[string]*reactive.Resource
}

// New returns an Invalidator that publishes and receives through source.
func New(source Source) *Invalidator {
	return &Invalidator{
		source:    source,
		resources: make(map[string]*reactive.Resource),
	}
}

// Depend records that the computation running in ctx reads the values behind
// keys, so that it re-runs when any of them is invalidated.
//
// Call it from a resolver, with the keys it is about to read, **before** the
// read. Invalidating a key only reaches the computations already registered
// against it, so registering afterwards drops any change that happened in the
// window between the read and the registration — and the live query then serves
// a stale value until something else invalidates the same key.
//
// Outside a live query it does nothing, so a resolver need not know whether it
// is being executed for a one-off request or a subscription.
func (i *Invalidator) Depend(ctx context.Context, keys ...string) {
	for _, key := range keys {
		reactive.AddDependency(ctx, i.resource(key), Dependency{Key: key})
	}
}

// Invalidate announces that the values behind keys have changed. Every live
// query that depended on any of them re-runs.
//
// The announcement goes through the Source, so it reaches live queries in other
// processes as well as this one.
func (i *Invalidator) Invalidate(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return i.source.Publish(ctx, keys)
}

// Run receives invalidation events until ctx is done. A server runs it once,
// in its own goroutine, for the lifetime of the process.
func (i *Invalidator) Run(ctx context.Context) error {
	return i.source.Subscribe(ctx, i.strobe)
}

// strobe re-runs every computation depending on any of keys.
func (i *Invalidator) strobe(keys []string) {
	i.mu.Lock()
	resources := make([]*reactive.Resource, 0, len(keys))
	for _, key := range keys {
		if resource, ok := i.resources[key]; ok {
			resources = append(resources, resource)
		}
	}
	i.mu.Unlock()

	// Strobe outside the lock: it re-runs computations, which call back into
	// Depend.
	for _, resource := range resources {
		resource.Strobe()
	}
}

// resource returns the resource for a key, creating it if this is the first
// computation to depend on that key.
func (i *Invalidator) resource(key string) *reactive.Resource {
	i.mu.Lock()
	defer i.mu.Unlock()

	if resource, ok := i.resources[key]; ok {
		return resource
	}

	resource := reactive.NewResource()
	i.resources[key] = resource

	// Drop the resource once nothing depends on it any more, so that a server
	// with an unbounded key space does not accumulate one resource per key it
	// has ever seen. The identity check matters: by the time the cleanup runs,
	// the map may already hold a newer resource for the same key.
	resource.Cleanup(func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if current, ok := i.resources[key]; ok && current == resource {
			delete(i.resources, key)
		}
	})

	return resource
}

// Keys reports the invalidation keys the computation running in ctx has
// depended on so far. It is useful for logging what a live query is watching,
// and for a Source that must register interest in specific keys rather than
// receiving everything.
func Keys(ctx context.Context) []string {
	dependencies := reactive.Dependencies(ctx)
	keys := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		if dependency, ok := dependency.(Dependency); ok {
			keys = append(keys, dependency.Key)
		}
	}
	return keys
}

// WatchKeys arranges for watch to be called with each invalidation key the
// computations running under the returned context depend on, as they depend on
// it.
//
// A Source that needs to register interest per key — LISTEN on a channel,
// subscribe to a topic — uses this to learn what to register for, rather than
// waiting until a computation has finished.
func WatchKeys(ctx context.Context, watch func(key string)) context.Context {
	return reactive.WithDependencyCallback(ctx, func(ctx context.Context, dependency reactive.Dependency) {
		if dependency, ok := dependency.(Dependency); ok {
			watch(dependency.Key)
		}
	})
}
