// Package session implements the owned account session: cookie jar,
// transport, the auth state machine and single-flight recovery.
//
// There is deliberately no mutable global session or account singleton. One
// Session owns one account's credentials, cookie jar, auth generation and
// cancellation handles. Network calls never hold the session lock across
// unrelated slow operations.
//
// The session spawns no background work except one bounded JWC callback
// goroutine (service_callback.go), which Logout joins. Heartbeat scheduling
// is the caller's (or a future server's) responsibility.
package session

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/detector"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// AuthState enumerates the auth state machine states (spec section 5).
type AuthState string

// Auth states. Spellings match the Rust serde rename_all snake_case.
const (
	StateSignedOut              AuthState = "signed_out"
	StateAuthenticatingGateway  AuthState = "authenticating_gateway"
	StateAuthenticatingIdentity AuthState = "authenticating_identity"
	StateAuthenticated          AuthState = "authenticated"
	StateReconnecting           AuthState = "reconnecting"
	StateTemporarilyUnavailable AuthState = "temporarily_unavailable"
	StateInteractionRequired    AuthState = "interaction_required"
)

// Kind classifies whether a request mutates server state.
type Kind int

// Request kinds.
const (
	// Read is a safe, idempotent request: may be retried once after
	// successful reauth.
	Read Kind = iota
	// Write is a mutating request: never blindly replayed after an
	// uncertain outcome.
	Write
)

// Config is the per-session configuration. Defaults come from the source
// endpoint catalog; tests override base URLs to point at a local mock server.
type Config struct {
	VPNServer        string `json:"vpn_server"`
	JWCBase          string `json:"jwc_base"`
	YKTBase          string `json:"ykt_base"`
	AACAPIBase       string `json:"aac_api_base"`
	LaborAPIBase     string `json:"labor_api_base"`
	ISIMBase         string `json:"isim_base"`
	RepairBase       string `json:"repair_base"`
	CompetitionBase  string `json:"competition_base"`
	TimeoutMS        int64  `json:"timeout_ms"`
	MaxResponseBytes int    `json:"max_response_bytes"`
	// AllowWrites: whether writes must be explicitly permitted
	// (CLI-level flag).
	AllowWrites bool `json:"allow_writes"`
}

// DefaultConfig returns the source-backed defaults.
func DefaultConfig() Config {
	return Config{
		VPNServer:        config.VPNServer,
		JWCBase:          config.JWCBase,
		YKTBase:          config.YKTBase,
		AACAPIBase:       config.AACAPIBase,
		LaborAPIBase:     config.LaborAPIBase,
		ISIMBase:         config.ISIMBase,
		RepairBase:       config.RepairBase,
		CompetitionBase:  config.CompetitionBase,
		TimeoutMS:        config.DefaultTimeoutMS,
		MaxResponseBytes: config.DefaultMaxResponseBytes,
		AllowWrites:      false,
	}
}

// SendOutcome is the result of one logical send, including sanitized
// redirect traces.
type SendOutcome struct {
	Response  *transport.Response
	Detection detector.Detection
	// Redirects holds sanitized redirect hops (scheme/host/path only).
	Redirects []string
	// Synthetic marks a debug-injected failure.
	Synthetic bool
}

// Inspection is a redacted session inspection snapshot.
type Inspection struct {
	UserIDHash string            `json:"user_id_hash"`
	State      string            `json:"state"`
	Generation uint64            `json:"generation"`
	ECLogged   bool              `json:"ec_logged"`
	UAAPLogged bool              `json:"uaap_logged"`
	HasTWFID   bool              `json:"has_twfid"`
	Cookies    []cookies.Summary `json:"cookies"`
}

type recoveryOutcome int

const (
	recoveryRecovered recoveryOutcome = iota
	recoveryUnavailable
	recoveryFatal
)

type recoveryRecord struct {
	epoch   uint64
	outcome recoveryOutcome
	message string
	// err carries the underlying authenticate failure for recoveryFatal,
	// so callers surface the REAL cause (invalid credentials, gateway
	// captcha) instead of a generic outcome-unknown.
	err error
}

