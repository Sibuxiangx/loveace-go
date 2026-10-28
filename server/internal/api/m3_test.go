package api

import (
	"net/http"
	"testing"
)

// TestPublicStatusAndAdminHealth pins the §9 split: /v1/public/status
// exposes layer states only (no auth, no counters); /v1/admin/health adds
// counters and the pollution timeline; the maintenance override wins.
func TestPublicStatusAndAdminHealth(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "adm-secret")

	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/public/status", "", nil)
	out := asMap(t, rawOut)
	if res.StatusCode != 200 || out["layers"] == nil {
		t.Fatalf("public status = %d %v", res.StatusCode, rawOut)
	}
	if _, hasCounters := out["counters"]; hasCounters {
		t.Fatal("public view must not expose counters")
	}

	res, _ = doJSON(t, h, http.MethodGet, "/v1/admin/health", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin health without token = %d, want 401", res.StatusCode)
	}

	// Real traffic: a login + a school-backed read produce observations.
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	res, _ = doJSON(t, h, http.MethodGet, "/v1/me/academic/summary", access, nil)
	if res.StatusCode != 200 {
		t.Fatalf("summary = %d", res.StatusCode)
	}

	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/admin/health", "adm-secret", nil)
	out = asMap(t, rawOut)
	if res.StatusCode != 200 {
		t.Fatalf("admin health = %d %v", res.StatusCode, rawOut)
	}
	counters, _ := out["counters"].(map[string]any)
	if counters == nil {
		t.Fatalf("admin health lost counters: %v", rawOut)
	}
	if out["layers"] == nil {
		t.Fatalf("admin health lost the layered status: %v", rawOut)
	}
	if captcha, _ := out["captcha"].(map[string]any); captcha == nil {
		t.Fatalf("admin health must carry the three-loop captcha counters: %v", rawOut)
	}

	// Maintenance override via the endpoint.
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/health/maintenance", "adm-secret",
		map[string]any{"layer": "jwc", "minutes": 30})
	if res.StatusCode != 200 {
		t.Fatalf("maintenance = %d", res.StatusCode)
	}
	_, rawOut = doJSON(t, h, http.MethodGet, "/v1/admin/health", "adm-secret", nil)
	out = asMap(t, rawOut)
	layers, _ := out["layers"].(map[string]any)
	jwc, _ := layers["jwc"].(map[string]any)
	if jwc == nil || jwc["state"] != "maintenance" {
		t.Fatalf("jwc after override = %v", layers["jwc"])
	}
}
