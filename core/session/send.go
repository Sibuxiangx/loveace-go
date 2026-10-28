package session

import (
	"context"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/detector"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/retry"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// Send executes a logical request with cookie handling, bounded redirects and
// session-expiry recovery. Reads may replay once after successful reauth;
// writes never replay and surface outcome_unknown instead.
func (s *Session) Send(caller context.Context, request *transport.Request, kind Kind) (*SendOutcome, error) {
	if err := s.RequireWritePermission(kind); err != nil {
		return nil, err
	}
	ctx, release := s.operationContext(caller)
	defer release()
	if err := s.EnsureAuthenticated(ctx); err != nil {
		return nil, err
	}
	if request.RequestID == "" {
		request.RequestID = s.NextRequestID()
	}
	if request.Operation == "" {
		if op := s.operationCtx.Load(); op != nil {
			request.Operation = *op
		}
	}
	generationAtFailure := s.Generation()

	// For writes, an ambiguous post-dispatch failure (timeout/network/5xx)
	// must not be replayed or reported as a clean failure.
	outcome, err := s.sendFollowingRedirects(ctx, request)
	if err != nil {
		if kind == Write && isAmbiguous(errs.CodeOf(err)) {
			return nil, errs.NewOutcomeUnknown("write outcome uncertain after dispatch: " + err.Error())
		}
		return nil, err
	}
	if !outcome.Detection.IsExpired {
		return outcome, nil
	}
	s.Emit(events.KindSessionExpiry, "send", "expired", 0)
	switch kind {
	case Read:
		recovered, rerr := s.recover(ctx, generationAtFailure)
		if rerr != nil {
			// A CAPTCHA demanded mid-recovery has no challenge id the read's
			// caller could answer, and no retry clears it on its own. Only a
			// fresh login (which owns the captcha flow) can finish, so tell the
			// client to sign in again. Credentials failures propagate as before.
			if errs.CodeOf(rerr) == errs.InteractionRequired {
				return nil, errs.NewAuthRequiredSimple()
			}
			return nil, rerr
		}
		if !recovered {
			return nil, errs.NewTemporarilyUnavailable("session recovery deferred: network unavailable")
		}
		// Re-check generation before publishing the retried result.
		if s.cancelled() {
			return nil, errs.NewCancelled()
		}
		retried, err := s.sendFollowingRedirects(ctx, request)
		if err != nil {
			return nil, err
		}
		if retried.Detection.IsExpired {
			return nil, errs.NewAuthRequiredSimple()
		}
		return retried, nil
	default: // Write
		return nil, errs.NewOutcomeUnknown("write response indicated an expired session; refusing to replay")
	}
}

// SendRead sends a read request with bounded retry (source RetryHandler uses
// 3 attempts with exponential backoff).
func (s *Session) SendRead(caller context.Context, request *transport.Request) (*transport.Response, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	return retry.With(ctx, retry.Attempts(3), func(ctx context.Context) (*transport.Response, error) {
		outcome, err := s.Send(ctx, request, Read)
		if err != nil {
			// A gateway CAPTCHA during login or recovery has no challenge id the
			// read's caller could answer. Un-stick the state so the next login
			// starts clean, and report AuthRequired so the client signs in again
			// (where the captcha flow lives) instead of retrying a dead end.
			if errs.CodeOf(err) == errs.InteractionRequired {
				s.setState(StateTemporarilyUnavailable)
				return nil, errs.NewAuthRequiredSimple()
			}
			return nil, err
		}
		return outcome.Response, nil
	})
}

// SendWrite sends a write request exactly once. Writes are permission-gated
// and never retried.
func (s *Session) SendWrite(caller context.Context, request *transport.Request) (*transport.Response, error) {
	if err := s.RequireWritePermission(Write); err != nil {
		return nil, err
	}
	outcome, err := s.Send(caller, request, Write)
	if err != nil {
		return nil, err
	}
	return outcome.Response, nil
}

// SendFollowingRedirects executes one logical request following redirects
// with correct method and credential semantics. Does not perform session
// recovery.
func (s *Session) SendFollowingRedirects(caller context.Context, request *transport.Request) (*SendOutcome, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	return s.sendFollowingRedirects(ctx, request)
}

func (s *Session) sendFollowingRedirects(ctx context.Context, request *transport.Request) (*SendOutcome, error) {
	current := request.Clone()
	var redirects []string
	// Credentials on the original request must not leak cross-origin.
	manualCookie, hasManualCookie := current.GetHeader("Cookie")
	originalOrigin := originOf(current.URL)

	for {
		if s.cancelled() {
			return nil, errs.NewCancelled()
		}
		if err := ctx.Err(); err != nil {
			return nil, errs.NewCancelled()
		}

		currentOrigin := originOf(current.URL)
		prepared := s.prepareRequest(current, manualCookie, hasManualCookie, originalOrigin, currentOrigin, true)

		response, err := s.transport.Execute(ctx, prepared)
		if err != nil {
			return nil, err
		}
		if s.cancelled() {
			// Do not write late cookies/TWFID into a cancelled session.
			return nil, errs.NewCancelled()
		}
		s.storeResponseCookies(response)

		location, hasLocation := response.Header("location")
		primary := detector.Detector{}.Inspect(response.FinalURL, response.Body, location)
		// Legacy login-page detection for non-por endpoints.
		detection := primary
		if !primary.IsExpired {
			var d detector.Detector
			if d.IsLegacyLoginPage(current.URL, response.Body) {
				detection = detector.Detection{
					IsExpired: true,
					Kind:      "vpn_session_expired",
					Signals:   []string{"legacy_login_page"},
				}
			}
		}

		if !response.IsRedirect() || !hasLocation {
			return &SendOutcome{
				Response:  response,
				Detection: detection,
				Redirects: redirects,
				Synthetic: false,
			}, nil
		}
		if len(redirects) >= config.MaxRedirects {
			return nil, errs.NewHTTP(response.Status,
				"redirect chain exceeded "+itoa(config.MaxRedirects)+" hops")
		}
		nextURL := resolveLocation(current.URL, location)
		redirects = append(redirects, SanitizeURL(nextURL))

		// Method semantics: 303 always GET; 307/308 preserve; 301/302
		// downgrade POST to GET (browser behaviour).
		var nextMethod transport.Method
		switch response.Status {
		case 303:
			nextMethod = transport.Get
		case 307, 308:
			nextMethod = current.Method
		default:
			if current.Method == transport.Post {
				nextMethod = transport.Get
			} else {
				nextMethod = current.Method
			}
		}
		dropBody := nextMethod == transport.Get

		nextOrigin := originOf(nextURL)
		crossOrigin := nextOrigin != originalOrigin
		headers := make([][2]string, 0, len(current.Headers))
		for _, kv := range current.Headers {
			name := kv[0]
			if strings.EqualFold(name, "host") {
				continue
			}
			if crossOrigin && (strings.EqualFold(name, "authorization") || strings.EqualFold(name, "cookie")) {
				continue
			}
			if strings.EqualFold(name, "content-length") {
				continue
			}
			if dropBody && strings.EqualFold(name, "content-type") {
				continue
			}
			headers = append(headers, kv)
		}

		var nextBody []byte
		if !dropBody {
			nextBody = current.Body
		}
		current = &transport.Request{
			Method:    nextMethod,
			URL:       nextURL,
			Headers:   headers,
			Body:      nextBody,
			TimeoutMS: current.TimeoutMS,
			MaxBytes:  current.MaxBytes,
			Operation: current.Operation,
			RequestID: current.RequestID,
		}
	}
}

// prepareRequest builds a transport request: default UA, jar cookie
// injection, manual cookie preservation only for the original origin.
func (s *Session) prepareRequest(
	request *transport.Request,
	manualCookie string,
	hasManualCookie bool,
	originalOrigin, currentOrigin origin,
	injectCookies bool,
) *transport.Request {
	headers := make([][2]string, 0, len(request.Headers)+2)
	var hasUA bool
	for _, kv := range request.Headers {
		if strings.EqualFold(kv[0], "cookie") {
			continue
		}
		if strings.EqualFold(kv[0], "user-agent") {
			hasUA = true
		}
		if strings.EqualFold(kv[0], "host") {
			continue // handled by the transport via req.Host
		}
		headers = append(headers, kv)
	}
	if !hasUA {
		headers = append(headers, [2]string{"User-Agent", config.UADesktop})
	}

	var cookieValue string
	switch {
	case !injectCookies:
		// Debug/no-cookie path: only an explicit caller cookie is forwarded.
		if currentOrigin == originalOrigin && hasManualCookie {
			cookieValue = manualCookie
		}
	case currentOrigin == originalOrigin:
		if hasManualCookie {
			cookieValue = manualCookie
		} else {
			cookieValue = s.CookieHeader(request.URL)
		}
	default:
		// Cross-origin hops only receive cookies that match the jar.
		cookieValue = s.CookieHeader(request.URL)
	}
	if cookieValue != "" {
		headers = append(headers, [2]string{"Cookie", cookieValue})
	}

	return &transport.Request{
		Method:    request.Method,
		URL:       request.URL,
		Headers:   headers,
		Body:      request.Body,
		TimeoutMS: request.TimeoutMS,
		MaxBytes:  request.MaxBytes,
		Operation: request.Operation,
		RequestID: request.RequestID,
	}
}

// recover performs single-flight recovery: concurrent callers merge onto one
// reauth. The outcome for a given failure generation is cached, so waiters
// share a success or failure instead of each reattempting.
func (s *Session) recover(caller context.Context, generationAtFailure uint64) (bool, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	// Acquire the recovery semaphore (ctx-aware).
	select {
	case s.recoveryMu <- struct{}{}:
		defer func() { <-s.recoveryMu }()
	case <-ctx.Done():
		return false, errs.NewCancelled()
	}

	// Another task may have already recovered while we waited.
	if s.Generation() > generationAtFailure && s.State() == StateAuthenticated {
		return true, nil
	}
	if s.cancelled() {
		return false, errs.NewCancelled()
	}
	// Share a previously recorded outcome for this epoch.
	if v, ok := s.recoveryState.Load(generationAtFailure); ok {
		rec := v.(*recoveryRecord)
		switch rec.outcome {
		case recoveryRecovered:
			return true, nil
		case recoveryUnavailable:
			return false, nil
		default:
			if rec.err != nil {
				return false, rec.err
			}
			return false, errs.NewOutcomeUnknown(rec.message)
		}
	}

	startedMS := nowMillis()
	s.setState(StateReconnecting)

	// Hold the auth lock for the whole reauthentication.
	select {
	case s.authMu <- struct{}{}:
	case <-ctx.Done():
		return false, errs.NewCancelled()
	}
	defer func() { <-s.authMu }()

	s.ecLogged.Store(false)
	s.uaapLogged.Store(false)

	var outcome recoveryOutcome
	var message string
	var authErr error
	var authFail error
	if s.recoveryFn != nil {
		// The server's pool-deriving recovery: borrow an EC slot, clone
		// its gateway cookies onto this session, re-run the identity
		// login with the session's own credentials. Never an EC gateway
		// login — that budget belongs to the pool's refill loop.
		authFail = s.recoveryFn(ctx)
	} else {
		authFail = s.authenticate(ctx)
	}
	if authFail != nil {
		if errs.CodeOf(authFail) == errs.Cancelled {
			return false, authFail
		}
		if errs.CodeOf(authFail) == errs.InteractionRequired {
			// A CAPTCHA demanded mid-recovery has no solver: the challenge
			// belongs to no login flow, and gateway pollution self-heals
			// in minutes. Land in the retryable state (the sticky
			// InteractionRequired would brick the session — nothing would
			// ever solve that challenge) and share a retryable outcome.
			s.setState(StateTemporarilyUnavailable)
			outcome = recoveryUnavailable
		} else if errs.CodeOf(authFail) == errs.TemporarilyUnavailable {
			outcome = recoveryUnavailable
		} else {
			outcome = recoveryFatal
			message = authFail.Error()
			authErr = authFail
		}
	} else {
		// Recovery re-establishes JWC too: the retried request must not race
		// the JWC callback this re-login started.
		if err := s.AwaitServiceCallback(ctx); err != nil {
			return false, err
		}
		outcome = recoveryRecovered
	}
	durationMS := nowMillis() - startedMS

	s.recoveryState.Store(generationAtFailure, &recoveryRecord{epoch: generationAtFailure, outcome: outcome, message: message, err: authErr})
	switch outcome {
	case recoveryRecovered:
		s.Emit(events.KindReconnect, "recover", "success", durationMS)
		return true, nil
	case recoveryUnavailable:
		s.Emit(events.KindReconnect, "recover", "unavailable", durationMS)
		return false, nil
	default:
		s.Emit(events.KindReconnect, "recover", "failed", durationMS)
		if authErr != nil {
			return false, authErr
		}
		return false, errs.NewOutcomeUnknown(message)
	}
}

// RawGet is a low-level GET used during authentication (no recovery, no auth gate).
func (s *Session) RawGet(caller context.Context, rawURL string, headers [][2]string, timeoutMS int64) (*transport.Response, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	request := transport.NewRequest(transport.Get, rawURL)
	if timeoutMS <= 0 {
		timeoutMS = s.cfg.TimeoutMS
	}
	request.TimeoutMS = timeoutMS
	request.MaxBytes = s.cfg.MaxResponseBytes
	request.Headers = append(request.Headers, headers...)
	outcome, err := s.sendFollowingRedirects(ctx, request)
	if err != nil {
		return nil, err
	}
	return outcome.Response, nil
}

// RawPost is a low-level form POST used during authentication and ticket minting.
func (s *Session) RawPost(caller context.Context, rawURL string, form [][2]string, headers [][2]string, timeoutMS int64) (*transport.Response, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	request := s.formPostRequest(rawURL, form, headers, timeoutMS)
	outcome, err := s.sendFollowingRedirects(ctx, request)
	if err != nil {
		return nil, err
	}
	return outcome.Response, nil
}

// RawGetOnce executes exactly one hop (no redirect following), so the caller
// can inspect the Location (e.g. an already-authenticated CAS service-ticket
// redirect).
func (s *Session) RawGetOnce(caller context.Context, rawURL string, headers [][2]string, timeoutMS int64) (*transport.Response, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	request := transport.NewRequest(transport.Get, rawURL)
	if timeoutMS <= 0 {
		timeoutMS = s.cfg.TimeoutMS
	}
	request.TimeoutMS = timeoutMS
	request.MaxBytes = s.cfg.MaxResponseBytes
	request.Headers = append(request.Headers, headers...)
	return s.executeOnce(ctx, request, true)
}

// RawPostOnce is like RawPost but executes exactly one hop: no redirect
// following, so the caller can inspect the Location (e.g. a CAS service
// ticket) and decide what to do next.
func (s *Session) RawPostOnce(caller context.Context, rawURL string, form [][2]string, headers [][2]string, timeoutMS int64) (*transport.Response, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	request := s.formPostRequest(rawURL, form, headers, timeoutMS)
	return s.executeOnce(ctx, request, true)
}

func (s *Session) formPostRequest(rawURL string, form [][2]string, headers [][2]string, timeoutMS int64) *transport.Request {
	if timeoutMS <= 0 {
		timeoutMS = s.cfg.TimeoutMS
	}
	request := transport.NewRequest(transport.Post, rawURL).
		Header("Content-Type", "application/x-www-form-urlencoded").
		WithBody([]byte(config.FormEncode(form)))
	request.TimeoutMS = timeoutMS
	request.MaxBytes = s.cfg.MaxResponseBytes
	request.Headers = append(request.Headers, headers...)
	return request
}

// executeOnce executes a single prepared hop (no redirects, no recovery).
// Response cookies are stored only when withCookies is set, so a no-cookie
// debug probe cannot mutate the session jar.
func (s *Session) executeOnce(ctx context.Context, request *transport.Request, withCookies bool) (*transport.Response, error) {
	org := originOf(request.URL)
	manualCookie, hasManual := request.GetHeader("Cookie")
	prepared := s.prepareRequest(request, manualCookie, hasManual, org, org, withCookies)
	response, err := s.transport.Execute(ctx, prepared)
	if err != nil {
		return nil, err
	}
	if s.cancelled() {
		return nil, errs.NewCancelled()
	}
	if withCookies {
		s.storeResponseCookies(response)
	}
	return response, nil
}

// RedirectLocations follows redirects one hop at a time and returns the raw
// Location URLs (including query strings such as register?ticket=...). Used
// for CAS ticket extraction; callers must not log the returned URLs verbatim.
//
// Unlike Send, the walk sees raw hops without sendFollowingRedirects' expiry
// handling, so a session that idled past the gateway TTL would surface the
// gateway auth page (or a transport-level rejection) directly. Mirror the
// read semantics here: detect expiry in the chain, recover single-flight,
// and retry the walk exactly once.
func (s *Session) RedirectLocations(caller context.Context, rawURL string, max int) ([]string, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	generationAtFailure := s.Generation()

	locations, expired, err := s.walkLocations(ctx, rawURL, max)
	if err == nil && !expired {
		return locations, nil
	}
	if err != nil {
		return nil, err
	}
	s.Emit(events.KindSessionExpiry, "send", "expired", 0)
	recovered, rerr := s.recover(ctx, generationAtFailure)
	if rerr != nil {
		return nil, rerr
	}
	if !recovered {
		return nil, errs.NewTemporarilyUnavailable("session recovery deferred: network unavailable")
	}
	// Re-check cancellation before publishing the retried result.
	if s.cancelled() {
		return nil, errs.NewCancelled()
	}
	locations, _, err = s.walkLocations(ctx, rawURL, max)
	return locations, err
}

func (s *Session) walkLocations(ctx context.Context, rawURL string, max int) (locations []string, expired bool, err error) {
	current := rawURL
	for i := 0; i < max; i++ {
		if s.cancelled() {
			return nil, false, errs.NewCancelled()
		}
		org := originOf(current)
		request := transport.NewRequest(transport.Get, current)
		request.TimeoutMS = s.cfg.TimeoutMS
		request.MaxBytes = s.cfg.MaxResponseBytes
		prepared := s.prepareRequest(request, "", false, org, org, true)
		response, err := s.transport.Execute(ctx, prepared)
		if err != nil {
			return nil, false, err
		}
		s.storeResponseCookies(response)

		location, hasLocation := response.Header("location")
		primary := detector.Detector{}.Inspect(response.FinalURL, response.Body, location)
		// The mint chain legitimately STARTS at cas/login, so the bare
		// cas_login_url signal (meaning "a read landed on the CAS login")
		// is not expiry here. A real stale session shows the CAS login
		// FORM, the EC portal or the gateway auth XML — any other signal.
		hopExpired := primary.IsExpired && !casURLOnly(primary.Signals)
		if !hopExpired {
			var d detector.Detector
			if d.IsLegacyLoginPage(current, response.Body) {
				hopExpired = true
			}
		}
		if !response.IsRedirect() || !hasLocation {
			debugf("walk hop=%d host=%s status=%d final expired=%v", i, hostOf(current), response.Status, hopExpired)
			return locations, hopExpired, nil
		}
		next := resolveLocation(current, location)
		// While the gateway session is valid it rewrites service redirects
		// into the vpn2 namespace; a Location that escapes it (raw internal
		// host) means the gateway no longer honours this session. Following
		// it can only time out on an unreachable internal address, so stop
		// and recover instead.
		escaped := !strings.Contains(next, config.VPNHostSuffix)
		debugf("walk hop=%d host=%s status=%d next-host=%s escaped=%v expired=%v",
			i, hostOf(current), response.Status, hostOf(next), escaped, hopExpired)
		if escaped {
			return locations, true, nil
		}
		locations = append(locations, next)
		current = next
	}
	return locations, false, nil
}

// casURLOnly reports whether the detector fired solely on the CAS login URL
// — the mint chain's normal entry hop — rather than on an actual login form
// or portal page.
func casURLOnly(signals []string) bool {
	if len(signals) != 1 {
		return false
	}
	return signals[0] == "cas_login_url"
}

// ClearCookies clears cookies by scope, preserving credentials.
func (s *Session) ClearCookies(scope cookies.Scope) { s.jar.Clear(scope) }

// ExpireServiceTicket invalidates service-derived tickets without touching
// gateway login.
func (s *Session) ExpireServiceTicket() {
	s.jar.Clear(cookies.Service)
	s.serviceTicketsMu.Lock()
	s.serviceTickets = map[string]string{}
	s.serviceTicketsMu.Unlock()
}

// CachedTicket returns the cached CAS-derived ticket for a named service
// (aac/labor), if any.
func (s *Session) CachedTicket(service string) (string, bool) {
	s.serviceTicketsMu.Lock()
	defer s.serviceTicketsMu.Unlock()
	v, ok := s.serviceTickets[service]
	return v, ok
}

// StoreTicket stores a CAS-derived ticket for a named service.
func (s *Session) StoreTicket(service, ticket string) {
	s.serviceTicketsMu.Lock()
	defer s.serviceTicketsMu.Unlock()
	s.serviceTickets[service] = ticket
}

// Tickets returns a copy of every cached service ticket (aac/labor/...).
// The school-side tickets are effectively long-lived (static in practice —
// the operator observed a single manual rotation across years), so the
// server persists them with the session snapshot and re-seeds them onto
// restored sessions: a restored or re-logged session then talks to the
// business APIs DIRECTLY instead of re-walking the 4-hop CAS mint on
// every cold session.
func (s *Session) Tickets() map[string]string {
	s.serviceTicketsMu.Lock()
	defer s.serviceTicketsMu.Unlock()
	out := make(map[string]string, len(s.serviceTickets))
	for k, v := range s.serviceTickets {
		out[k] = v
	}
	return out
}

// SeedTickets installs previously persisted tickets (see Tickets) onto a
// freshly built session, before any business call.
func (s *Session) SeedTickets(tickets map[string]string) {
	if len(tickets) == 0 {
		return
	}
	s.serviceTicketsMu.Lock()
	defer s.serviceTicketsMu.Unlock()
	if s.serviceTickets == nil {
		s.serviceTickets = map[string]string{}
	}
	for k, v := range tickets {
		if k != "" && v != "" {
			s.serviceTickets[k] = v
		}
	}
}

// ResetServiceTicket clears a named service's cached ticket.
func (s *Session) ResetServiceTicket(service string) {
	s.serviceTicketsMu.Lock()
	defer s.serviceTicketsMu.Unlock()
	delete(s.serviceTickets, service)
}

// Jar exposes the cookie jar for snapshot/restore (server use).
func (s *Session) Jar() *cookies.Jar { return s.jar }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