// Session is the owned account session.
//
// Create with New; the zero value is not usable. Concurrently safe: all
// mutable state is either atomic or short-critical-section locked, and the
// network calls never hold a lock across awaits.
type Session struct {
	userID     string
	ecPassword string
	password   string
	transport  transport.Transport
	jar        *cookies.Jar
	cfg        Config
	sink       atomic.Pointer[events.Sink]
	ctx        context.Context
	cancelFunc context.CancelFunc
	cancelMu   sync.Mutex
	// opMu guards the current operation token. CancelOperation cancels it
	// (aborting in-flight work) and installs a fresh one; the session's
	// login survives.
	opMu     sync.Mutex
	opCtx    context.Context
	opCancel context.CancelFunc

	state atomic.Pointer[AuthState]
	// stateMu guards state transitions to keep read-modify-write atomic.
	stateMu sync.Mutex

	generation atomic.Uint64
	twfid      atomic.Pointer[string]
	ecLogged   atomic.Bool
	uaapLogged atomic.Bool
	// recoveryMu / authMu are capacity-1 semaphores (ctx-aware).
	recoveryMu chan struct{}
	authMu     chan struct{}
	// recoveryState caches per-epoch recovery outcomes (single-flight).
	recoveryState sync.Map // epoch -> *recoveryRecord

	serviceTicketsMu sync.Mutex
	serviceTickets   map[string]string

	// svcMu guards svcCb, the JWC callback started by the last UAAP success
	// (see service_callback.go). Login does not wait for it.
	svcMu sync.Mutex
	svcCb *serviceCallback

	// pendingEC is the EC auth context captured when a CAPTCHA image was
	// last fetched, used by ECLoginWithCaptcha. Cleared once a submit
	// completes.
	pendingEC    sync.Map
	requestSeq   atomic.Uint64
	operationCtx atomic.Pointer[string]

	// recoveryFn, when set, performs the session's re-authentication
	// INSTEAD of the built-in raw authenticate() — the server wires a
	// pool-deriving implementation (borrow an EC slot, clone its gateway
	// cookies, run the UAAP login on top) so recoveries never burn EC
	// gateway logins. Must be set before the first Send.
	recoveryFn func(ctx context.Context) error
}

// New creates a session for one account. transport is injectable for tests.
func New(userID, ecPassword, password string, tr transport.Transport) *Session {
	return NewWithConfig(userID, ecPassword, password, tr, DefaultConfig())
}

// NewWithConfig creates a session with an explicit configuration.
func NewWithConfig(userID, ecPassword, password string, tr transport.Transport, cfg Config) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	opCtx, opCancel := context.WithCancel(ctx)
	s := &Session{
		userID:         userID,
		ecPassword:     ecPassword,
		password:       password,
		transport:      tr,
		jar:            cookies.New(),
		cfg:            cfg,
		recoveryMu:     make(chan struct{}, 1),
		authMu:         make(chan struct{}, 1),
		serviceTickets: map[string]string{},
	}
	root := StateSignedOut
	s.state.Store(&root)
	s.ctx, s.cancelFunc = ctx, cancel
	s.opCtx, s.opCancel = opCtx, opCancel
	return s
}

// SetRecoveryFunc installs the recovery implementation (server wiring;
// see the recoveryFn field). Must be called before the first Send.
func (s *Session) SetRecoveryFunc(fn func(ctx context.Context) error) {
	s.recoveryFn = fn
}

// UserID returns the account identifier (student number).
func (s *Session) UserID() string { return s.userID }

// Config returns the session configuration.
func (s *Session) Config() Config { return s.cfg }

// Transport returns the underlying transport (used by manifest, debug probes).
func (s *Session) Transport() transport.Transport { return s.transport }

// SetProxy re-points the session's egress (the EC gateway binds a TWFID to
// its birth IP, so a session's proxy exit IS its identity — see the
// school's proxy-pool recovery).
func (s *Session) SetProxy(proxyURL string) {
	if ht, ok := s.transport.(*transport.HTTPTransport); ok {
		ht.SetProxy(proxyURL)
	}
}

// ProxyURL reports the session's current egress proxy endpoint ("" =
// direct). The borrow paths clone it so the validation rides the host's
// exit.
func (s *Session) ProxyURL() string {
	if ht, ok := s.transport.(*transport.HTTPTransport); ok {
		return ht.ProxyURL()
	}
	return ""
}

// State returns the current auth state.
func (s *Session) State() AuthState {
	return *s.state.Load()
}

