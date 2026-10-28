package session

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

type ctxKey string

func TestMergedContextCancelsOnAnyParent(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	m, release := newMergedContext(p1, p2, context.Background())
	defer release()
	cancel2()
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("merged context did not cancel when a secondary parent cancelled")
	}
}

func TestMergedContextCancelsWhenFirstParentCancels(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	m, release := newMergedContext(p1, context.Background())
	defer release()
	cancel1()
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("merged context did not cancel when the first parent cancelled")
	}
}

func TestMergedContextValuesFromFirstParent(t *testing.T) {
	p1 := context.WithValue(context.Background(), ctxKey("k"), "v1")
	p2 := context.WithValue(context.Background(), ctxKey("k"), "v2")
	m, release := newMergedContext(p1, p2)
	defer release()
	if got := m.Value(ctxKey("k")); got != "v1" {
		t.Fatalf("value = %v, want v1", got)
	}
}

// TestMergedContextReleaseCancelsAndIsIdempotent pins the release contract:
// release ends the operation-scoped context (Done closes, Err is Canceled)
// and is safe to call more than once.
func TestMergedContextReleaseCancelsAndIsIdempotent(t *testing.T) {
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	m, release := newMergedContext(context.Background(), p2)
	release()
	release() // must not panic or deadlock
	select {
	case <-m.Done():
	default:
		t.Fatal("release did not cancel the merged context")
	}
	if got := m.Err(); !errors.Is(got, context.Canceled) {
		t.Fatalf("Err after release = %v, want context.Canceled", got)
	}
}

// TestOperationsDoNotLeakGoroutines reproduces the bench pattern: Background
// callers (never cancelled), no logout. Before the M0 fix, every operation
// left watchdog goroutines blocked in context.propagateCancel, because the
// old mergedContext was a custom Context type: children created from it via
// WithCancel/WithTimeout degraded to goroutine-based propagation.
func TestOperationsDoNotLeakGoroutines(t *testing.T) {
	tr := transport.NewMockTransport() // unmatched requests get a synthetic 404
	base := runtime.NumGoroutine()
	const n = 200
	for i := 0; i < n; i++ {
		s := New("leak", "gw", "school", tr)
		s.MarkAuthenticatedForTest()
		if _, err := s.SendRead(context.Background(), transport.NewRequest(transport.Get, "https://x.example/probe")); err != nil {
			t.Fatalf("operation %d failed: %v", i, err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		now := runtime.NumGoroutine()
		if now <= base+n/10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: base=%d now=%d after %d operations", base, now, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// blockingTransport blocks the first Execute until the caller's context is
// done; after release is closed it responds normally.
type blockingTransport struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (b *blockingTransport) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return transport.NewResponse(200, []byte(`ok`)), nil
	}
}

// TestCancelOperationAbortsInFlightAndKeepsSessionUsable verifies that
// CancelOperation really cancels work in flight (not just flips state) and
// that the session stays usable afterwards.
func TestCancelOperationAbortsInFlightAndKeepsSessionUsable(t *testing.T) {
	tr := &blockingTransport{started: make(chan struct{}), release: make(chan struct{})}
	s := New("cancel", "gw", "school", tr)
	s.MarkAuthenticatedForTest()

	errc := make(chan error, 1)
	go func() {
		_, err := s.SendRead(context.Background(), transport.NewRequest(transport.Get, "https://x.example/slow"))
		errc <- err
	}()
	select {
	case <-tr.started:
	case <-time.After(2 * time.Second):
		t.Fatal("operation never reached the transport")
	}

	s.CancelOperation()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) && errs.CodeOf(err) != errs.Cancelled {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelOperation did not abort the in-flight operation")
	}

	// The session remains usable: the transport now answers normally.
	close(tr.release)
	if _, err := s.SendRead(context.Background(), transport.NewRequest(transport.Get, "https://x.example/ok")); err != nil {
		t.Fatalf("session unusable after CancelOperation: %v", err)
	}
}
