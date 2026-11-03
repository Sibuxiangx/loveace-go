package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// deviceServer builds an API server whose clock the test controls. The
// returned pointer is the clock; advance it to move time forward.
func deviceServer(t *testing.T, tr *transport.MockTransport) (http.Handler, *time.Time) {
	t.Helper()
	srv, h := newGroupTestServer(t, tr, "")
	srv.deps.Cfg.PublicURL = "https://loveace.test"
	clock := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return clock }
	return h, &clock
}

// deviceCall sends one request and decodes the JSON body (or nil when none).
func deviceCall(t *testing.T, h http.Handler, method, path, contentType, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const formType = "application/x-www-form-urlencoded"

// issueDeviceCode starts a grant as a hardware device would, returning the
// device code and the user code shown on its screen.
func issueDeviceCode(t *testing.T, h http.Handler) (deviceCode, userCode string) {
	t.Helper()
	code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/code", formType,
		"client_name=Kiosk&platform=hardware")
	if code != http.StatusOK {
		t.Fatalf("device code = %d %v", code, out)
	}
	deviceCode, _ = out["device_code"].(string)
	userCode, _ = out["user_code"].(string)
	if deviceCode == "" || len(strings.ReplaceAll(userCode, "-", "")) != 8 {
		t.Fatalf("device code response malformed: %v", out)
	}
	return deviceCode, userCode
}

func pollDevice(t *testing.T, h http.Handler, deviceCode string) (int, map[string]any) {
	t.Helper()
	return deviceCall(t, h, http.MethodPost, "/v1/auth/device/token", formType,
		"grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code="+deviceCode)
}

func TestDeviceGrantApprovesAndIssuesTokensOnce(t *testing.T) {
	h, clock := deviceServer(t, successTransport(t))
	deviceCode, userCode := issueDeviceCode(t, h)

	if code, out := pollDevice(t, h, deviceCode); code != http.StatusBadRequest || out["error"] != "authorization_pending" {
		t.Fatalf("poll before approval = %d %v, want authorization_pending", code, out)
	}

	code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/verify", "application/json",
		`{"user_code":"`+userCode+`","account":"20000003","password":"Passw0rd!123"}`)
	if code != http.StatusOK || out["status"] != "approved" {
		t.Fatalf("verify = %d %v, want approved", code, out)
	}

	*clock = clock.Add(6 * time.Second)
	code, out = pollDevice(t, h, deviceCode)
	if code != http.StatusOK {
		t.Fatalf("poll after approval = %d %v, want tokens", code, out)
	}
	if access, _ := out["access_token"].(string); access == "" {
		t.Fatalf("no access token issued: %v", out)
	}
	if refresh, _ := out["refresh_token"].(string); refresh == "" {
		t.Fatalf("no refresh token issued: %v", out)
	}

	if code, out := pollDevice(t, h, deviceCode); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("second redemption = %d %v, want invalid_grant (single use)", code, out)
	}
}

func TestDeviceGrantPollingFasterThanIntervalIsSlowDown(t *testing.T) {
	h, clock := deviceServer(t, successTransport(t))
	deviceCode, _ := issueDeviceCode(t, h)

	if _, out := pollDevice(t, h, deviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("first poll = %v", out)
	}
	*clock = clock.Add(time.Second)
	if _, out := pollDevice(t, h, deviceCode); out["error"] != "slow_down" {
		t.Fatalf("early poll = %v, want slow_down", out)
	}
	// RFC 8628 §3.5: the interval grew by five seconds, so ten seconds after the
	// slow_down poll the device may ask again.
	*clock = clock.Add(10 * time.Second)
	if _, out := pollDevice(t, h, deviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("poll at the grown interval = %v, want authorization_pending", out)
	}
}

func TestDeviceGrantExpiresAfterTTL(t *testing.T) {
	h, clock := deviceServer(t, successTransport(t))
	deviceCode, userCode := issueDeviceCode(t, h)

	*clock = clock.Add(11 * time.Minute)
	if code, out := pollDevice(t, h, deviceCode); code != http.StatusBadRequest || out["error"] != "expired_token" {
		t.Fatalf("poll after TTL = %d %v, want expired_token", code, out)
	}
	code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/verify", "application/json",
		`{"user_code":"`+userCode+`","account":"20000003","password":"Passw0rd!123"}`)
	if code != http.StatusGone || out["code"] != "device.code_expired" {
		t.Fatalf("verify after TTL = %d %v, want 410 device.code_expired", code, out)
	}
}