func (s *Session) setState(next AuthState) {
	s.stateMu.Lock()
	s.state.Store(&next)
	s.stateMu.Unlock()
}

// Generation returns the monotonic auth generation. It advances on every
// successful login/logout.
func (s *Session) Generation() uint64 { return s.generation.Load() }

func (s *Session) bumpGeneration() uint64 { return s.generation.Add(1) }

// SetEventSink attaches an observation sink for availability telemetry.
// Passing nil clears it. Safe to swap at runtime.
func (s *Session) SetEventSink(sink events.Sink) {
	if sink == nil {
		s.sink.Store(nil)
		return
	}
	s.sink.Store(&sink)
}

func (s *Session) sinkOrNull() events.Sink {
	if p := s.sink.Load(); p != nil {
		return *p
	}
	return nil
}

// Emit publishes an observation (exported for service-layer telemetry).
func (s *Session) Emit(kind events.Kind, operation, outcome string, durationMS int64) {
	if sink := s.sinkOrNull(); sink != nil {
		sink.Record(events.Observation{
			Kind:       kind,
			Operation:  operation,
			Outcome:    outcome,
			DurationMS: durationMS,
			Generation: s.Generation(),
			Synthetic:  false,
		})
	}
}

// SetOperationContext sets the logical operation name used as HTTP debug
// metadata. Empty string clears it.
func (s *Session) SetOperationContext(operation string) {
	if operation == "" {
		s.operationCtx.Store(nil)
		return
	}
	s.operationCtx.Store(&operation)
}

// NextRequestID generates the next logical request id for debug metadata.
func (s *Session) NextRequestID() string {
	seq := s.requestSeq.Add(1)
	return fmtHex(s.Generation()&0xffffffff) + "-" + itoaHex(seq)
}

// TWFID returns the ACTIVE gateway TwfID. The jar's DOMAIN-scoped cookie
// is the live truth — the gateway rotates the TWFID during identity
// phases and mint walks (the jar ingests the rotation), while the
// in-memory field is only the EC-login-time snapshot; the school's
// sdp-app-session binding honours whatever the gateway currently honors,
// so a stale in-memory value makes every business call fail with HTTP
// 500 (2026-10-16 production incident: a recovery pinned the old value
// and the post-recovery mint 500ed). The in-memory value is the fallback
// for sessions whose jar was cleared but state kept.
func (s *Session) TWFID() (string, bool) {
	domainValue, anyValue := "", ""
	haveDomain, haveAny := false, false
	for _, c := range s.jar.All() {
		if c.Name != "TWFID" {
			continue
		}
		if !haveAny {
			anyValue, haveAny = c.Value, true
		}
		if strings.HasPrefix(c.Domain, ".") {
			domainValue, haveDomain = c.Value, true
		}
	}
	if haveDomain {
		return domainValue, true
	}
	if haveAny {
		return anyValue, true
	}
	if v := s.twfid.Load(); v != nil {
		return *v, true
	}
	return "", false
}

func (s *Session) setTWFID(v string) {
	if v == "" {
		s.twfid.Store(nil)
	} else {
		s.twfid.Store(&v)
	}
}

// SetTWFID overrides the active gateway TwfID (session snapshot/restore).
func (s *Session) SetTWFID(v string) { s.setTWFID(v) }

// WritesAllowed reports whether writes are permitted for this session.
func (s *Session) WritesAllowed() bool { return s.cfg.AllowWrites }

// RequireWritePermission errors for writes when the session is not
// configured to permit them.
func (s *Session) RequireWritePermission(kind Kind) error {
	if kind == Write && !s.cfg.AllowWrites {
		return errs.NewInvalidArgument("write operation requires explicit --allow-write permission")
	}
	return nil
}

func (s *Session) cancelled() bool {
	select {
	case <-s.ctx.Done():
		return true
	default:
	}
	s.opMu.Lock()
	op := s.opCtx
	s.opMu.Unlock()
	if op != nil {
		select {
		case <-op.Done():
			return true
		default:
		}
	}
	return false
}

// CancelOperation cancels in-flight work only; the session (and its login)
// remains usable afterwards.
func (s *Session) CancelOperation() {
	s.opMu.Lock()
	if s.opCancel != nil {
		s.opCancel()
	}
	s.opCtx, s.opCancel = context.WithCancel(s.ctx)
	s.opMu.Unlock()
	s.Emit(events.KindCancellation, "cancel", "cancelled", 0)
}

