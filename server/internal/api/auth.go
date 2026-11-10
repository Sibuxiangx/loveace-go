package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/server/internal/dto"
	"github.com/Sibuxiangx/loveace-go/server/internal/gate"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// registerAuth wires the auth endpoints.
func (s *Server) registerAuth(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-session",
		Summary:     "Log in with school credentials",
		Method:      http.MethodPost,
		Path:        "/v1/auth/sessions",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, in *LoginInput) (*Output[dto.TokenPair], error) {
		pair, err := s.login(ctx, in)
		if err != nil {
			return nil, err
		}
		return &Output[dto.TokenPair]{Status: http.StatusOK, Body: *pair}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "refresh-tokens",
		Summary:     "Rotate the refresh token",
		Method:      http.MethodPost,
		Path:        "/v1/auth/tokens/refresh",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, in *RefreshInput) (*Output[dto.TokenPair], error) {
		pair, err := s.refresh(ctx, in)
		if err != nil {
			return nil, err
		}
		return &Output[dto.TokenPair]{Status: http.StatusOK, Body: *pair}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "logout-current-device",
		Summary:     "Log out the current device",
		Method:      http.MethodDelete,
		Path:        "/v1/auth/sessions/current",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[struct{}], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.revokeDevice(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-challenge-image",
		Summary:     "Fetch the CAPTCHA image for a challenge",
		Method:      http.MethodGet,
		Path:        "/v1/auth/challenges/{id}/image",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, in *ChallengeIDInput) (*ImageOutput, error) {
		image, err := s.deps.School.ChallengeImage(ctx, in.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &ImageOutput{
			Status:      http.StatusOK,
			ContentType: "image/gif",
			Body:        image,
		}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "solve-challenge",
		Summary:     "Submit a CAPTCHA code and finish the login",
		Method:      http.MethodPost,
		Path:        "/v1/auth/challenges/{id}/solution",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, in *ChallengeSolveInput) (*Output[dto.TokenPair], error) {
		pair, err := s.solveChallenge(ctx, in)
		if err != nil {
			return nil, err
		}
		return &Output[dto.TokenPair]{Status: http.StatusOK, Body: *pair}, nil
	})
}

// --- inputs / outputs ---

type LoginInput struct {
	Body LoginBody
}

type LoginBody struct {
	Account         string `json:"account" required:"false" doc:"学号"`
	Password        string `json:"password" required:"false" doc:"学校密码（半角）"`
	GatewayPassword string `json:"gateway_password" required:"false" doc:"网关密码；留空表示与学校密码相同"`
	DeviceName      string `json:"device_name" required:"false" doc:"设备名"`
	Platform        string `json:"platform" required:"false" doc:"客户端平台"`
}

type RefreshInput struct {
	Body RefreshBody
}

type RefreshBody struct {
	RefreshToken string `json:"refresh_token" required:"false"`
}

type AuthHeaderInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
}

type ChallengeIDInput struct {
	ID string `path:"id" required:"false"`
}

type ChallengeSolveInput struct {
	ID   string `path:"id" required:"false"`
	Body ChallengeSolveBody
}

type ChallengeSolveBody struct {
	Code string `json:"code" required:"false" doc:"验证码"`
}

type ImageOutput struct {
	Status      int
	ContentType string `header:"Content-Type"`
	Body        []byte `type:"string" format:"binary"`
}

// --- rejection fingerprint cache (DESIGN.md §5.2 step 2) ---

type rejectionCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]time.Time
}

func newRejectionCache(ttl time.Duration, now func() time.Time) *rejectionCache {
	return &rejectionCache{ttl: ttl, now: now, entries: map[string]time.Time{}}
}

func (r *rejectionCache) mark(fp string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[fp] = r.now().Add(r.ttl)
}

func (r *rejectionCache) rejected(fp string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.entries[fp]
	return ok && r.now().Before(until)
}

// --- handlers ---

