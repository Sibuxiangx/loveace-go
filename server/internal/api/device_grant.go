package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Sibuxiangx/loveace-go/server/internal/dto"
	"github.com/Sibuxiangx/loveace-go/server/internal/gate"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// Device authorization (OAuth 2.0 Device Grant, RFC 8628). A device without a
// keyboard shows a user code. The user approves it on the verification page by
// entering school credentials there. The device polls the token endpoint and
// receives tokens. The device never sees the password.

//go:embed web
var webFS embed.FS

const (
	deviceGrantType    = "urn:ietf:params:oauth:grant-type:device_code"
	deviceCodeTTL      = 10 * time.Minute
	deviceInterval     = 5 // seconds between polls
	deviceSlowDownStep = 5 // RFC 8628 §3.5: slow_down adds five seconds
	deviceMaxAttempts  = 5 // failed credential attempts per grant
	deviceChallengeTTL = 15 * time.Minute
	deviceBodyLimit    = 1 << 16
	userCodeAlphabet   = "BCDFGHJKLMNPQRSTVWXZ23456789" // no vowels, no look-alikes
	userCodeLength     = 8
)

// registerDeviceAuth wires the device grant endpoints and the verification page.
func (s *Server) registerDeviceAuth(r chi.Router) {
	r.Post("/v1/auth/device/code", s.deviceCode)
	r.Post("/v1/auth/device/token", s.deviceToken)
	r.Get("/v1/auth/device/info", s.deviceInfo)
	r.Post("/v1/auth/device/verify", s.deviceVerify)
	r.Post("/v1/auth/device/challenges/{id}/solution", s.deviceSolve)
	r.Get("/device", deviceAsset("web/device.html", "text/html; charset=utf-8"))
	r.Get("/device.css", deviceAsset("web/device.css", "text/css; charset=utf-8"))
	r.Get("/device.js", deviceAsset("web/device.js", "text/javascript; charset=utf-8"))
	r.Get("/device-icons.js", deviceAsset("web/icons.js", "text/javascript; charset=utf-8"))
	r.Get("/device-appicon.png", deviceAsset("web/appicon.png", "image/png"))
}

// isLoginRatePath reports whether a POST spends the per-IP login budget.
func isLoginRatePath(p string) bool {
	switch p {
	case "/v1/auth/sessions", "/v1/auth/device/code", "/v1/auth/device/verify":
		return true
	}
	return strings.HasPrefix(p, "/v1/auth/device/challenges/") && strings.HasSuffix(p, "/solution")
}

// deviceAsset serves an embedded page with headers that keep the user code and
// password from leaking: no referrer, no framing, no third-party resources.
func deviceAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := webFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
		_, _ = w.Write(body)
	}
}

// deviceCode issues a device code and user code (RFC 8628 §3.1, §3.2).
func (s *Server) deviceCode(w http.ResponseWriter, r *http.Request) {
	in, err := oauthParams(r)
	if err != nil {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	clientName := strings.TrimSpace(in["client_name"])
	platform := strings.TrimSpace(in["platform"])
	if clientName == "" || len([]rune(clientName)) > 64 || len([]rune(platform)) > 32 {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	deviceCode, err := randomToken(32)
	if err != nil {
		writeDeviceErr(w, err)
		return
	}
	userCode, err := newUserCode()
	if err != nil {
		writeDeviceErr(w, err)
		return
	}
	now := s.now()
	ctx := r.Context()
	g := store.DeviceGrant{
		ID:             store.NewID(),
		DeviceCodeHash: sha256Hex(deviceCode),
		UserCodeHash:   s.userCodeHash(userCode),
		ClientName:     clientName,
		Platform:       platform,
		Status:         store.DeviceGrantPending,
		Interval:       deviceInterval,
		ExpiresAt:      now.Add(deviceCodeTTL),
		CreatedAt:      now,
	}
	if err := s.deps.Store.CreateDeviceGrant(ctx, g); err != nil {
		writeDeviceErr(w, mapErr(err))
		return
	}
	_ = s.deps.Store.PurgeDeviceGrants(ctx, now.Add(-time.Hour))
	shown := formatUserCode(userCode)
	uri := strings.TrimRight(s.deps.Cfg.PublicURL, "/") + "/device"
	writeDeviceJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 shown,
		"verification_uri":          uri,
		"verification_uri_complete": uri + "?user_code=" + url.QueryEscape(shown),
		"expires_in":                int(deviceCodeTTL / time.Second),
		"interval":                  deviceInterval,
	})
}