func TestDeviceGrantLocksAfterFailedCredentialAttempts(t *testing.T) {
	h, _ := deviceServer(t, invalidIdentityTransport(t))
	_, userCode := issueDeviceCode(t, h)
	body := `{"user_code":"` + userCode + `","account":"20000003","password":"wrongpw"}`

	for i := 1; i <= deviceMaxAttempts; i++ {
		code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/verify", "application/json", body)
		if code != http.StatusUnauthorized || out["code"] != "school.credentials_invalid" {
			t.Fatalf("attempt %d = %d %v, want 401 school.credentials_invalid", i, code, out)
		}
	}
	code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/verify", "application/json", body)
	if code != http.StatusTooManyRequests || out["code"] != "device.too_many_attempts" {
		t.Fatalf("attempt after the limit = %d %v, want 429 device.too_many_attempts", code, out)
	}
}

func TestDeviceGrantRejectsMalformedInput(t *testing.T) {
	h, _ := deviceServer(t, successTransport(t))

	if code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/code", formType, "platform=x"); code != http.StatusBadRequest || out["error"] != "invalid_request" {
		t.Fatalf("missing client_name = %d %v", code, out)
	}
	if code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/token", formType,
		"grant_type=password&device_code=x"); code != http.StatusBadRequest || out["error"] != "unsupported_grant_type" {
		t.Fatalf("wrong grant type = %d %v", code, out)
	}
	if code, out := pollDevice(t, h, "not-a-real-device-code"); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("unknown device code = %d %v", code, out)
	}
	if code, out := deviceCall(t, h, http.MethodGet, "/v1/auth/device/info?user_code=AAAA-AAAA", "", ""); code != http.StatusUnprocessableEntity || out["code"] != "device.code_invalid" {
		t.Fatalf("malformed user code = %d %v", code, out)
	}
	if code, out := deviceCall(t, h, http.MethodGet, "/v1/auth/device/info?user_code=BCDF-GHJK", "", ""); code != http.StatusNotFound || out["code"] != "device.code_not_found" {
		t.Fatalf("unknown user code = %d %v", code, out)
	}
}

func TestDeviceSolveRejectsUnknownChallenge(t *testing.T) {
	h, _ := deviceServer(t, successTransport(t))
	_, userCode := issueDeviceCode(t, h)
	code, out := deviceCall(t, h, http.MethodPost,
		"/v1/auth/device/challenges/not-issued-to-this-grant/solution", "application/json",
		`{"user_code":"`+userCode+`","code":"abcd"}`)
	if code != http.StatusNotFound || out["code"] != "device.challenge_unknown" {
		t.Fatalf("solve with unknown challenge = %d %v", code, out)
	}
}

func TestDeviceVerificationPageIsFramingAndReferrerSafe(t *testing.T) {
	h, _ := deviceServer(t, successTransport(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /device = %d", rec.Code)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, the user code must not leak in referrers", got)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q, the page must not be framed", csp)
	}
}

func TestDeviceGrantApprovedIsRedeemedAtAnyPollPace(t *testing.T) {
	h, clock := deviceServer(t, successTransport(t))
	deviceCode, userCode := issueDeviceCode(t, h)

	if _, out := pollDevice(t, h, deviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("first poll = %v", out)
	}
	*clock = clock.Add(time.Second)
	if _, out := pollDevice(t, h, deviceCode); out["error"] != "slow_down" {
		t.Fatalf("early poll = %v, want slow_down", out)
	}
	*clock = clock.Add(time.Second)
	code, out := deviceCall(t, h, http.MethodPost, "/v1/auth/device/verify", "application/json",
		`{"user_code":"`+userCode+`","account":"20000003","password":"Passw0rd!123"}`)
	if code != http.StatusOK {
		t.Fatalf("verify = %d %v", code, out)
	}
	// The device polls again straight away, far faster than the grown interval.
	// An approved grant must still hand over its tokens.
	code, out = pollDevice(t, h, deviceCode)
	if code != http.StatusOK || out["access_token"] == nil {
		t.Fatalf("poll after approval = %d %v, want tokens despite pace", code, out)
	}
}
