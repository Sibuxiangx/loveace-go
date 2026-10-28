package testutil

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// CallbackGate wraps a login fixture and observes the JWC service-ticket
// callback. It can hold the callback until Release, delay it, answer it with
// a fixed status, and records the arrival order of callback and JWC requests.
type CallbackGate struct {
	inner       transport.Transport
	block       bool
	status      int
	delay       time.Duration
	release     chan struct{}
	started     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	order       []string
}

// NewCallbackGate wraps inner. With block=true the callback request waits
// for Release (or its context) before it reaches inner.
func NewCallbackGate(inner transport.Transport, block bool) *CallbackGate {
	return &CallbackGate{
		inner:   inner,
		block:   block,
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
}

// WithStatus makes the callback answer with status instead of the fixture.
func (g *CallbackGate) WithStatus(status int) *CallbackGate {
	g.status = status
	return g
}

// WithDelay holds every callback for d before it completes.
func (g *CallbackGate) WithDelay(d time.Duration) *CallbackGate {
	g.delay = d
	return g
}

// Started closes when the callback request first arrives at the gate.
func (g *CallbackGate) Started() <-chan struct{} { return g.started }

// Release lets a blocked callback proceed. Safe to call more than once.
func (g *CallbackGate) Release() {
	g.releaseOnce.Do(func() { close(g.release) })
}

// Order returns the arrival order of observed requests: "callback" and "jwc".
func (g *CallbackGate) Order() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.order...)
}

// Callbacks returns how many JWC callbacks have completed through the gate.
func (g *CallbackGate) Callbacks() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, tag := range g.order {
		if tag == "callback" {
			n++
		}
	}
	return n
}

func (g *CallbackGate) record(tag string) {
	g.mu.Lock()
	g.order = append(g.order, tag)
	g.mu.Unlock()
}

// isCallback matches only the CAS ticket redemption (the redirect carries
// ticket=) and the JWC root probe. The CAS login page itself names the
// callback in its service= parameter, so a bare substring match is wrong.
func isCallback(url string) bool {
	return strings.Contains(url, "j_spring_cas_security_check?ticket=") || url == config.UAAPCheckURL
}

// Execute implements transport.Transport.
func (g *CallbackGate) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	if !isCallback(r.URL) {
		if strings.Contains(r.URL, "://jwcxk2") {
			g.record("jwc")
		}
		return g.inner.Execute(ctx, r)
	}
	g.startedOnce.Do(func() { close(g.started) })
	if g.block {
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if g.delay > 0 {
		select {
		case <-time.After(g.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	g.record("callback")
	if g.status != 0 {
		return transport.NewResponse(g.status, []byte("callback failed")), nil
	}
	return g.inner.Execute(ctx, r)
}
