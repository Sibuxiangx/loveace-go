// Package school owns the server's school-side sessions (core/session) and
// the login orchestration: full login (two phases, EC first) on the DIRECT
// egress — one EC session per one UAAP identity, captcha demands handled
// by the three-loop (auto solve → breaker → interactive challenge) — and
// per-user session groups (DESIGN.md §7). The transport is injectable
// so tests run against the core's mock fixtures.
package school

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// ChallengeTTL is how long a half-finished CAPTCHA login is kept.
const ChallengeTTL = 3 * time.Minute

// ErrNoSession is returned when no live school session exists for a user.
var ErrNoSession = errors.New("school: no live session for user")

// ErrChallengeUnknownOrExpired is returned for stale or bogus challenge ids.
var ErrChallengeUnknownOrExpired = errors.New("school: challenge unknown or expired")

// ChallengeError carries the challenge id after a CAPTCHA was demanded.
type ChallengeError struct {
	ID string
}

func (e *ChallengeError) Error() string { return "school: captcha challenge " + e.ID }

// CredentialResolver decrypts a user's school credentials for snapshot
// restores. The API layer wires this to the vault; a miss or a decrypt
// failure degrades gracefully (the snapshot restores nothing and the next
// request takes the vault-relogin path instead).
type CredentialResolver func(ctx context.Context, userID string) (account, gwPass, password string, err error)

// WithCredentialResolver teaches the manager to rebuild snapshot-restored
// sessions WITH real credentials. Without it a restored session is a
// zombie: its cookies work until the school expires them, and every
// recovery then re-logins with empty credentials and fails forever.
func WithCredentialResolver(r CredentialResolver) Option {
	return func(m *Manager) { m.credResolver = r }
}

// OnInvalidCredentials fires when the SCHOOL rejected a credential the
// manager used on the user's behalf (a derivation, a recovery). The
// server wires it to mark the vault credential invalid + audit — so the
// silent relogin path short-circuits instead of re-attempting school
// logins with a password the user has already changed elsewhere.
type OnInvalidCredentials func(ctx context.Context, userID string)

// WithInvalidCredentialsHook wires the invalidation callback.
func WithInvalidCredentialsHook(fn OnInvalidCredentials) Option {
	return func(m *Manager) { m.onInvalidCred = fn }
}

// Manager owns per-user school sessions (as session groups, §7.5) and
// pending CAPTCHA challenges.
//
// Passwords held inside pending challenges live only in memory for at most
// ChallengeTTL and are never logged.
type Manager struct {
	mu         sync.Mutex
	group      map[string]map[string]*groupEntry
	groupSize  int
	challenges map[string]*challenge

	newTransport  func() transport.Transport
	sf            singleflight.Group
	now           func() time.Time
	sinkFor       func(userID string) events.Sink
	credResolver  CredentialResolver
	onInvalidCred OnInvalidCredentials

	// Captcha 三循环(验证码原生化):solver 在场时,ecPhase 撞验证码走
	// 自动"取图→解→提交"(L1/L2);判卷结果喂 recorder 沉淀(M3);接受率
	// 崩塌由熔断器(L3)回退人工挑战路径。
	captchaSolver   CaptchaSolver
	captchaRecorder CaptchaRecorder
	capFetches      int
	capRounds       int
	br              captchaBreaker
	captcha         struct {
		autoSolved   int64
		rounds       int64
		accepted     int64
		rejected     int64
		breakerOpens int64
	}

	// Global EC login gate (§7.1): nil disables. Defaults in NewManager.
	loginGate   *rate.Limiter
	loginJitter time.Duration

	// writesAllowed is handed to every session the manager creates. The
	// server posture is writes-enabled: authorization happens at the API
	// layer (authenticated owner + mandatory Idempotency-Key, M5), and
	// the core's write semantics (never replay, outcome_unknown) govern
	// the wire. The CLI keeps its own --allow-write flag.
	writesAllowed bool

	// Invalid-credential cooling ladder (§5.2), keyed by school account.
	ladders map[string]*failLedger

	snapshots SnapshotStore
	coldAfter time.Duration
	activity  map[string]time.Time
	cooling   struct {
		evictions      int64
		snapshotsSaved int64
		restores       int64
		restoreMisses  int64
	}
}

// singleflightGroup was an alias placeholder; the real type is
// golang.org/x/sync/singleflight.Group (imported above).

