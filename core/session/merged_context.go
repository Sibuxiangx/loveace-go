package session

import (
	"context"
	"sync"
)

// newMergedContext returns a context that cancels when any parent cancels,
// and inherits values from the first parent.
//
// The merged context is a real cancelCtx child of the first parent, so
// contexts derived FROM it (WithCancel/WithTimeout) propagate cancellation
// natively through the parent's children set — no watchdog goroutines. (A
// custom Context implementation would force every derived context to spawn
// a fallback watcher goroutine in propagateCancel, which is exactly the
// leak the bench used to report: ~2 goroutines per operation.)
//
// Cancellation from the remaining parents is injected via AfterFunc. The
// returned stop releases those registrations and cancels the merged
// context; callers must defer it for the lifetime of the operation,
// otherwise registrations accumulate on the long-lived session context.
func newMergedContext(parents ...context.Context) (context.Context, func()) {
	if len(parents) == 1 {
		return parents[0], func() {}
	}
	merged, cancel := context.WithCancel(parents[0])
	stops := make([]func() bool, 0, len(parents)-1)
	for _, p := range parents[1:] {
		p := p
		stops = append(stops, context.AfterFunc(p, func() {
			cancel()
		}))
	}
	return merged, sync.OnceFunc(func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	})
}
