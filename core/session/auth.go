package session

// Gateway (EC) and identity (UAAP/CAS) authentication.
//
// Ports desktop/lib/services/aufe/connector.dart and the iOS AUFEConnection
// behaviour. The iOS client is authoritative for session recovery; the
// desktop client supplies the EC RSA request shape.

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/crypto"
	"github.com/Sibuxiangx/loveace-go/core/detector"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
)

// ECLoginOutcome is the outcome of an EC gateway login attempt.
type ECLoginOutcome string

// EC login outcomes (Rust serde snake_case spellings).
const (
	ECSuccess            ECLoginOutcome = "success"
	ECInvalidCredentials ECLoginOutcome = "invalid_credentials"
	ECMaybeAttacked      ECLoginOutcome = "maybe_attacked"
	ECCaptchaRequired    ECLoginOutcome = "captcha_required"
	ECCaptchaInvalid     ECLoginOutcome = "captcha_invalid"
	ECNonAsciiCredential ECLoginOutcome = "non_ascii_credential"
	ECNotFoundTwfid      ECLoginOutcome = "not_found_twfid"
	ECNotFoundRsaKey     ECLoginOutcome = "not_found_rsa_key"
	ECNotFoundRsaExp     ECLoginOutcome = "not_found_rsa_exp"
	ECNotFoundCsrfCode   ECLoginOutcome = "not_found_csrf_code"
	ECNetworkError       ECLoginOutcome = "network_error"
	ECRequestRejected    ECLoginOutcome = "request_rejected"
	ECUnknownError       ECLoginOutcome = "unknown_error"
)

// ECLoginStatus is the EC login status wrapper.
type ECLoginStatus struct {
	Outcome ECLoginOutcome `json:"outcome"`
}

// Success reports whether login succeeded.
func (s ECLoginStatus) Success() bool { return s.Outcome == ECSuccess }

// ECAuthContext is the parsed EC auth-parameter page state, reusable across
// CAPTCHA attempts.
type ECAuthContext struct {
	TWFID  string `json:"twfid"`
	CSRF   string `json:"csrf"`
	RSAKey string `json:"rsa_key"`
	RSAExp string `json:"rsa_exp"`
	RndImg bool   `json:"rnd_img"`
}

// ECCaptcha is the EC CAPTCHA state returned by EcCaptcha.
type ECCaptcha struct {
	Required bool   `json:"required"`
	Image    []byte `json:"image,omitempty"`
	Mime     string `json:"mime"`
}

// UAAPLoginOutcome is the outcome of a UAAP/CAS identity login attempt.
type UAAPLoginOutcome string

// UAAP login outcomes.
const (
	UAAPSuccess                UAAPLoginOutcome = "success"
	UAAPInvalidCredentials     UAAPLoginOutcome = "invalid_credentials"
	UAAPPasswordChangeRequired UAAPLoginOutcome = "password_change_required"
	UAAPNotFoundLt             UAAPLoginOutcome = "not_found_lt"
	UAAPNotFoundExecution      UAAPLoginOutcome = "not_found_execution"
	UAAPNonAsciiCredential     UAAPLoginOutcome = "non_ascii_credential"
	UAAPNetworkError           UAAPLoginOutcome = "network_error"
	UAAPUnknownError           UAAPLoginOutcome = "unknown_error"
)

// UAAPLoginStatus is the UAAP login status wrapper.
type UAAPLoginStatus struct {
	Outcome UAAPLoginOutcome `json:"outcome"`
}

// Success reports whether login succeeded.
func (s UAAPLoginStatus) Success() bool { return s.Outcome == UAAPSuccess }

// HeartbeatResult is the heartbeat probe result. Alive only proves the
// gateway responds; it is never proof that a subsequent business request is
// authenticated.
type HeartbeatResult string

// Heartbeat outcomes.
const (
	HeartbeatAlive       HeartbeatResult = "alive"
	HeartbeatExpired     HeartbeatResult = "expired"
	HeartbeatUnavailable HeartbeatResult = "unavailable"
)

// ECLoginCheckStatus is the combined EC/UAAP check status.
type ECLoginCheckStatus struct {
	LoggedIn     bool `json:"logged_in"`
	NetworkError bool `json:"network_error"`
}