func (s *Server) login(ctx context.Context, in *LoginInput) (*dto.TokenPair, error) {
	body := in.Body
	// The review account is answered from fixtures and never reaches the school (review.go).
	if account := trimAccount(body.Account); s.reviewer.matches(account) {
		return s.loginReview(ctx, account, body.Password, body.DeviceName, body.Platform)
	}
	v, err := s.verifyLogin(ctx, body.Account, body.Password, body.GatewayPassword, body.DeviceName, body.Platform)
	if err != nil {
		return nil, err
	}
	if v.sess == nil {
		// Session-sharing fast path (§7.5 revised): bind the new device to the
		// user's live school session. No school calls, no extra UAAP session.
		device, derr := s.deps.Store.CreateDevice(ctx, v.userID, body.DeviceName, body.Platform)
		if derr != nil {
			return nil, mapErr(derr)
		}
		s.deps.School.BindDevice(v.userID, device.ID, body.DeviceName, body.Platform, v.live)
		pair, terr := s.newTokenPair(ctx, v.userID, device.ID, s.now())
		if terr != nil {
			return nil, mapErr(terr)
		}
		pair.User = s.userView(ctx, v.userID)
		pair.User.AccountMasked = gate.MaskedAccount(v.account)
		_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
			At: s.now(), UserID: v.userID, DeviceID: device.ID, Event: "login.shared",
		})
		return pair, nil
	}
	// 4. Persist credentials + device + tokens.
	return s.finishLogin(ctx, v.userID, v.account, v.password, v.gwPass, body.DeviceName, body.Platform, v.fp, v.sess)
}

// verifiedLogin is an identity the school accepted. Creating a device or
// issuing tokens is the caller's decision.
type verifiedLogin struct {
	userID   string
	account  string
	password string
	gwPass   string
	fp       string
	sess     *session.Session // full school login; nil when a live session was reused
	live     *session.Session // reused live session; nil on a full login
}

// verifyLogin validates the request, consults the rejection cache, and proves
// the credentials against the school. It creates no device and issues no tokens.
func (s *Server) verifyLogin(ctx context.Context, rawAccount, password, gatewayPassword, deviceName, platform string) (*verifiedLogin, error) {
	// 1. Local pre-validation (§5.1): never touch the school, never count as a failure.
	if res := gate.ValidatePasswordFormat(password, s.deps.Cfg.PasswordMaxLen); !res.OK {
		return nil, passwordProblem(res)
	}
	gwPass := gatewayPassword
	if gwPass == "" {
		gwPass = password // §6: gateway password defaults to the school password
	}
	if gwPass != password {
		if res := gate.ValidatePasswordFormat(gwPass, s.deps.Cfg.PasswordMaxLen); !res.OK {
			return nil, passwordProblem(res)
		}
	}

	account := trimAccount(rawAccount)
	if account == "" {
		return nil, newProblem(http.StatusUnprocessableEntity, "auth.invalid_account", "学号不能为空")
	}

	// 2. Rejected fingerprint (§5.2 step 2): do not resubmit a recently refused pair.
	fp := vault.Fingerprint(s.deps.Pepper, account, password)
	if s.rejections.rejected(fp) {
		return nil, newProblem(http.StatusUnauthorized, "school.credentials_invalid",
			"该账号与密码最近已被学校拒绝，请确认后重试")
	}

	// 3. School-side verification.
	u, _, err := s.deps.Store.GetOrCreateUser(ctx, vault.AccountHash(s.deps.Pepper, account))
	if err != nil {
		return nil, mapErr(err)
	}

	// 3.5 Session-sharing (§7.5 revised): when the presented password matches
	// the vault and a live school session exists, reuse it. A mismatch falls
	// through to a full login, which refreshes the vault.
	if live, ok := s.deps.School.LiveSession(u.ID); ok {
		if cred, cerr := s.deps.Store.GetCredential(ctx, u.ID); cerr == nil {
			if plaintext, derr := s.deps.Vault.Decrypt(u.ID, cred.Blob); derr == nil {
				var payload vault.CredPayload
				if jerr := json.Unmarshal(plaintext, &payload); jerr == nil && payload.Password != "" &&
					subtle.ConstantTimeCompare([]byte(payload.Password), []byte(password)) == 1 {
					return &verifiedLogin{userID: u.ID, account: account, password: password, gwPass: gwPass, fp: fp, live: live}, nil
				}
			}
		}
	}

	sess, err := s.deps.School.Login(ctx, u.ID, account, password, gwPass, deviceName, platform)
	if err != nil {
		if errs.CodeOf(err) == errs.InvalidCredentials {
			s.rejections.mark(fp)
			_ = s.deps.Store.SetCredentialState(ctx, u.ID, store.CredInvalid)
		}
		return nil, mapErr(err)
	}
	return &verifiedLogin{userID: u.ID, account: account, password: password, gwPass: gwPass, fp: fp, sess: sess}, nil
}

