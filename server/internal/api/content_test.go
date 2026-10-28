package api

import (
	"net/http"
	"testing"
)

// TestAnnouncementLifecycle pins §11.2: admin authoring, the pre-login
// and post-login lists with audience/window filtering, and acks. Without
// an admin token the authoring endpoints do not exist (404).
func TestAnnouncementLifecycle(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "adm-secret")

	create := map[string]any{
		"level": "warning", "body_md": "**维护** 今晚停机", "audience": "both",
		"pinned": true, "requires_ack": true, "duration_hours": 24,
	}
	res, rawOut := doJSON(t, h, http.MethodPost, "/v1/admin/announcements", "adm-secret", create)
	out := asMap(t, rawOut)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, out)
	}
	id, _ := out["id"].(string)

	// Pre-login list sees it (audience both) — the response is a bare array,
	// stamped with the public cache header (§11.2: plain HTTP, CDN-agnostic).
	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/public/announcements", "", nil)
	list, ok := rawOut.([]any)
	if res.StatusCode != 200 || !ok || len(list) != 1 {
		t.Fatalf("public list = %d %v", res.StatusCode, rawOut)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=60, stale-while-revalidate=300" {
		t.Fatalf("cache-control = %q", cc)
	}

	// Post-login list carries requires_ack; ack clears it. Authenticated
	// endpoints never carry a cache header.
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	res, _ = doJSON(t, h, http.MethodGet, "/v1/me/announcements", access, nil)
	if cc := res.Header.Get("Cache-Control"); cc != "" {
		t.Fatalf("me announcements must not be cached, got %q", cc)
	}
	_, rawOut = doJSON(t, h, http.MethodGet, "/v1/me/announcements", access, nil)
	list, _ = rawOut.([]any)
	if len(list) != 1 {
		t.Fatalf("me list = %v", rawOut)
	}
	first := asMap(t, list[0])
	if first["requires_ack"] != true || first["acked_at"] != nil {
		t.Fatalf("ack state wrong: %v", first)
	}
	res, _ = doJSON(t, h, http.MethodPost, "/v1/me/announcements/"+id+"/ack", access, nil)
	if res.StatusCode != 200 {
		t.Fatalf("ack = %d", res.StatusCode)
	}
	_, rawOut = doJSON(t, h, http.MethodGet, "/v1/me/announcements", access, nil)
	list, _ = rawOut.([]any)
	first = asMap(t, list[0])
	if first["acked_at"] == nil {
		t.Fatal("ack not visible after acking")
	}

	// Deactivation hides it everywhere.
	res, _ = doJSON(t, h, http.MethodDelete, "/v1/admin/announcements/"+id, "adm-secret", nil)
	if res.StatusCode != 200 {
		t.Fatalf("deactivate = %d", res.StatusCode)
	}
	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/public/announcements", "", nil)
	list, _ = rawOut.([]any)
	if len(list) != 0 {
		t.Fatalf("deactivated announcement still listed (%d)", len(list))
	}

	// Authoring without a token configured = 404 (endpoints absent).
	_, hNone := newGroupTestServer(t, tr, "")
	res, _ = doJSON(t, hNone, http.MethodPost, "/v1/admin/announcements", "", create)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured admin authoring = %d, want 404", res.StatusCode)
	}
}

// TestCalendarMaterialisation pins §11.1: term + rules → materialised
// days, with a makeup rule carrying its follow pointer.
func TestCalendarMaterialisation(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "adm-secret")

	// Term: first week Monday 2026-09-07, 20 weeks.
	res, _ := doJSON(t, h, http.MethodPut, "/v1/admin/calendar/terms", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "first_week_monday": "2026-09-07", "weeks": 20})
	if res.StatusCode != 200 {
		t.Fatalf("term upsert = %d", res.StatusCode)
	}
	// National Day 2026-10-01 is a holiday; 2026-10-10 (Saturday) is a
	// makeup day following week 4's Friday timetable.
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/rules", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "day": "2026-10-01", "kind": "holiday"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("holiday rule = %d", res.StatusCode)
	}
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/rules", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "day": "2026-10-10", "kind": "makeup",
			"follow_week": 4, "follow_weekday": 5})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("makeup rule = %d", res.StatusCode)
	}
	// Rules are drafts (§11.1 workflow): publish before the public view.
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/publish", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1"})
	if res.StatusCode != 200 {
		t.Fatalf("publish = %d", res.StatusCode)
	}

	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/public/calendar?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-11", "", nil)
	days, ok := rawOut.([]any)
	if res.StatusCode != 200 || !ok {
		t.Fatalf("calendar = %d %v", res.StatusCode, rawOut)
	}
	if len(days) != 11 {
		t.Fatalf("calendar days = %d, want 11", len(days))
	}
	// Week numbering: first Monday is 2026-09-07 → 2026-10-01 is week 4,
	// Thursday (weekday 4).
	oct1 := asMap(t, days[0])
	if oct1["kind"] != "holiday" || oct1["week_no"] != float64(4) || oct1["weekday"] != float64(4) {
		t.Fatalf("2026-10-01 = %v, want holiday wk4 wd4", oct1)
	}
	// 2026-10-10: week 5, Saturday (weekday 6), makeup following week 4's
	// Friday (weekday 5) timetable.
	oct10 := asMap(t, days[9])
	if oct10["kind"] != "makeup" || oct10["week_no"] != float64(5) || oct10["weekday"] != float64(6) {
		t.Fatalf("2026-10-10 = %v", oct10)
	}
	follows, _ := oct10["follows"].(map[string]any)
	if follows == nil || follows["week_no"] != float64(4) || follows["weekday"] != float64(5) {
		t.Fatalf("follows = %v", follows)
	}
	// Unknown term → 404.
	res, _ = doJSON(t, h, http.MethodGet, "/v1/public/calendar?term_id=nope", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown term = %d, want 404", res.StatusCode)
	}
}