// ValidateCredentials validates that credential fields are pure ASCII.
//
// Full-width/Chinese characters (common when an IME is left in Chinese mode)
// make authentication fail opaquely, so they are rejected before any request.
// The offending value is never included in the error.
func ValidateCredentials(userID, gatewayPassword, password string) error {
	for field, value := range map[string]string{
		"user_id":          userID,
		"gateway_password": gatewayPassword,
		"password":         password,
	} {
		if !isASCII(value) {
			// Deterministic field order for tests: check explicitly.
			_ = field
		}
	}
	if !isASCII(userID) {
		return errs.NewNonAsciiCredential("user_id")
	}
	if !isASCII(gatewayPassword) {
		return errs.NewNonAsciiCredential("gateway_password")
	}
	if !isASCII(password) {
		return errs.NewNonAsciiCredential("password")
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// ValidateCredentials checks this session's credential fields.
func (s *Session) ValidateCredentials() error {
	return ValidateCredentials(s.userID, s.ecPassword, s.password)
}

// Login performs the full login: gateway then identity. Idempotent per call.
func (s *Session) Login(caller context.Context) error {
	ctx, release := s.operationContext(caller)
	defer release()
	// Acquire the auth semaphore (single-flight login).
	select {
	case s.authMu <- struct{}{}:
		defer func() { <-s.authMu }()
	case <-ctx.Done():
		return errs.NewCancelled()
	}
	if s.State() == StateAuthenticated {
		return nil
	}
	return s.authenticate(ctx)
}

// authenticate is the core login sequence. Callers hold authMu.
//
// Every await is followed by a cancellation and generation check so a
// concurrent Logout always wins: a login that completes after sign-out must
// not publish authenticated or repopulate the jar/generation.
func (s *Session) authenticate(ctx context.Context) error {
	// Reject non-ASCII credentials before any network or state change.
	if err := s.ValidateCredentials(); err != nil {
		s.setState(StateSignedOut)
		s.Emit(events.KindLogin, "login", "non_ascii_credential", 0)
		return err
	}
	epoch := s.Generation()
	startedMS := nowMillis()
	s.setState(StateAuthenticatingGateway)

	ec := s.ECLogin(ctx)
	if s.cancelled() || s.Generation() != epoch {
		return errs.NewCancelled()
	}
	gatewayMS := nowMillis() - startedMS

	switch ec.Outcome {
	case ECSuccess:
		// continue to identity
	case ECNetworkError:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindGatewayAuth, "login", "unavailable", gatewayMS)
		return errs.NewTemporarilyUnavailable("gateway unreachable during login")
	case ECInvalidCredentials:
		s.signOutInternal()
		s.Emit(events.KindGatewayAuth, "login", "bad_credentials", gatewayMS)
		return errs.NewInvalidCredentials()
	case ECMaybeAttacked:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindGatewayAuth, "login", "throttled", gatewayMS)
		return errs.NewMaybeAttacked()
	case ECRequestRejected:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindGatewayAuth, "login", "request_rejected", gatewayMS)
		return errs.NewTemporarilyUnavailable("gateway rejected the login request (20026)")
	case ECCaptchaRequired, ECCaptchaInvalid:
		invalid := ec.Outcome == ECCaptchaInvalid
		s.setState(StateInteractionRequired)
		outcome := "captcha_required"
		msg := "gateway requires a CAPTCHA"
		if invalid {
			outcome = "captcha_invalid"
			msg = "gateway CAPTCHA was rejected"
		}
		s.Emit(events.KindGatewayAuth, "login", outcome, gatewayMS)
		return errs.NewInteractionRequired(msg)
	case ECNonAsciiCredential:
		s.setState(StateSignedOut)
		s.Emit(events.KindGatewayAuth, "login", "non_ascii_credential", gatewayMS)
		return errs.NewNonAsciiCredential("credential")
	default:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindGatewayAuth, "login", "failed", gatewayMS)
		return errs.NewTemporarilyUnavailable("gateway login failed: " + string(ec.Outcome))
	}

	identityStarted := nowMillis()
	s.setState(StateAuthenticatingIdentity)
	uaap := s.UAAPLogin(ctx)
	if s.cancelled() || s.Generation() != epoch {
		return errs.NewCancelled()
	}
	identityMS := nowMillis() - identityStarted
	switch uaap.Outcome {
	case UAAPSuccess:
	case UAAPNetworkError:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindIdentityAuth, "login", "unavailable", identityMS)
		return errs.NewTemporarilyUnavailable("identity provider unreachable during login")
	case UAAPInvalidCredentials:
		s.signOutInternal()
		s.Emit(events.KindIdentityAuth, "login", "bad_credentials", identityMS)
		return errs.NewInvalidCredentials()
	case UAAPNonAsciiCredential:
		s.setState(StateSignedOut)
		s.Emit(events.KindIdentityAuth, "login", "non_ascii_credential", identityMS)
		return errs.NewNonAsciiCredential("credential")
	case UAAPPasswordChangeRequired:
		// The credentials are CORRECT — the school intercepts with its
		// mandatory password-change flow. Do NOT sign out (the cookies
		// stay) and do NOT mark the vault invalid: the user resolves it
		// through the school's portal, then retries.
		s.Emit(events.KindIdentityAuth, "login", "password_change_required", identityMS)
		return errs.NewPasswordChangeRequired()
	default:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindIdentityAuth, "login", "failed", identityMS)
		return errs.NewTemporarilyUnavailable("identity login failed: " + string(uaap.Outcome))
	}

	// Guard again before publishing success.
	if s.cancelled() || s.Generation() != epoch {
		return errs.NewCancelled()
	}
	s.setState(StateAuthenticated)
	s.bumpGeneration()
	s.Emit(events.KindLogin, "login", "success", nowMillis()-startedMS)
	return nil
}