func (s *Server) solveChallenge(ctx context.Context, in *ChallengeSolveInput) (*dto.TokenPair, error) {
	if in.Body.Code == "" {
		return nil, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "验证码不能为空")
	}
	params, err := s.deps.School.ChallengeParams(in.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	sess, err := s.deps.School.SolveChallenge(ctx, in.ID, in.Body.Code)
	if err != nil {
		return nil, mapErr(err)
	}
	fp := vault.Fingerprint(s.deps.Pepper, params.Account, params.Password)
	return s.finishLogin(ctx, params.UserID, params.Account, params.Password, params.GwPass, params.Device, params.Platform, fp, sess)
}

// finishLogin stores credentials, creates the device, issues the token pair.
func (s *Server) finishLogin(ctx context.Context, userID, account, password, gwPass, deviceName, platform, fp string, sess *session.Session) (*dto.TokenPair, error) {
	now := s.now()
	if err := s.storeCredentials(ctx, userID, account, password, gwPass, fp); err != nil {
		return nil, err
	}
	device, err := s.deps.Store.CreateDevice(ctx, userID, deviceName, platform)
	if err != nil {
		return nil, mapErr(err)
	}
	// Bind the school session group entry (§7.5) to the device row so a
	// later device revocation releases the right UAAP quota slot.
	if sess != nil {
		s.deps.School.AttachDevice(userID, device.ID, deviceName, sess)
	}
	pair, err := s.newTokenPair(ctx, userID, device.ID, now)
	if err != nil {
		return nil, mapErr(err)
	}
	pair.User = s.userView(ctx, userID)
	pair.User.AccountMasked = gate.MaskedAccount(account)
	_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: now, UserID: userID, DeviceID: device.ID, Event: "login.success",
	})
	return pair, nil
}

// storeCredentials encrypts the school credentials into the vault and marks
// them valid. It creates no device and issues no tokens.
func (s *Server) storeCredentials(ctx context.Context, userID, account, password, gwPass, fp string) error {
	payload, err := json.Marshal(vault.CredPayload{
		Account:         account,
		Password:        password,
		GatewayPassword: gwPass,
	})
	if err != nil {
		return mapErr(err)
	}
	blob, err := s.deps.Vault.Encrypt(userID, payload)
	if err != nil {
		return mapErr(err)
	}
	cred := store.Credential{UserID: userID, Blob: blob, Fingerprint: fp, UpdatedAt: s.now()}
	if err := s.deps.Store.UpsertCredential(ctx, cred); err != nil {
		return mapErr(err)
	}
	if err := s.deps.Store.SetCredentialState(ctx, userID, store.CredValid); err != nil {
		return mapErr(err)
	}
	return nil
}

