// Package sessionpool manages a bounded, sharded map of account sessions for
// the future backend server: single-flight creation, TTL cleanup and a hard
// cap on live sessions.
package sessionpool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// Factory creates a session for an account (called at most once per account
// at a time thanks to single-flight).
type Factory func(ctx context.Context, account string) (*session.Session, error)

// Options configures the pool.
type Options struct {
	// MaxSessions is the hard cap (default 10000).
	MaxSessions int
	// TTL is how long an idle session survives (default 30m).
	TTL time.Duration
	// CleanupInterval is the sweeper period (default 5m); 0 disables the
	// background goroutine (use Cleanup manually, e.g. from a scheduler).
	CleanupInterval time.Duration
	// Shards is the shard count, power of two recommended (default 64).
	Shards int
}

// DefaultOptions returns server-sized defaults.
func DefaultOptions() Options {
	return Options{
		MaxSessions:     10_000,
		TTL:             30 * time.Minute,
		CleanupInterval: 5 * time.Minute,
		Shards:          64,
	}
}

type entry struct {
	s        *session.Session
	lastUsed atomic.Int64 // unix nanos
}

func newEntry(s *session.Session, nowNano int64) *entry {
	e := &entry{s: s}
	e.lastUsed.Store(nowNano)
	return e
}

// Pool is the sharded session pool.
type Pool struct {
	opts    Options
	factory Factory
	shards  []shard
}

type shard struct {
	mu       sync.Mutex
	sessions map[string]*entry
	creating map[string]*createCall
}

type createCall struct {
	done chan struct{}
	sess *session.Session
	err  error
}

// New creates a pool. If opts.CleanupInterval > 0 a background sweeper runs;
// call Close to stop it.
func New(opts Options, factory Factory) *Pool {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 10_000
	}
	if opts.TTL <= 0 {
		opts.TTL = 30 * time.Minute
	}
	if opts.Shards <= 0 {
		opts.Shards = 64
	}
	p := &Pool{opts: opts, factory: factory}
	p.shards = make([]shard, opts.Shards)
	for i := range p.shards {
		p.shards[i] = shard{
			sessions: map[string]*entry{},
			creating: map[string]*createCall{},
		}
	}
	if opts.CleanupInterval > 0 {
		go func() {
			ticker := time.NewTicker(opts.CleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				p.Cleanup(time.Now())
			}
		}()
	}
	return p
}

func (p *Pool) shardFor(account string) *shard {
	var h uint32
	for i := 0; i < len(account); i++ {
		h = h*31 + uint32(account[i])
	}
	return &p.shards[h%uint32(len(p.shards))]
}

// Get returns an existing session or creates one (single-flight per account).
func (p *Pool) Get(ctx context.Context, account string) (*session.Session, error) {
	sh := p.shardFor(account)
	now := time.Now()

	sh.mu.Lock()
	if e, ok := sh.sessions[account]; ok {
		e.lastUsed.Store(now.UnixNano())
		sh.mu.Unlock()
		return e.s, nil
	}
	if call, ok := sh.creating[account]; ok {
		// Single-flight: wait for the in-flight creation.
		sh.mu.Unlock()
		select {
		case <-call.done:
			return call.sess, call.err
		case <-ctx.Done():
			return nil, errs.NewCancelled()
		}
	}
	if len(sh.sessions) >= p.opts.MaxSessions {
		sh.mu.Unlock()
		return nil, errs.NewTemporarilyUnavailable(fmt.Sprintf(
			"session pool at capacity (%d)", p.opts.MaxSessions))
	}
	call := &createCall{done: make(chan struct{})}
	sh.creating[account] = call
	sh.mu.Unlock()

	sess, err := p.factory(ctx, account)

	sh.mu.Lock()
	delete(sh.creating, account)
	if err == nil {
		sh.sessions[account] = newEntry(sess, now.UnixNano())
	}
	sh.mu.Unlock()
	call.sess = sess
	call.err = err
	close(call.done)
	return sess, err
}

// Peek returns the session for an account without creating one.
func (p *Pool) Peek(account string) (*session.Session, bool) {
	sh := p.shardFor(account)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.sessions[account]; ok {
		e.lastUsed.Store(time.Now().UnixNano())
		return e.s, true
	}
	return nil, false
}

// Remove drops an account's session (e.g. after logout).
func (p *Pool) Remove(account string) {
	sh := p.shardFor(account)
	sh.mu.Lock()
	delete(sh.sessions, account)
	sh.mu.Unlock()
}

// Len returns the number of live sessions.
func (p *Pool) Len() int {
	n := 0
	for i := range p.shards {
		sh := &p.shards[i]
		sh.mu.Lock()
		n += len(sh.sessions)
		sh.mu.Unlock()
	}
	return n
}

// Cleanup evicts sessions idle beyond the TTL. Returns the evicted count.
func (p *Pool) Cleanup(now time.Time) int {
	cutoff := now.Add(-p.opts.TTL).UnixNano()
	evicted := 0
	for i := range p.shards {
		sh := &p.shards[i]
		sh.mu.Lock()
		for account, e := range sh.sessions {
			if e.lastUsed.Load() < cutoff {
				delete(sh.sessions, account)
				evicted++
			}
		}
		sh.mu.Unlock()
	}
	return evicted
}

// Close stops the background sweeper and clears all sessions.
func (p *Pool) Close() {
	for i := range p.shards {
		sh := &p.shards[i]
		sh.mu.Lock()
		sh.sessions = map[string]*entry{}
		sh.mu.Unlock()
	}
}
