package reactive

import (
	"context"
	"time"
)

// ExpirationTime is a dependency key for time-based invalidation.
type ExpirationTime struct {
	Time time.Time
}

// InvalidateAfter invalidates the current computation after the given duration.
func InvalidateAfter(ctx context.Context, d time.Duration) {
	r := NewResource()
	timer := time.AfterFunc(d, r.Invalidate)
	r.Cleanup(func() { timer.Stop() })
	AddDependency(ctx, r, &ExpirationTime{Time: time.Now().Add(d)})
}

// InvalidateAt invalidates the current computation at the given time.
func InvalidateAt(ctx context.Context, t time.Time) {
	InvalidateAfter(ctx, time.Until(t))
}
