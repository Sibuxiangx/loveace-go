package session

// The JWC service-ticket callback, decoupled from login.
//
// Right after UAAP success the CAS service ticket must be redeemed against
// the JWC callback URL, or the JWC cookies never exist. That redemption costs
// ~1.2-2s on the login critical path, but login itself does not need its
// result: only JWC requests do. So the callback runs in one bounded background
// goroutine, login returns, and the JWC request helpers await the gate before
// their first request. Logout and superseding logins join it.

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
)

// serviceCallback is one in-flight JWC callback.
type serviceCallback struct {
	done   chan struct{}
	cancel context.CancelFunc
	// abandoned marks a callback superseded by a newer login or by sign-out.
	// Its outcome is not an availability observation of the school.
	abandoned atomic.Bool
}

// startServiceCallback redeems the CAS ticket for JWC in the background.
// A previous callback is cancelled and joined first, so two callbacks never
// write the jar concurrently.
func (s *Session) startServiceCallback(callbackURL string) {
	s.svcMu.Lock()
	prev := s.svcCb
	s.svcCb = nil
	s.svcMu.Unlock()
	joinServiceCallback(prev)

	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(s.cfg.TimeoutMS)*time.Millisecond)
	cb := &serviceCallback{done: make(chan struct{}), cancel: cancel}
	s.svcMu.Lock()
	s.svcCb = cb
	s.svcMu.Unlock()

	go func() {
		defer close(cb.done)
		defer cancel()
		err := s.consumeTicketInner(ctx, callbackURL)
		// A superseded callback, or one whose session already ended, is not
		// an availability observation of the school.
		if cb.abandoned.Load() || s.ctx.Err() != nil {
			return
		}
		outcome := "success"
		if err != nil {
			outcome = "failed"
		}
		s.Emit(events.KindServiceTicket, "jwc_callback", outcome, 0)
	}()
}

// AwaitServiceCallback blocks until the JWC callback started by the last UAAP
// success has finished, or ctx ends. With none pending it returns at once. A
// failed callback is not an error here: JWC availability is service-scoped,
// and the request proceeds exactly as it did before the callback was async.
func (s *Session) AwaitServiceCallback(ctx context.Context) error {
	s.svcMu.Lock()
	cb := s.svcCb
	s.svcMu.Unlock()
	if cb == nil {
		return nil
	}
	select {
	case <-cb.done:
		return nil
	case <-ctx.Done():
		return errs.NewCancelled()
	}
}

// joinServiceCallback cancels a callback and waits until its goroutine is
// gone, so nothing it does can land after the caller moves on.
func joinServiceCallback(cb *serviceCallback) {
	if cb == nil {
		return
	}
	cb.abandoned.Store(true)
	cb.cancel()
	<-cb.done
}

// detachServiceCallback removes and joins the pending callback (sign-out).
func (s *Session) detachServiceCallback() {
	s.svcMu.Lock()
	cb := s.svcCb
	s.svcCb = nil
	s.svcMu.Unlock()
	joinServiceCallback(cb)
}

// consumeTicketInner GETs the callback URL; any 2xx means JWC accepted it.
func (s *Session) consumeTicketInner(ctx context.Context, callbackURL string) error {
	response, err := s.RawGet(ctx, callbackURL, nil, 0)
	if err != nil {
		return err
	}
	if response.Status >= 200 && response.Status < 300 {
		return nil
	}
	return errs.NewHTTP(response.Status, "JWC callback returned non-2xx")
}