// EnsureAuthenticated ensures the session is authenticated before dispatch
// (pre-dispatch recovery is permitted for both reads and writes).
func (s *Session) EnsureAuthenticated(caller context.Context) error {
	ctx, release := s.operationContext(caller)
	defer release()
	if s.cancelled() {
		return errs.NewCancelled()
	}
	switch s.State() {
	case StateAuthenticated:
		return nil
	case StateTemporarilyUnavailable, StateSignedOut:
		return s.Login(ctx)
	case StateAuthenticatingGateway, StateAuthenticatingIdentity, StateReconnecting:
		// Another task owns authentication; wait for the auth lock then
		// re-check rather than starting a second login.
		select {
		case s.authMu <- struct{}{}:
			defer func() { <-s.authMu }()
		case <-ctx.Done():
			return errs.NewCancelled()
		}
		if s.State() == StateAuthenticated {
			return nil
		}
		return s.authenticate(ctx)
	default: // InteractionRequired
		return errs.NewInteractionRequired("gateway requires user interaction (captcha)")
	}
}

// ECLogin performs the EC gateway login (RSA-encrypted password).
//
// If the auth page indicates a CAPTCHA (<RndImg>1</RndImg>) this returns
// ECCaptchaRequired without submitting credentials; the caller must fetch the
// image with EcCaptcha and retry with ECLoginWithCaptcha.
func (s *Session) ECLogin(caller context.Context) ECLoginStatus {
	ctx, release := s.operationContext(caller)
	defer release()
	if s.ValidateCredentials() != nil {
		return ECLoginStatus{Outcome: ECNonAsciiCredential}
	}
	res := s.ecFetchAuthContext(ctx)
	if res.err != nil {
		return ECLoginStatus{Outcome: ecContextOutcome(res.err)}
	}
	if res.ctx.RndImg {
		return ECLoginStatus{Outcome: ECCaptchaRequired}
	}
	status, err := s.ecSubmitLogin(ctx, &res.ctx, nil)
	if err != nil {
		if isNetwork(err) {
			return ECLoginStatus{Outcome: ECNetworkError}
		}
		return ECLoginStatus{Outcome: ECUnknownError}
	}
	return *status
}

type ecContextResult struct {
	ctx ECAuthContext
	err error
}

func (r ecContextResult) errIsNil() bool { return r.err == nil }

// ECFetchAuthContext fetches and parses the EC auth-parameter page.
func (s *Session) ECFetchAuthContext(caller context.Context) (*ECAuthContext, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	res := s.ecFetchAuthContext(ctx)
	return &res.ctx, res.err
}

