package api

import (
	"net/http"
	"testing"
)

// A logged-out device must lose access at once, not when its JWT expires.
func TestRevokedDeviceAccessTokenIsRejected(t *testing.T) {
	h := newTestServer(t, successTransport(t))
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "phone")

	if code, out := teachDo(t, h, http.MethodGet, "/v1/me", access, "", nil); code != http.StatusOK {
		t.Fatalf("before logout: /v1/me = %d %v", code, out)
	}
	if code, out := teachDo(t, h, http.MethodDelete, "/v1/auth/sessions/current", access, "", nil); code < 200 || code >= 300 {
		t.Fatalf("logout = %d %v", code, out)
	}
	code, out := teachDo(t, h, http.MethodGet, "/v1/me", access, "", nil)
	if code != http.StatusUnauthorized || out["code"] != "auth.invalid_token" {
		t.Fatalf("after logout the old token still works: %d %v, want 401 auth.invalid_token", code, out)
	}
}