func (s *Server) refresh(ctx context.Context, in *RefreshInput) (*dto.TokenPair, error) {
	now := s.now()
	if in.Body.RefreshToken == "" {
		return nil, newProblem(http.StatusUnauthorized, "auth.invalid_token", "刷新令牌无效")
	}
	hash := token.HashRefreshToken(in.Body.RefreshToken)
	t, err := s.deps.Store.GetRefreshToken(ctx, hash)
	if err != nil {
		return nil, newProblem(http.StatusUnauthorized, "auth.invalid_token", "刷新令牌无效")
	}
	// Reuse detection: a rotated or revoked token presented again kills the
	// whole device's chain (DESIGN.md §5).
	if t.Revoked || t.RotatedTo != "" {
		_ = s.deps.Store.RevokeDeviceTokens(ctx, t.DeviceID)
		_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
			At: now, UserID: t.UserID, DeviceID: t.DeviceID, Event: "refresh.reuse_detected",
		})
		return nil, newProblem(http.StatusUnauthorized, "auth.invalid_token",
			"刷新令牌已被使用过，为保护账号已吊销该设备的全部令牌，请重新登录")
	}
	if now.After(t.ExpiresAt) {
		return nil, newProblem(http.StatusUnauthorized, "auth.token_expired", "刷新令牌已过期，请重新登录")
	}
	newSecret, newHash, err := token.NewRefreshToken()
	if err != nil {
		return nil, mapErr(err)
	}
	if err := s.deps.Store.RotateRefreshToken(ctx, hash, newHash, now.Add(s.deps.Tokens.RefreshTTL())); err != nil {
		return nil, mapErr(err)
	}
	access, _, err := s.deps.Tokens.NewAccessToken(t.UserID, t.DeviceID, now)
	if err != nil {
		return nil, mapErr(err)
	}
	pair := &dto.TokenPair{
		AccessToken:  access,
		RefreshToken: newSecret,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.deps.Tokens.AccessTTL().Seconds()),
	}
	pair.User = s.userView(ctx, t.UserID)
	return pair, nil
}

func (s *Server) newTokenPair(ctx context.Context, userID, deviceID string, now time.Time) (*dto.TokenPair, error) {
	access, _, err := s.deps.Tokens.NewAccessToken(userID, deviceID, now)
	if err != nil {
		return nil, err
	}
	secret, rhash, err := token.NewRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := s.deps.Store.InsertRefreshToken(ctx, store.RefreshToken{
		Hash:      rhash,
		UserID:    userID,
		DeviceID:  deviceID,
		ExpiresAt: now.Add(s.deps.Tokens.RefreshTTL()),
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &dto.TokenPair{
		AccessToken:  access,
		RefreshToken: secret,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.deps.Tokens.AccessTTL().Seconds()),
	}, nil
}

func (s *Server) userView(ctx context.Context, userID string) dto.UserView {
	v := dto.UserView{ID: userID, CredentialState: store.CredUnverified}
	if u, err := s.deps.Store.GetUser(ctx, userID); err == nil {
		v.CredentialState = u.CredentialState
	}
	return v
}

func (s *Server) revokeDevice(ctx context.Context, userID, deviceID string) error {
	if err := s.deps.Store.RevokeDevice(ctx, userID, deviceID); err != nil {
		return err
	}
	if err := s.deps.Store.RevokeDeviceTokens(ctx, deviceID); err != nil {
		return err
	}
	// Release this device's UAAP quota slot (§7.5); other devices of the
	// user keep their sessions.
	s.deps.School.LogoutDevice(userID, deviceID)
	return s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: s.now(), UserID: userID, DeviceID: deviceID, Event: "device.revoked",
	})
}

// passwordProblem shapes §5.1 violations.
func passwordProblem(res gate.Result) error {
	p := newProblem(http.StatusUnprocessableEntity, "auth.password_invalid_format",
		"密码包含学校不支持的字符，未提交到学校，未计入失败次数")
	p.Detail = res.Hint
	p.Violations = res.Violations
	return p
}

// --- small helpers kept dependency-light for tests ---

func trimAccount(account string) string {
	// Trim only plain spaces around the value; the account is digits.
	start, end := 0, len(account)
	for start < end && account[start] == ' ' {
		start++
	}
	for end > start && account[end-1] == ' ' {
		end--
	}
	return account[start:end]
}