func (s *Session) ecFetchAuthContext(ctx context.Context) ecContextResult {
	authURL := config.JoinURL(s.cfg.VPNServer, config.ECLoginAuth)
	response, err := s.RawGet(ctx, authURL, nil, 0)
	if err != nil {
		return ecContextResult{err: err}
	}
	body := response.BodyText()

	twfid, ok := extractBetween(body, "TwfID")
	if !ok || twfid == "" {
		return ecContextResult{err: errs.NewNotFound("TwfID")}
	}
	rsaKey, ok := extractBetween(body, "RSA_ENCRYPT_KEY")
	if !ok || rsaKey == "" {
		return ecContextResult{err: errs.NewNotFound("RSA_ENCRYPT_KEY")}
	}
	rsaExp, ok := extractBetween(body, "RSA_ENCRYPT_EXP")
	if !ok || rsaExp == "" {
		return ecContextResult{err: errs.NewNotFound("RSA_ENCRYPT_EXP")}
	}
	csrf, ok := extractBetween(body, "CSRF_RAND_CODE")
	if !ok || csrf == "" {
		return ecContextResult{err: errs.NewNotFound("CSRF_RAND_CODE")}
	}
	rndImg := false
	if v, ok := extractBetween(body, "RndImg"); ok {
		rndImg = strings.TrimSpace(v) == "1"
	}
	return ecContextResult{ctx: ECAuthContext{
		TWFID:  twfid,
		CSRF:   csrf,
		RSAKey: rsaKey,
		RSAExp: rsaExp,
		RndImg: rndImg,
	}}
}