// operationContext merges the caller context with the session and current
// operation cancellation tokens. The returned release must be deferred by
// the caller for the lifetime of the operation.
func (s *Session) operationContext(caller context.Context) (context.Context, func()) {
	s.opMu.Lock()
	op := s.opCtx
	s.opMu.Unlock()
	if op == nil {
		return newMergedContext(s.ctx, caller)
	}
	return newMergedContext(s.ctx, op, caller)
}

// OperationContext is the exported form (service-layer helpers). The
// returned release must be called when the operation completes.
func (s *Session) OperationContext(caller context.Context) (context.Context, func()) {
	return s.operationContext(caller)
}

// Logout signs out, cancelling outstanding work and advancing the generation
// so a late reconnect can never repopulate a new account's cache.
func (s *Session) Logout() {
	s.cancelMu.Lock()
	s.cancelFunc()
	s.cancelMu.Unlock()
	s.signOutInternal()
}

func (s *Session) signOutInternal() {
	s.detachServiceCallback()
	s.setState(StateSignedOut)
	s.ecLogged.Store(false)
	s.uaapLogged.Store(false)
	s.setTWFID("")
	s.jar.Clear(cookies.All)
	s.bumpGeneration()
	s.Emit(events.KindLogout, "logout", "signed_out", 0)
}

// MarkAuthenticatedForTest marks a reconstructed session as authenticated.
// Used by session-replay tests (iptest) that restore a cookie snapshot and
// must skip the login flow; not intended for production use.
func (s *Session) MarkAuthenticatedForTest() {
	s.setState(StateAuthenticated)
	s.ecLogged.Store(true)
	s.uaapLogged.Store(true)
}

// HashAccount returns a non-reversible account fingerprint used in telemetry
// and diagnostics.
func HashAccount(userID string) string {
	sum := md5.Sum(append([]byte("loveace-core-account:"), userID...))
	return hex.EncodeToString(sum[:8])
}

// Inspect returns a redacted session inspection for diagnostics (no cookie values).
func (s *Session) Inspect() Inspection {
	_, has := s.TWFID()
	return Inspection{
		UserIDHash: HashAccount(s.userID),
		State:      string(s.State()),
		Generation: s.Generation(),
		ECLogged:   s.ecLogged.Load(),
		UAAPLogged: s.uaapLogged.Load(),
		HasTWFID:   has,
		Cookies:    s.jar.Inspect(),
	}
}

// CookieHeader computes the Cookie header value for a URL from the jar.
func (s *Session) CookieHeader(rawURL string) string {
	host, path, secure := cookies.URLParts(rawURL)
	return s.jar.HeaderFor(host, path, secure)
}

func (s *Session) storeResponseCookies(resp *transport.Response) {
	host, path, _ := cookies.URLParts(resp.FinalURL)
	if host == "" {
		return
	}
	for _, sc := range resp.SetCookieHeaders() {
		s.jar.StoreSetCookie(sc, host, path)
	}
}

// --- origin helpers ---

type origin struct {
	scheme, host string
	port         string // normalized; "" only when unknown
}

func originOf(rawURL string) origin {
	u, err := url.Parse(rawURL)
	if err != nil {
		return origin{}
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case port != "":
	case scheme == "http":
		port = "80"
	case scheme == "https":
		port = "443"
	}
	return origin{scheme: scheme, host: host, port: port}
}

func resolveLocation(current, location string) string {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return location
	}
	base, err := url.Parse(current)
	if err != nil {
		return location
	}
	ref, err := url.Parse(location)
	if err != nil {
		return location
	}
	return base.ResolveReference(ref).String()
}

// SanitizeURL renders scheme://host[:port]/path (no query) for redirect traces.
func SanitizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// isAmbiguous reports whether a post-dispatch error leaves a write's outcome
// uncertain.
func isAmbiguous(code errs.Code) bool {
	switch code {
	case errs.Network, errs.Timeout, errs.HTTP, errs.TemporarilyUnavailable, errs.OutcomeUnknown:
		return true
	default:
		return false
	}
}

func itoaHex(v uint64) string {
	const digits = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf[i:])
}

func fmtHex(v uint64) string { return itoaHex(v) }