type challenge struct {
	id        string
	userID    string
	s         *session.Session
	account   string
	password  string
	gwPass    string
	device    string
	platform  string
	expiresAt time.Time
	// image 是最近一次 ChallengeImage 取到的验证码图(判卷沉淀归属用)。
	image []byte
}

// NewManager builds a Manager. newTransport must return a fresh transport
// per session; every session rides the DIRECT egress (M4: the proxy pool
// and the borrowed-validation probe are retired — E-D measured the direct
// egress faster than the pool and the captcha three-loop covers the
// gateway's per-IP failure budget).
func NewManager(newTransport func() transport.Transport, opts ...Option) *Manager {
	m := &Manager{
		group:         map[string]map[string]*groupEntry{},
		groupSize:     GroupSize,
		challenges:    map[string]*challenge{},
		newTransport:  newTransport,
		now:           time.Now,
		coldAfter:     DefaultCold,
		activity:      map[string]time.Time{},
		loginGate:     rate.NewLimiter(rate.Limit(float64(DefaultLoginRatePerMinute)/60.0), 1),
		loginJitter:   DefaultLoginJitter,
		capFetches:    DefaultCaptchaFetches,
		capRounds:     DefaultCaptchaRounds,
		writesAllowed: true,
		ladders:       map[string]*failLedger{},
	}
	// 验证码熔断器的默认参数(WithCaptchaBreaker 可覆盖)。
	m.br.now = m.now
	m.br.windowSize = DefaultCaptchaWindow
	m.br.minRate = DefaultCaptchaMinRate
	m.br.openFor = DefaultCaptchaOpenFor
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Login performs the full direct login (EC gateway + UAAP identity). When
// the gateway demands a CAPTCHA and a CaptchaSolver is configured, the
// manager first runs the automatic solve loop (guess → submit → fresh
// challenge on rejection); only rounds exhausted / solver failure / open
// breaker surface a challenge (*ChallengeError) for the interactive flow
// via ChallengeImage + SolveChallenge. Concurrent logins for the same user
// share one flight.
func (m *Manager) Login(ctx context.Context, userID, account, password, gwPass, device, platform string) (*session.Session, error) {
	v, err, _ := m.sf.Do("login:"+userID, func() (any, error) {
		return m.login(ctx, userID, account, password, gwPass, device, platform)
	})
	if err != nil {
		return nil, err
	}
	return v.(*session.Session), nil
}

func (m *Manager) login(ctx context.Context, userID, account, password, gwPass, device, platform string) (*session.Session, error) {
	// §5.2 cooling ladder: an account failing credentials repeatedly is
	// cooled down before its next gateway touch (checked inside the
	// per-user flight, before any school-side work).
	if d := m.accountCooling(account); d > 0 {
		return nil, &AccountCoolingError{RetryAfter: d}
	}
	s, err := m.loginInner(ctx, userID, account, password, gwPass, device, platform)
	switch {
	case err == nil:
		m.clearAuthFailures(account)
	case errs.CodeOf(err) == errs.InvalidCredentials:
		m.noteAuthFailure(account)
	}
	return s, err
}

func (m *Manager) loginInner(ctx context.Context, userID, account, password, gwPass, device, platform string) (*session.Session, error) {
	// Capacity first: a rejected 6th login must not create a school-side
	// session (§7.5). Runs inside the per-user flight, so admission and
	// registration never interleave for one account.
	if err := m.admit(userID, device); err != nil {
		return nil, err
	}

	// Full login in two phases, ONE EC session per ONE UAAP identity, all
	// on the DIRECT egress (M4). A wrong UAAP password dies at the CAS
	// layer AFTER a successful EC login — it never burns the gateway's
	// per-IP failure quota (E-D 2026-10-21), so no pre-validation is
	// needed; a wrong GATEWAY password costs one EC failure (the captcha
	// loop + ladder absorb it) and the hook marks the vault credential.
	//
	// The gateway touch sits behind the global EC login gate — arrivals
	// queue instead of stampeding.
	if err := m.gateEC(ctx); err != nil {
		return nil, err
	}
	s := m.newSessionAt(account, gwPass, password)
	m.tagSink(userID, s)
	m.seedStoredTickets(ctx, userID, s)
	m.wireRecovery(userID, s)
	if err := m.ecPhase(ctx, s); err != nil {
		if errs.CodeOf(err) == errs.InteractionRequired {
			c := &challenge{
				id:        newChallengeID(),
				userID:    userID,
				s:         s,
				account:   account,
				password:  password,
				gwPass:    gwPass,
				device:    device,
				platform:  platform,
				expiresAt: m.now().Add(ChallengeTTL),
			}
			m.mu.Lock()
			m.challenges[c.id] = c
			m.mu.Unlock()
			return nil, &ChallengeError{ID: c.id}
		}
		return nil, err
	}
	if err := s.CompleteCaptchaLogin(ctx); err != nil {
		if errs.CodeOf(err) == errs.InvalidCredentials {
			m.noteInvalidCred(ctx, userID)
		}
		return nil, err
	}
	m.mu.Lock()
	m.register(userID, device, platform, s)
	m.mu.Unlock()
	return s, nil
}

// seedStoredTickets installs the user's PERSISTED service tickets (the
// CAS-derived aac/labor tickets held in the session snapshot) onto a
// freshly built session. The tickets are effectively static — the
// operator observed a single manual rotation across years, only after
// reporting the flaw — so the 4-hop mint walk is a one-time-ever cost
// per user, not a per-session one: with TWFID + ticket, the business
// APIs answer directly. A missing snapshot (or ticket) is silent — the
// walk remains the fallback.
func (m *Manager) seedStoredTickets(ctx context.Context, userID string, s *session.Session) {
	if m.snapshots == nil {
		return
	}
	snap, err := m.snapshots.LoadUserSnapshot(ctx, userID)
	if err != nil || snap == nil || len(snap.Tickets) == 0 {
		return
	}
	s.SeedTickets(snap.Tickets)
}

// ecPhase runs the EC gateway step and maps outcomes the same way
// session.authenticate does. When a CaptchaSolver is configured (and the
// L3 breaker is closed), a CAPTCHA demand triggers the automatic solve
// loop instead of surfacing the challenge to the user; rounds exhausted /
// solver failure fall back to the interactive path.
func (m *Manager) ecPhase(ctx context.Context, s *session.Session) error {
	for round := 0; ; round++ {
		switch out := s.ECLogin(ctx); out.Outcome {
		case session.ECSuccess:
			return nil
		case session.ECCaptchaRequired, session.ECCaptchaInvalid:
			if m.captchaSolver == nil || m.br.isOpen() {
				return errs.NewInteractionRequired("gateway requires a CAPTCHA")
			}
			done, err := m.autoSolveCaptcha(ctx, s)
			if err != nil {
				if code := errs.CodeOf(err); code == errs.InvalidCredentials || code == errs.MaybeAttacked {
					return err
				}
				// 轮次耗尽/求解器故障/网络错:回退人工挑战路径 —— 会话仍
				// 处于待解状态,ChallengeImage/SolveChallenge 照常可用。
				return errs.NewInteractionRequired("gateway requires a CAPTCHA")
			}
			if done {
				return nil
			}
			if round >= 1 {
				return errs.NewTemporarilyUnavailable("gateway captcha loop did not settle")
			}
			continue // 网关不再要码:重走一次普通登录
		case session.ECInvalidCredentials:
			return errs.NewInvalidCredentials()
		case session.ECNetworkError:
			return errs.NewTemporarilyUnavailable("gateway unreachable during login")
		case session.ECMaybeAttacked:
			return errs.NewMaybeAttacked()
		case session.ECRequestRejected:
			return errs.NewTemporarilyUnavailable("gateway rejected the login request (20026)")
		case session.ECNonAsciiCredential:
			return errs.NewNonAsciiCredential("credential")
		default:
			return errs.NewTemporarilyUnavailable("gateway login failed: " + string(out.Outcome))
		}
	}
}

// ChallengeImage returns the CAPTCHA image bound to the challenge.
func (m *Manager) ChallengeImage(ctx context.Context, challengeID string) ([]byte, error) {
	c, ok := m.liveChallenge(challengeID)
	if !ok {
		return nil, ErrChallengeUnknownOrExpired
	}
	captcha, err := c.s.EcCaptcha(ctx)
	if err != nil {
		return nil, err
	}
	if !captcha.Required || len(captcha.Image) == 0 {
		// Pollution cleared while waiting: no image to show.
		return nil, ErrChallengeUnknownOrExpired
	}
	// 记下图:人工手解的判卷沉淀(M3)需要图本身作为训练样本。
	m.mu.Lock()
	c.image = captcha.Image
	m.mu.Unlock()
	return captcha.Image, nil
}

// SolveChallenge submits the CAPTCHA code, completes the login and
// registers the session. A rejected code consumes the pending context; the
// client must refetch the image (the challenge stays until its TTL).
func (m *Manager) SolveChallenge(ctx context.Context, challengeID, code string) (*session.Session, error) {
	c, ok := m.liveChallenge(challengeID)
	if !ok {
		return nil, ErrChallengeUnknownOrExpired
	}
	// Serialize with logins of the same user, then re-admit: the challenge
	// may predate the group filling up (§7.5).
	v, err, _ := m.sf.Do("login:"+c.userID, func() (any, error) {
		if err := m.admit(c.userID, c.device); err != nil {
			return nil, err
		}
		return m.solveChallenge(ctx, c, code)
	})
	if err != nil {
		return nil, err
	}
	return v.(*session.Session), nil
}

func (m *Manager) solveChallenge(ctx context.Context, c *challenge, code string) (*session.Session, error) {
	// A CAPTCHA solve is a gateway login attempt: same global gate (§7.1).
	if err := m.gateEC(ctx); err != nil {
		return nil, err
	}
	status := c.s.ECLoginWithCaptcha(ctx, code)
	if status.Outcome != session.ECSuccess {
		switch status.Outcome {
		case session.ECCaptchaInvalid:
			m.noteCaptchaVerdict(c.image, code, false)
			return nil, errs.NewInteractionRequired("gateway rejected the CAPTCHA; fetch a new image")
		case session.ECCaptchaRequired:
			m.noteCaptchaVerdict(c.image, code, false)
			return nil, errs.NewInteractionRequired("gateway still requires a CAPTCHA")
		case session.ECInvalidCredentials:
			// 码被网关接受了(金标:学校亲判),错的是凭据 —— 照实透传真判。
			m.noteCaptchaVerdict(c.image, code, true)
			return nil, errs.NewInvalidCredentials()
		case session.ECMaybeAttacked:
			// Attack-suspicion throttle: no verdict on the code, nothing to record.
			return nil, errs.NewMaybeAttacked()
		case session.ECRequestRejected:
			return nil, errs.NewTemporarilyUnavailable("gateway rejected the captcha login request (20026)")
		default:
			return nil, fmt.Errorf("school: captcha login failed: %s", status.Outcome)
		}
	}
	m.noteCaptchaVerdict(c.image, code, true)
	if err := c.s.CompleteCaptchaLogin(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.challenges, c.id)
	m.register(c.userID, c.device, c.platform, c.s)
	m.mu.Unlock()
	return c.s, nil
}

// ChallengeParams are the login parameters held by a pending challenge.
// The API layer uses them to persist credentials after the solve.
type ChallengeParams struct {
	UserID   string
	Account  string
	Password string
	GwPass   string
	Device   string
	Platform string
}

// ChallengeParams returns the login parameters held by a challenge.
func (m *Manager) ChallengeParams(challengeID string) (*ChallengeParams, error) {
	c, ok := m.liveChallenge(challengeID)
	if !ok {
		return nil, ErrChallengeUnknownOrExpired
	}
	return &ChallengeParams{
		UserID:   c.userID,
		Account:  c.account,
		Password: c.password,
		GwPass:   c.gwPass,
		Device:   c.device,
		Platform: c.platform,
	}, nil
}

func (m *Manager) liveChallenge(id string) (*challenge, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	if !ok || m.now().After(c.expiresAt) {
		if ok {
			delete(m.challenges, id)
		}
		return nil, false
	}
	return c, true
}

// Session returns the live school session for a user (any device's entry).
// Most callers should use SessionFor to bind to the requesting device.
func (m *Manager) Session(userID string) (*session.Session, error) {
	return m.SessionFor(context.Background(), userID, "")
}

// Logout drops ALL of the user's session entries. It is local-only: core
// Logout never issues a network sign-out, which matters once sessions
// share gateway cookies (DESIGN.md §7.4).
func (m *Manager) Logout(userID string) {
	m.mu.Lock()
	g, ok := m.group[userID]
	delete(m.group, userID)
	m.mu.Unlock()
	if ok {
		for _, e := range g {
			e.s.Logout()
		}
	}
}

// AcademicSummary fetches (through the session's own recovery) the academic
// overview.
func (m *Manager) AcademicSummary(ctx context.Context, userID, deviceID string) (*jwc.AcademicInfo, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).AcademicInfo(ctx)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// Terms fetches the selectable terms.
func (m *Manager) Terms(ctx context.Context, userID, deviceID string) ([]jwc.TermItem, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).Terms(ctx)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// TermScores fetches one term's scores.
func (m *Manager) TermScores(ctx context.Context, userID, deviceID, termCode string) (*jwc.TermScoreResponse, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).TermScores(ctx, termCode)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// PlanCompletion fetches the plan-completion tree.
func (m *Manager) PlanCompletion(ctx context.Context, userID, deviceID string) (*jwc.PlanCompletionOutcome, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).PlanCompletion(ctx, nil)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// Exams fetches the aggregated exam view.
func (m *Manager) Exams(ctx context.Context, userID, deviceID string) (*jwc.ExamInfoResponse, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).ExamInfo(ctx)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// TrainingPlan fetches the training-plan summary.
func (m *Manager) TrainingPlan(ctx context.Context, userID, deviceID string) (*jwc.TrainingPlanInfo, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).TrainingPlanInfo(ctx)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// Timetable fetches the raw student schedule for one term.
func (m *Manager) Timetable(ctx context.Context, userID, deviceID, termCode string) (*jwc.StudentScheduleResponse, error) {
	s, err := m.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := jwc.New(s).StudentSchedule(ctx, termCode)
	if err != nil {
		m.markAuthFailed(userID, s, err)
	}
	return out, err
}

// newSessionAt builds a session on the DIRECT egress with the manager's
// posture (writes allowed; the API layer adds idempotency on top, M5).
func (m *Manager) newSessionAt(account, gwPass, password string) *session.Session {
	cfg := session.DefaultConfig()
	cfg.AllowWrites = m.writesAllowed
	return session.NewWithConfig(account, gwPass, password, m.newTransport(), cfg)
}

// wireRecovery routes the session's recoveries through the school: rerun
// the gateway + identity phases IN PLACE (the direct egress never moves,
// so the re-login simply replaces the TWFID on the same transport), with
// the session's own credentials — the core's default recovery shape.
// This is what keeps a wrong UAAP password harmless: it dies at the CAS
// layer after a successful EC login and never touches the gateway's
// per-IP failure quota (E-D 2026-10-21); the hook fires and the vault
// marks the credential.
func (m *Manager) wireRecovery(userID string, s *session.Session) {
	s.SetRecoveryFunc(func(ctx context.Context) error {
		if err := m.gateEC(ctx); err != nil {
			return err
		}
		if err := m.ecPhase(ctx, s); err != nil {
			return err
		}
		idErr := s.IdentityPhaseNoLock(ctx)
		if errs.CodeOf(idErr) == errs.InvalidCredentials {
			m.noteInvalidCred(ctx, userID)
		}
		return idErr
	})
}

// noteInvalidCred fires the invalid-credential hook (the server marks the
// vault credential + audits, so the silent relogin short-circuits and the
// frontend goes straight to the password prompt).
func (m *Manager) noteInvalidCred(ctx context.Context, userID string) {
	if m.onInvalidCred != nil {
		m.onInvalidCred(ctx, userID)
	}
}

// ReloginVaulted silently rebuilds a session from vaulted credentials
// (the authenticated relogin path, §7.2 step 6). It NEVER touches the
// §5.2 cooling ladder — login() wraps loginInner with the ladder; this
// calls loginInner directly — because the vaulted credential is the
// user's own last-typed password, not a brute-force vector: after a
// school-side password change our stale vault must not lock the user out
// of their OWN next login. A stale password dies at the CAS layer with
// the gateway quota untouched (bounded: the hook marks the credential
// invalid on the first rejection, and every later attempt short-circuits).
func (m *Manager) ReloginVaulted(ctx context.Context, userID, device, account, password, gwPass string) (*session.Session, error) {
	v, err, _ := m.sf.Do("login:"+userID, func() (any, error) {
		return m.loginInner(ctx, userID, account, password, gwPass, device, "")
	})
	if err != nil {
		return nil, err
	}
	return v.(*session.Session), nil
}

func newChallengeID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b[:])
}