// ECFetchCaptchaImage fetches the CAPTCHA image bound to ctx (same TWFID cookie).
func (s *Session) ECFetchCaptchaImage(caller context.Context, ctx *ECAuthContext) ([]byte, error) {
	url := config.JoinURL(s.cfg.VPNServer, config.ECRandCode)
	cookie := "TWFID=" + ctx.TWFID
	response, err := s.RawGet(caller, url, [][2]string{{"Cookie", cookie}}, 0)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (s *Session) ecSubmitLogin(ctx context.Context, ecCtx *ECAuthContext, captcha *string) (*ECLoginStatus, error) {
	plaintext := s.ecPassword + "_" + ecCtx.CSRF
	encrypted, err := crypto.RSAEncryptPKCS1v15(plaintext, ecCtx.RSAKey, ecCtx.RSAExp)
	if err != nil {
		return nil, err
	}

	loginURL := config.JoinURL(s.cfg.VPNServer, config.ECLoginPsw)
	captchaValue := ""
	if captcha != nil {
		captchaValue = *captcha
	}
	form := [][2]string{
		{"svpn_rand_code", captchaValue},
		{"mitm", ""},
		{"svpn_req_randcode", ecCtx.CSRF},
		{"svpn_name", s.userID},
		{"svpn_password", encrypted},
	}
	cookie := "TWFID=" + ecCtx.TWFID
	login, err := s.RawPost(ctx, loginURL, form, [][2]string{{"Cookie", cookie}}, 0)
	if err != nil {
		return nil, err
	}
	loginBody := login.BodyText()

	if strings.Contains(loginBody, "<Result>1</Result>") {
		s.setTWFID(ecCtx.TWFID)
		s.jar.Set("TWFID", ecCtx.TWFID, ".vpn2.aufe.edu.cn", "/")
		s.ecLogged.Store(true)
		return &ECLoginStatus{Outcome: ECSuccess}, nil
	}

	captchaChallenge := strings.Contains(loginBody, "20023") ||
		strings.Contains(loginBody, "CAPTCHA required") ||
		strings.Contains(loginBody, "characters are incorrect")
	// 20041 is the gateway's attack-suspicion throttle. It refuses the attempt
	// without judging the code or the credentials, so it is never a captcha
	// verdict: callers must not record it as a rejected code.
	maybeAttacked := strings.Contains(loginBody, "20041") ||
		strings.Contains(loginBody, "maybe attacked") ||
		strings.Contains(loginBody, "[CDATA[maybe attacked]]")
	if maybeAttacked {
		return &ECLoginStatus{Outcome: ECMaybeAttacked}, nil
	}
	if captchaChallenge {
		outcome := ECCaptchaRequired
		if captcha != nil {
			outcome = ECCaptchaInvalid
		}
		return &ECLoginStatus{Outcome: outcome}, nil
	}

	if strings.Contains(loginBody, "Invalid username or password!") || strings.Contains(loginBody, "20004") {
		return &ECLoginStatus{Outcome: ECInvalidCredentials}, nil
	}
	// 20026: the gateway rejected the request shape (CSRF mismatch or a
	// malformed submission). Neither the code nor the credentials were judged,
	// so it is kept apart from unknown_error as a protocol break.
	if strings.Contains(loginBody, "20026") {
		return &ECLoginStatus{Outcome: ECRequestRejected}, nil
	}
	return &ECLoginStatus{Outcome: ECUnknownError}, nil
}

// UAAPLogin performs the UAAP/CAS identity login (3DES-encrypted password).
func (s *Session) UAAPLogin(caller context.Context) UAAPLoginStatus {
	ctx, release := s.operationContext(caller)
	defer release()
	if s.ValidateCredentials() != nil {
		return UAAPLoginStatus{Outcome: UAAPNonAsciiCredential}
	}
	status, err := s.performUAAPLogin(ctx)
	if err != nil {
		if isNetwork(err) {
			return UAAPLoginStatus{Outcome: UAAPNetworkError}
		}
		return UAAPLoginStatus{Outcome: UAAPUnknownError}
	}
	return *status
}

func (s *Session) performUAAPLogin(ctx context.Context) (*UAAPLoginStatus, error) {
	// Single hop: with a still-valid gateway/CASTGC session, CAS redirects
	// straight to the service with a ticket instead of returning the login
	// form. Following that redirect blindly loses the ticket and yields a
	// spurious NotFoundLt; inspect the redirect instead.
	response, err := s.RawGetOnce(ctx, config.UAAPLoginURL, nil, 0)
	if err != nil {
		return nil, err
	}
	location, hasLocation := response.Header("location")
	var ticketLocation string
	isRedirectStatus := response.Status == 301 || response.Status == 302 ||
		response.Status == 303 || response.Status == 307 || response.Status == 308
	// The school's mandatory password-change interception: the CAS has
	// accepted the credentials (it even mints a TGT — CTgtId rides the
	// redirect) but issues NO ticket until the password is changed through
	// the school's own portal. Nothing we can submit resolves it.
	if isRedirectStatus && hasLocation && strings.Contains(location, "/mng/acp/completeInfo/password") {
		return &UAAPLoginStatus{Outcome: UAAPPasswordChangeRequired}, nil
	}
	if isRedirectStatus && hasLocation && strings.Contains(location, "ticket=") {
		ticketLocation = resolveRelative(config.UAAPLoginURL, location)
	}
	if ticketLocation != "" {
		// Already authenticated: UAAP success without submitting credentials.
		s.uaapLogged.Store(true)
		s.startServiceCallback(ticketLocation)
		return &UAAPLoginStatus{Outcome: UAAPSuccess}, nil
	}

	body := response.BodyText()
	lt, ok := extractInputValue(body, "lt")
	if !ok || lt == "" {
		return &UAAPLoginStatus{Outcome: UAAPNotFoundLt}, nil
	}
	execution, ok := extractInputValue(body, "execution")
	if !ok || execution == "" {
		return &UAAPLoginStatus{Outcome: UAAPNotFoundExecution}, nil
	}

	encrypted, err := crypto.DESEde3ECBPKCS7Base64(s.password, lt)
	if err != nil {
		return nil, err
	}
	postURL := config.UAAPLoginURL
	if action, ok := extractFormAction(body); ok {
		postURL = resolveRelative(config.UAAPLoginURL, action)
	}

	form := [][2]string{
		{"username", s.userID},
		{"password", encrypted},
		{"lt", lt},
		{"execution", execution},
		{"_eventId", "submit"},
		{"submit", "LOGIN"},
	}
	// Exactly one hop: CAS either answers with a service-ticket redirect, or
	// (session already present) the service page directly.
	login, err := s.RawPostOnce(ctx, postURL, form, nil, 0)
	if err != nil {
		return nil, err
	}
	loginBody := login.BodyText()
	responseURL := login.FinalURL
	location, hasLocation = login.Header("location")

	// The mandatory password-change interception (the bare URL and the
	// ?service=...&CTgtId=... form both match the path marker).
	if (hasLocation && strings.Contains(location, "/mng/acp/completeInfo/password")) ||
		strings.Contains(responseURL, "/mng/acp/completeInfo/password") {
		return &UAAPLoginStatus{Outcome: UAAPPasswordChangeRequired}, nil
	}

	// Invalid-credentials detection is unchanged.
	// 2026-10-21 实测:本校 CAS 皮肤的错密码文案是
	// 「密码输入错误，您还有"N"次输入机会。」(带账号级剩余次数计数,约 5 次),
	// 不含 "Invalid username or password" 也不含 errorMsg —— 必须单独匹配,
	// 否则错密码登录会落进 UAAPUnknownError(temporarily_unavailable)。
	if strings.Contains(loginBody, "Invalid username or password") ||
		strings.Contains(loginBody, "用户名或密码错误") ||
		strings.Contains(loginBody, "密码输入错误") ||
		strings.Contains(loginBody, "errorMsg") {
		return &UAAPLoginStatus{Outcome: UAAPInvalidCredentials}, nil
	}

	// Success = the CAS/UAAP service accepted the authentication and issued
	// a ticket (redirect with ticket=), or the CAS session already existed
	// and the response landed on the service.
	redirectStatus := login.Status == 301 || login.Status == 302 ||
		login.Status == 303 || login.Status == 307 || login.Status == 308
	ticketLocation = ""
	if redirectStatus && hasLocation && strings.Contains(location, "ticket=") {
		ticketLocation = resolveRelative(postURL, location)
	}
	landedOnService := login.Status == 200 && strings.HasPrefix(responseURL, "http://jwcxk2")
	ticketInURL := login.Status == 200 && strings.Contains(responseURL, "ticket=")

	if ticketLocation == "" && !landedOnService && !ticketInURL {
		return &UAAPLoginStatus{Outcome: UAAPUnknownError}, nil
	}

	// UAAP/CAS authentication is the login success criterion. JWC is a
	// service-scoped dependency and must not gate login.
	s.uaapLogged.Store(true)
	callbackURL := config.UAAPCheckURL
	if ticketLocation != "" {
		callbackURL = ticketLocation
	}
	s.startServiceCallback(callbackURL)
	return &UAAPLoginStatus{Outcome: UAAPSuccess}, nil
}

// CheckECLoginStatus checks the EC gateway login state.
func (s *Session) CheckECLoginStatus(caller context.Context) ECLoginCheckStatus {
	if !s.ecLogged.Load() {
		return ECLoginCheckStatus{}
	}
	response, err := s.RawGet(caller, config.ECCheckURL, nil, 0)
	if err != nil {
		return ECLoginCheckStatus{NetworkError: isNetwork(err)}
	}
	return ECLoginCheckStatus{LoggedIn: response.Status == 200}
}

// CheckUAAPLoginStatus checks the UAAP identity login state.
func (s *Session) CheckUAAPLoginStatus() ECLoginCheckStatus {
	return ECLoginCheckStatus{LoggedIn: s.uaapLogged.Load()}
}

// ExpireTicketCookies invalidates only the service-local ticket cookies,
// preserving gateway login. Used by the debug expire_service_ticket control.
func (s *Session) ExpireTicketCookies() { s.jar.Clear(cookies.Service) }

// Heartbeat probes gateway liveness THROUGH the proxy, fingerprint-based
// (never status-based). A live TWFID is proxied through to the school's
// own pages — including the CAS's own login form (the UAAP layer never
// demands an EC login). A dead TWFID gets the EC login demand: a redirect
// to the BARE portal host (vpn2.aufe.edu.cn / vpn.aufe.edu.cn) or one of
// the gateway's interception page fingerprints (the portal / the
// route-selector / the auth XML / the legacy login page). Gateway
// pollution (the login-layer captcha) does not intercept existing
// sessions, so this stays a pure session-liveness signal.
func (s *Session) Heartbeat(caller context.Context) HeartbeatResult {
	ctx, release := s.operationContext(caller)
	defer release()
	current := config.UAAPBase + "/"
	for i := 0; i < 5; i++ {
		response, err := s.RawGetOnce(ctx, current, nil, 0)
		if err != nil {
			return HeartbeatUnavailable
		}
		location, hasLocation := response.Header("location")
		if response.IsRedirect() && hasLocation {
			next := resolveLocation(current, location)
			host := ""
			if parsed, perr := url.Parse(next); perr == nil {
				host = strings.ToLower(parsed.Hostname())
			}
			if host == "vpn2.aufe.edu.cn" || host == "vpn.aufe.edu.cn" {
				return HeartbeatExpired // the EC login demand
			}
			current = next
			continue
		}
		// Final page: any gateway fingerprint (portal, route selector,
		// auth XML, legacy login) means interception; the CAS's own
		// signals (cas_login_*) are the school's page — alive.
		d := detector.Detector{}.Inspect(response.FinalURL, response.Body, location)
		for _, sig := range d.Signals {
			if sig == "cas_login_url" || sig == "cas_login_form" {
				continue
			}
			return HeartbeatExpired
		}
		var det detector.Detector
		if det.IsLegacyLoginPage(current, response.Body) {
			return HeartbeatExpired
		}
		return HeartbeatAlive
	}
	return HeartbeatAlive
}

// EcCaptcha fetches the EC CAPTCHA state.
//
// When the gateway reports <RndImg>1</RndImg>, stores the bound auth context
// for ECLoginWithCaptcha and returns the CAPTCHA image (image/gif). Otherwise
// required=false and no image.
func (s *Session) EcCaptcha(caller context.Context) (*ECCaptcha, error) {
	ctx, release := s.operationContext(caller)
	defer release()
	res := s.ecFetchAuthContext(ctx)
	if res.err != nil {
		return nil, res.err
	}
	if res.ctx.RndImg {
		image, err := s.ECFetchCaptchaImage(ctx, &res.ctx)
		if err != nil {
			return nil, err
		}
		s.pendingEC.Store("pending", &res.ctx)
		return &ECCaptcha{Required: true, Image: image, Mime: "image/gif"}, nil
	}
	s.pendingEC.Delete("pending")
	return &ECCaptcha{Required: false, Mime: "image/gif"}, nil
}

// ECLoginWithCaptcha submits the EC CAPTCHA code, reusing the pending auth
// context if present.
//
// The context is consumed on completion: a rejected CAPTCHA requires a fresh
// EcCaptcha call (new image/new context).
func (s *Session) ECLoginWithCaptcha(caller context.Context, code string) ECLoginStatus {
	ctx, release := s.operationContext(caller)
	defer release()
	var ecCtx *ECAuthContext
	if v, ok := s.pendingEC.Load("pending"); ok {
		ecCtx = v.(*ECAuthContext)
	}
	if ecCtx == nil {
		res := s.ecFetchAuthContext(ctx)
		if res.err != nil {
			return ECLoginStatus{Outcome: ecContextOutcome(res.err)}
		}
		ecCtx = &res.ctx
	}
	status, err := s.ecSubmitLogin(ctx, ecCtx, &code)
	s.pendingEC.Delete("pending")
	if err != nil {
		if isNetwork(err) {
			return ECLoginStatus{Outcome: ECNetworkError}
		}
		return ECLoginStatus{Outcome: ECUnknownError}
	}
	return *status
}

// CompleteCaptchaLogin finishes a login whose EC step required a CAPTCHA.
// Call it after ECLoginWithCaptcha returned ECSuccess: it runs the identity
// (UAAP) step and publishes the authenticated state exactly as
// authenticate() would have. Safe under concurrency (single-flight via
// authMu); no-op if already authenticated.
func (s *Session) CompleteCaptchaLogin(caller context.Context) error {
	ctx, release := s.operationContext(caller)
	defer release()
	select {
	case s.authMu <- struct{}{}:
		defer func() { <-s.authMu }()
	case <-ctx.Done():
		return errs.NewCancelled()
	}
	return s.identityPhase(ctx)
}

// IdentityPhaseNoLock runs the UAAP identity step and publishes the
// authenticated state WITHOUT acquiring the auth semaphore. For recovery
// implementations (the server's pool-deriving recoveryFn) which run
// inside recover() — the semaphore is already held there; taking it
// again deadlocks.
func (s *Session) IdentityPhaseNoLock(caller context.Context) error {
	ctx, release := s.operationContext(caller)
	defer release()
	return s.identityPhase(ctx)
}

func (s *Session) identityPhase(ctx context.Context) error {
	if s.State() == StateAuthenticated {
		return nil
	}
	if s.cancelled() {
		return errs.NewCancelled()
	}
	s.setState(StateAuthenticatingIdentity)
	uaap := s.UAAPLogin(ctx)
	if s.cancelled() {
		return errs.NewCancelled()
	}
	switch uaap.Outcome {
	case UAAPSuccess:
	case UAAPNetworkError:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindIdentityAuth, "login", "unavailable", 0)
		return errs.NewTemporarilyUnavailable("identity provider unreachable during login")
	case UAAPInvalidCredentials:
		s.signOutInternal()
		s.Emit(events.KindIdentityAuth, "login", "bad_credentials", 0)
		return errs.NewInvalidCredentials()
	case UAAPPasswordChangeRequired:
		s.Emit(events.KindIdentityAuth, "login", "password_change_required", 0)
		return errs.NewPasswordChangeRequired()
	default:
		s.setState(StateTemporarilyUnavailable)
		s.Emit(events.KindIdentityAuth, "login", "failed", 0)
		return errs.NewTemporarilyUnavailable("identity login failed: " + string(uaap.Outcome))
	}
	if s.cancelled() {
		return errs.NewCancelled()
	}
	s.setState(StateAuthenticated)
	s.bumpGeneration()
	s.Emit(events.KindLogin, "login", "success", 0)
	return nil
}

