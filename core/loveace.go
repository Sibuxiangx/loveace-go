// Package loveace is the facade for the LoveACE Go core: a high-performance
// library port of the Rust loveace-core, designed so a future backend server
// can share one Client across thousands of account sessions.
//
// One Client owns process-level shared resources (the HTTP connection pool,
// per-host concurrency limits and the login rate limiter). Sessions are cheap
// per-account objects created through Client.NewSession.
package loveace

import (
	"context"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"sync"

	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"github.com/Sibuxiangx/loveace-go/core/session"
)

// Client owns process-level shared resources.
type Client struct {
	// Transport is the shared HTTP transport (connection pool).
	Transport transport.Transport

	// LoginLimiter rate-limits gateway logins across all sessions to avoid
	// tripping the gateway's anti-bot throttling.
	LoginLimiter *rate.Limiter

	hostLimits map[string]*semaphore.Weighted
	mu         sync.Mutex

	opts Options
}

// Options configures a Client.
type Options struct {
	// MaxConnsPerHost caps connections per upstream host (0 = unlimited).
	MaxConnsPerHost int
	// MaxConcurrentPerHost caps in-flight requests per host (default 64).
	MaxConcurrentPerHost int64
	// LoginRate is the sustained logins-per-second (default 5).
	LoginRate rate.Limit
	// LoginBurst is the login burst size (default 20).
	LoginBurst int
}

// DefaultOptions returns production-friendly defaults sized for thousands of
// concurrent sessions.
func DefaultOptions() Options {
	return Options{
		MaxConcurrentPerHost: 64,
		LoginRate:            5,
		LoginBurst:           20,
	}
}

// NewClient creates a client with the given options (zero fields defaulted).
func NewClient(opts Options) *Client {
	if opts.MaxConcurrentPerHost <= 0 {
		opts.MaxConcurrentPerHost = 64
	}
	if opts.LoginRate <= 0 {
		opts.LoginRate = 5
	}
	if opts.LoginBurst <= 0 {
		opts.LoginBurst = 20
	}
	return &Client{
		Transport:    transport.NewHTTPTransport(opts.MaxConnsPerHost),
		LoginLimiter: rate.NewLimiter(opts.LoginRate, opts.LoginBurst),
		hostLimits:   map[string]*semaphore.Weighted{},
		opts:         opts,
	}
}

// NewSession creates a per-account session bound to this client's transport.
func (c *Client) NewSession(userID, ecPassword, password string, cfg session.Config) *session.Session {
	if cfg.TimeoutMS == 0 {
		defaults := session.DefaultConfig()
		cfg.TimeoutMS = defaults.TimeoutMS
		cfg.MaxResponseBytes = defaults.MaxResponseBytes
	}
	return session.NewWithConfig(userID, ecPassword, password, c.Transport, cfg)
}

// Options returns the effective options.
func (c *Client) Options() Options { return c.opts }

// WaitLogin blocks until the shared login limiter admits one login.
func (c *Client) WaitLogin(ctx context.Context) error {
	return c.LoginLimiter.Wait(ctx)
}

// AcquireHost acquires a per-host concurrency slot (ctx-aware).
func (c *Client) AcquireHost(ctx context.Context, host string) error {
	c.mu.Lock()
	sem, ok := c.hostLimits[host]
	if !ok {
		sem = semaphore.NewWeighted(c.opts.MaxConcurrentPerHost)
		c.hostLimits[host] = sem
	}
	c.mu.Unlock()
	return sem.Acquire(ctx, 1)
}

// ReleaseHost releases a per-host concurrency slot.
func (c *Client) ReleaseHost(host string) {
	c.mu.Lock()
	sem, ok := c.hostLimits[host]
	c.mu.Unlock()
	if ok {
		sem.Release(1)
	}
}
