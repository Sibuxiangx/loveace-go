package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
)

// Test-only credentials. The production pair lives only in the server's env file.
const (
	reviewTestAccount  = "20990001"
	reviewTestPassword = "review-test-only"
)

func newReviewServer(t *testing.T, tr *transport.MockTransport) http.Handler {
	t.Helper()
	return newTestServerCfg(t, tr, config.Config{ReviewAccount: reviewTestAccount, ReviewPassword: reviewTestPassword})
}

// requireNoSchool fails if the review account sent anything to the school.
func requireNoSchool(t *testing.T, tr *transport.MockTransport, step string) {
	t.Helper()
	if n := len(tr.Recorded()); n != 0 {
		t.Fatalf("%s: review account sent %d school requests", step, n)
	}
}

func TestReviewLoginNeverReachesSchool(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	mustLogin(t, h, reviewTestAccount, reviewTestPassword, "review-phone")
	requireNoSchool(t, tr, "login")
}

func TestReviewWrongPasswordFailsLocally(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	res, _ := loginAs(t, h, reviewTestAccount, "not-the-password", "review-phone")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong review password = %d, want 401", res.StatusCode)
	}
	requireNoSchool(t, tr, "wrong password")
}

func TestReviewAcademicServedFromFixtures(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	access := mustLogin(t, h, reviewTestAccount, reviewTestPassword, "review-phone")
	res, raw := doJSON(t, h, http.MethodGet, "/v1/me/academic/summary", access, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("summary = %d %v", res.StatusCode, raw)
	}
	data, _ := asMap(t, raw)["data"].(map[string]any)
	if data["gpa"] != 3.61 {
		t.Fatalf("summary data = %v, want the fixture", data)
	}
	requireNoSchool(t, tr, "academic summary")
}

func TestReviewOpsReadsAndSingleWrite(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	access := mustLogin(t, h, reviewTestAccount, reviewTestPassword, "review-phone")

	if res, _ := runOps(t, h, access, "", "labor.joined_clubs", map[string]any{}); res.Code != http.StatusOK {
		t.Fatalf("joined clubs = %d", res.Code)
	}

	args := map[string]any{"activity_id": "review-open"}
	if res, _ := runOps(t, h, access, "", "labor.apply_activity", args); res.Code != http.StatusPreconditionRequired {
		t.Fatalf("apply without key = %d, want 428", res.Code)
	}
	res, _ := runOps(t, h, access, "apply-key-1", "labor.apply_activity", args)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "报名成功") {
		t.Fatalf("apply = %d %s", res.Code, res.Body.String())
	}

	if res, _ := runOps(t, h, access, "pay-key-1", "ykt.utility_pay", map[string]any{"money": 1}); res.Code != http.StatusForbidden {
		t.Fatalf("utility pay = %d, want 403", res.Code)
	}
	requireNoSchool(t, tr, "ops")
}

func TestReviewTeacherEvalStaysClosed(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	access := mustLogin(t, h, reviewTestAccount, reviewTestPassword, "review-phone")

	code, out := teachDo(t, h, http.MethodGet, "/v1/me/teacher-eval/status", access, "", nil)
	if code != http.StatusOK || out["open"] != false {
		t.Fatalf("status = %d %v, want closed", code, out)
	}
	if code, _ := teachDo(t, h, http.MethodPost, "/v1/me/teacher-eval/tasks", access, "create-key-1", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("create task = %d, want 403", code)
	}
	requireNoSchool(t, tr, "teacher eval")
}

func TestReviewLogoutNeverReachesSchool(t *testing.T) {
	tr := successTransport(t)
	h := newReviewServer(t, tr)
	access := mustLogin(t, h, reviewTestAccount, reviewTestPassword, "review-phone")
	if res, _ := doJSON(t, h, http.MethodDelete, "/v1/auth/sessions/current", access, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d", res.StatusCode)
	}
	requireNoSchool(t, tr, "logout")
}

// With the review settings absent the same credentials are an ordinary student
// login: the feature is off, not merely quiet.
func TestWithoutReviewAccountTheSameLoginGoesToSchool(t *testing.T) {
	tr := successTransport(t)
	h := newTestServer(t, tr)
	mustLogin(t, h, reviewTestAccount, reviewTestPassword, "phone")
	if len(tr.Recorded()) == 0 {
		t.Fatal("with the review account off, login must reach the school")
	}
}