// --- shared helpers ---

func ecContextOutcome(err error) ECLoginOutcome {
	var e *errs.Error
	if asErr(err, &e) {
		switch {
		case e.Code == errs.NotFound && e.Msg == "TwfID":
			return ECNotFoundTwfid
		case e.Code == errs.NotFound && e.Msg == "RSA_ENCRYPT_KEY":
			return ECNotFoundRsaKey
		case e.Code == errs.NotFound && e.Msg == "RSA_ENCRYPT_EXP":
			return ECNotFoundRsaExp
		case e.Code == errs.NotFound && e.Msg == "CSRF_RAND_CODE":
			return ECNotFoundCsrfCode
		case isNetwork(err):
			return ECNetworkError
		}
	}
	if isNetwork(err) {
		return ECNetworkError
	}
	return ECUnknownError
}

func isNetwork(err error) bool {
	code := errs.CodeOf(err)
	return code == errs.Network || code == errs.Timeout
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
		return true
	}
	return false
}

// ExtractBetween extracts the content between <tag> and </tag>
// (case-sensitive, first hit).
func ExtractBetween(text, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	start += len(open)
	rest := text[start:]
	end := strings.Index(rest, close)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

func extractBetween(text, tag string) (string, bool) { return ExtractBetween(text, tag) }

var (
	valueRe      = regexp.MustCompile(`(?is)\bvalue=["']([^"']*)["']`)
	formActionRe = regexp.MustCompile(`(?is)<form\b[^>]*\baction=["']([^"']+)["']`)
)

// ExtractInputValue extracts the value attribute of an <input name="name" ...>
// element.
func ExtractInputValue(html, name string) (string, bool) {
	// regexp has no literal interpolation; build the pattern per call but the
	// hot paths (login) run a handful of times per session.
	pattern := `(?is)<input[^>]*name=["']` + regexp.QuoteMeta(name) + `["'][^>]*>`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	tag := re.FindString(html)
	if tag == "" {
		return "", false
	}
	if m := valueRe.FindStringSubmatch(tag); m != nil {
		return m[1], true
	}
	return "", false
}

func extractInputValue(html, name string) (string, bool) { return ExtractInputValue(html, name) }

// ExtractFormAction extracts a <form action="..."> value.
func ExtractFormAction(html string) (string, bool) {
	if m := formActionRe.FindStringSubmatch(html); m != nil {
		return m[1], true
	}
	return "", false
}

func extractFormAction(html string) (string, bool) { return ExtractFormAction(html) }

func resolveRelative(base, path string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return path
	}
	return baseURL.ResolveReference(ref).String()
}