// deviceToken is the token endpoint (RFC 8628 §3.4, §3.5). It answers with
// the RFC error codes so that standard device clients can drive it.
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	in, err := oauthParams(r)
	if err != nil {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if in["grant_type"] != deviceGrantType {
		deviceOAuthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	ctx := r.Context()
	g, err := s.deps.Store.GetDeviceGrantByDeviceCode(ctx, sha256Hex(in["device_code"]))
	if errors.Is(err, store.ErrNotFound) {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if err != nil {
		deviceOAuthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	now := s.now()
	if !now.Before(g.ExpiresAt) {
		deviceOAuthError(w, http.StatusBadRequest, "expired_token")
		return
	}
	if g.Status == store.DeviceGrantConsumed {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// Polling faster than the interval is slow_down, and the interval grows
	// for the rest of the grant.
	interval := g.Interval
	early := !g.LastPolledAt.IsZero() && now.Sub(g.LastPolledAt) < time.Duration(interval)*time.Second
	if early {
		interval += deviceSlowDownStep
	}
	if err := s.deps.Store.RecordDeviceGrantPoll(ctx, g.ID, now, interval); err != nil {
		deviceOAuthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	if early {
		deviceOAuthError(w, http.StatusBadRequest, "slow_down")
		return
	}
	if g.Status == store.DeviceGrantPending {
		deviceOAuthError(w, http.StatusBadRequest, "authorization_pending")
		return
	}
	redeemed, err := s.deps.Store.ConsumeDeviceGrant(ctx, g.ID)
	if err != nil || !redeemed {
		deviceOAuthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	pair, err := s.issueDeviceTokens(ctx, g.UserID, g.ClientName, g.Platform, now)
	if err != nil {
		// The school session is gone and cannot be restored. The grant is
		// spent; the device must start a new authorization.
		deviceOAuthError(w, http.StatusBadRequest, "expired_token")
		return
	}
	writeDeviceJSON(w, http.StatusOK, pair)
}

// issueDeviceTokens creates the device row, attaches the user's live school
// session to it, and issues the token pair.
func (s *Server) issueDeviceTokens(ctx context.Context, userID, clientName, platform string, now time.Time) (*dto.TokenPair, error) {
	device, err := s.deps.Store.CreateDevice(ctx, userID, clientName, platform)
	if err != nil {
		return nil, err
	}
	sess, err := s.deps.School.SessionFor(ctx, userID, device.ID)
	if err != nil {
		_ = s.deps.Store.RevokeDevice(ctx, userID, device.ID)
		return nil, err
	}
	s.deps.School.AttachDevice(userID, device.ID, clientName, sess)
	pair, err := s.newTokenPair(ctx, userID, device.ID, now)
	if err != nil {
		return nil, err
	}
	pair.User = s.userView(ctx, userID)
	pair.User.AccountMasked = s.maskedAccount(ctx, userID)
	_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: now, UserID: userID, DeviceID: device.ID, Event: "login.device_grant",
	})
	return pair, nil
}

// deviceInfo tells the verification page what it is approving.
func (s *Server) deviceInfo(w http.ResponseWriter, r *http.Request) {
	g, err := s.pendingGrantByUserCode(r.Context(), r.URL.Query().Get("user_code"))
	if err != nil {
		writeDeviceErr(w, err)
		return
	}
	writeDeviceJSON(w, http.StatusOK, map[string]any{
		"client_name": g.ClientName,
		"platform":    g.Platform,
		"expires_in":  int(g.ExpiresAt.Sub(s.now()).Seconds()),
	})
}

type deviceVerifyBody struct {
	UserCode        string `json:"user_code"`
	Account         string `json:"account"`
	Password        string `json:"password"`
	GatewayPassword string `json:"gateway_password"`
}

// deviceVerify checks the school credentials for a user code and approves the
// grant. On a CAPTCHA it answers 409 with a challenge id; the page then calls
// deviceSolve.
func (s *Server) deviceVerify(w http.ResponseWriter, r *http.Request) {
	var in deviceVerifyBody
	if err := decodeDeviceBody(r, &in); err != nil {
		writeDeviceErr(w, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "请求格式无效"))
		return
	}
	ctx := r.Context()
	g, err := s.pendingGrantByUserCode(ctx, in.UserCode)
	if err != nil {
		writeDeviceErr(w, err)
		return
	}
	v, err := s.verifyLogin(ctx, in.Account, in.Password, in.GatewayPassword, g.ClientName, g.Platform)
	if err != nil {
		var p *Problem
		if errors.As(err, &p) {
			if p.ChallengeID != "" {
				s.deviceChallenges.put(p.ChallengeID, g.ID)
			}
			if p.Code == "school.credentials_invalid" {
				_, _ = s.deps.Store.IncrementDeviceGrantAttempts(ctx, g.ID)
			}
		}
		writeDeviceErr(w, err)
		return
	}
	if v.sess != nil {
		if err := s.storeCredentials(ctx, v.userID, v.account, v.password, v.gwPass, v.fp); err != nil {
			writeDeviceErr(w, err)
			return
		}
	}
	s.approveGrant(ctx, w, g.ID, v.userID)
}

type deviceSolveBody struct {
	UserCode string `json:"user_code"`
	Code     string `json:"code"`
}

// deviceSolve answers a CAPTCHA raised by deviceVerify and approves the grant
// that asked for it.
func (s *Server) deviceSolve(w http.ResponseWriter, r *http.Request) {
	challengeID := chi.URLParam(r, "id")
	var in deviceSolveBody
	if err := decodeDeviceBody(r, &in); err != nil || strings.TrimSpace(in.Code) == "" {
		writeDeviceErr(w, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "验证码不能为空"))
		return
	}
	ctx := r.Context()
	g, err := s.pendingGrantByUserCode(ctx, in.UserCode)
	if err != nil {
		writeDeviceErr(w, err)
		return
	}
	if owner, ok := s.deviceChallenges.grantFor(challengeID); !ok || owner != g.ID {
		writeDeviceErr(w, newProblem(http.StatusNotFound, "device.challenge_unknown", "验证码已过期，请重新取图"))
		return
	}
	params, err := s.deps.School.ChallengeParams(challengeID)
	if err != nil {
		writeDeviceErr(w, mapErr(err))
		return
	}
	if _, err := s.deps.School.SolveChallenge(ctx, challengeID, in.Code); err != nil {
		writeDeviceErr(w, mapErr(err))
		return
	}
	s.deviceChallenges.forget(challengeID)
	fp := vault.Fingerprint(s.deps.Pepper, params.Account, params.Password)
	if err := s.storeCredentials(ctx, params.UserID, params.Account, params.Password, params.GwPass, fp); err != nil {
		writeDeviceErr(w, err)
		return
	}
	s.approveGrant(ctx, w, g.ID, params.UserID)
}

// approveGrant binds a pending grant to the verified user and answers the page.
func (s *Server) approveGrant(ctx context.Context, w http.ResponseWriter, grantID, userID string) {
	ok, err := s.deps.Store.ApproveDeviceGrant(ctx, grantID, userID, s.now())
	if err != nil {
		writeDeviceErr(w, mapErr(err))
		return
	}
	if !ok {
		writeDeviceErr(w, newProblem(http.StatusConflict, "device.code_used", "该设备码已被使用或已过期"))
		return
	}
	_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: s.now(), UserID: userID, Event: "device_grant.approved",
	})
	writeDeviceJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// pendingGrantByUserCode resolves a typed user code to a grant that can still
// be approved.
func (s *Server) pendingGrantByUserCode(ctx context.Context, raw string) (*store.DeviceGrant, error) {
	code, ok := normalizeUserCode(raw)
	if !ok {
		return nil, newProblem(http.StatusUnprocessableEntity, "device.code_invalid", "设备码格式不正确")
	}
	g, err := s.deps.Store.GetDeviceGrantByUserCode(ctx, s.userCodeHash(code))
	if errors.Is(err, store.ErrNotFound) {
		return nil, newProblem(http.StatusNotFound, "device.code_not_found", "设备码不存在或已过期")
	}
	if err != nil {
		return nil, mapErr(err)
	}
	switch {
	case !s.now().Before(g.ExpiresAt):
		return nil, newProblem(http.StatusGone, "device.code_expired", "设备码已过期，请让设备重新发起授权")
	case g.Status != store.DeviceGrantPending:
		return nil, newProblem(http.StatusConflict, "device.code_used", "该设备码已被使用")
	case g.Attempts >= deviceMaxAttempts:
		return nil, newProblem(http.StatusTooManyRequests, "device.too_many_attempts", "尝试次数过多，请让设备重新发起授权")
	}
	return g, nil
}

func (s *Server) maskedAccount(ctx context.Context, userID string) string {
	cred, err := s.deps.Store.GetCredential(ctx, userID)
	if err != nil {
		return ""
	}
	plain, err := s.deps.Vault.Decrypt(userID, cred.Blob)
	if err != nil {
		return ""
	}
	var payload vault.CredPayload
	if json.Unmarshal(plain, &payload) != nil {
		return ""
	}
	return gate.MaskedAccount(payload.Account)
}

func (s *Server) userCodeHash(code string) string {
	mac := hmac.New(sha256.New, s.deps.Pepper)
	mac.Write([]byte("usercode:" + code))
	return hex.EncodeToString(mac.Sum(nil))
}

// deviceChallenges maps a school CAPTCHA challenge to the grant that caused it,
// so the solution endpoint approves the grant that asked. Entries live as long
// as the challenge they point to.
type deviceChallenges struct {
	mu     sync.Mutex
	now    func() time.Time
	grants map[string]deviceChallengeRef
}

type deviceChallengeRef struct {
	grantID string
	at      time.Time
}

func newDeviceChallenges(now func() time.Time) *deviceChallenges {
	return &deviceChallenges{now: now, grants: map[string]deviceChallengeRef{}}
}

func (d *deviceChallenges) put(challengeID, grantID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for id, ref := range d.grants {
		if now.Sub(ref.at) > deviceChallengeTTL {
			delete(d.grants, id)
		}
	}
	d.grants[challengeID] = deviceChallengeRef{grantID: grantID, at: now}
}

func (d *deviceChallenges) grantFor(challengeID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ref, ok := d.grants[challengeID]
	return ref.grantID, ok
}

func (d *deviceChallenges) forget(challengeID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.grants, challengeID)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newUserCode() (string, error) {
	b := make([]byte, userCodeLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = userCodeAlphabet[n.Int64()]
	}
	return string(b), nil
}

func formatUserCode(code string) string {
	return code[:4] + "-" + code[4:]
}

// normalizeUserCode accepts the code with or without its dash, in any case.
func normalizeUserCode(raw string) (string, bool) {
	code := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(raw)))
	if len(code) != userCodeLength {
		return "", false
	}
	for _, c := range code {
		if !strings.ContainsRune(userCodeAlphabet, c) {
			return "", false
		}
	}
	return code, true
}

// oauthParams reads a form-encoded (RFC 8628 §3.1) or JSON body into flat values.
func oauthParams(r *http.Request) (map[string]string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, deviceBodyLimit))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		for k, v := range raw {
			if str, ok := v.(string); ok {
				out[k] = str
			}
		}
		return out, nil
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	for k := range vals {
		out[k] = vals.Get(k)
	}
	return out, nil
}

func decodeDeviceBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, deviceBodyLimit))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func writeDeviceJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// deviceOAuthError answers with the RFC 6749 error shape the device grant uses.
func deviceOAuthError(w http.ResponseWriter, status int, code string) {
	writeDeviceJSON(w, status, map[string]string{"error": code})
}

// writeDeviceErr renders an error as a problem response. Unknown errors become
// a generic 500 so no internal detail leaves the server.
func writeDeviceErr(w http.ResponseWriter, err error) {
	var p *Problem
	if !errors.As(err, &p) {
		p = newProblem(http.StatusInternalServerError, "internal", "服务器内部错误")
	}
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
