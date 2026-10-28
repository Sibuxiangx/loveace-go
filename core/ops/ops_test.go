package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func academicMock(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":10,"coursePas":1,"gpa":3.52,"courseNum_bxqyxd":2,"zxjxjhh":"2024-2025-1-1"}]`)))
}

func TestDispatchRoundTripsAcademicInfo(t *testing.T) {
	s := session.New("20000002", "gw", "school", academicMock(t))
	value, err := Dispatch(context.Background(), s, "jwc.academic_info", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(raw, `"completed_courses":10`) || !jsonContains(raw, `"current_term":"2024-2025-1-1"`) {
		t.Fatalf("value = %s", raw)
	}
}

func TestWriteOperationRequiresPermission(t *testing.T) {
	tr := testutil.LoginTransport(t)
	s := session.New("20000002", "gw", "school", tr)
	params := json.RawMessage(`{
		"area_id":"1","area_name":"a","project_id":"2","project_name":"p",
		"phone":"1","address":"x","description":"d"}`)
	_, err := Dispatch(context.Background(), s, "repair.submit", params)
	var e *errs.Error
	if err == nil || !asErr(err, &e) || e.Code != errs.InvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestDispatchAuthStatus(t *testing.T) {
	s := session.New("20000002", "gw", "school", testutil.LoginTransport(t))
	value, err := Dispatch(context.Background(), s, "auth.status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !jsonContains(raw, `"state":"signed_out"`) {
		t.Fatalf("value = %s", raw)
	}
}

func TestDispatchSessionClearCookies(t *testing.T) {
	s := session.New("20000002", "gw", "school", testutil.LoginTransport(t))
	if err := s.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := Dispatch(context.Background(), s, "session.clear_cookies", json.RawMessage(`{"scope":"all"}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.Inspect().Cookies) != 0 {
		t.Fatal("cookies must be cleared")
	}
	// Unknown scope errors.
	_, err := Dispatch(context.Background(), s, "session.clear_cookies", json.RawMessage(`{"scope":"nope"}`))
	var e *errs.Error
	if !asErr(err, &e) || e.Code != errs.InvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestDispatchDiagnosticsReport(t *testing.T) {
	s := session.New("20000002", "gw", "school", testutil.LoginTransport(t))
	value, err := Dispatch(context.Background(), s, "diagnostics.export_report", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !jsonContains(raw, `"schema_version":1`) || !jsonContains(raw, `"report_id"`) {
		t.Fatalf("value = %s", raw)
	}
}

func TestDispatchUnknownOperation(t *testing.T) {
	s := session.New("20000002", "gw", "school", testutil.LoginTransport(t))
	_, err := Dispatch(context.Background(), s, "door.login", json.RawMessage(`{}`))
	var e *errs.Error
	if !asErr(err, &e) || e.Code != errs.NotImplemented {
		t.Fatalf("err = %v", err)
	}
}

func TestCatalogMatchesRustOperationNames(t *testing.T) {
	// The complete production catalog from Rust operations.rs (debug-tools
	// entries excluded).
	want := []string{
		"auth.login", "auth.ec_login", "auth.ec_captcha", "auth.uaap_login",
		"auth.heartbeat", "auth.status", "auth.logout", "auth.cancel",
		"jwc.academic_info", "jwc.training_plan", "jwc.terms", "jwc.term_scores",
		"jwc.all_term_scores", "jwc.this_term_scores", "jwc.score_detail",
		"jwc.exams", "jwc.seat_info", "jwc.other_exams", "jwc.plan_completion",
		"jwc.plan_options", "jwc.student_schedule", "jwc.course_schedule_terms",
		"jwc.course_schedule", "jwc.course_schedule_all", "jwc.course_schedule_term",
		"jwc.class_terms", "jwc.class_departments", "jwc.class_subjects",
		"jwc.class_list", "jwc.class_curriculum", "jwc.teacher_eval_courses",
		"jwc.teacher_eval_prepare", "jwc.teacher_eval_submit", "jwc.teacher_eval_verify",
		"ykt.init_session", "ykt.balance", "ykt.transactions",
		"ykt.recent_transactions", "ykt.monthly_transactions",
		"ykt.utility_page_info", "ykt.utility_options", "ykt.utility_pay",
		"ykt.purchase_history", "ykt.recent_purchase_history",
		"aac.ticket", "aac.credit_info", "aac.credit_list", "aac.reset_ticket",
		"labor.ticket", "labor.progress", "labor.joined_activities",
		"labor.joined_clubs", "labor.club_activities", "labor.apply_activity",
		"labor.scan_sign_in", "labor.sign_list", "labor.activity_detail",
		"labor.reset_ticket",
		"isim.buildings", "isim.floors", "isim.rooms", "isim.electricity",
		"competition.info",
		"repair.orders", "repair.order_detail", "repair.form_data",
		"repair.upload_image", "repair.submit",
		"manifest.v2", "announcements.list",
		"session.clear_cookies", "session.expire_service_ticket",
		"diagnostics.export_report",
	}
	catalog := Catalog()
	got := map[string]bool{}
	for _, spec := range catalog {
		got[spec.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing operation: %s", name)
		}
	}
	if len(got) != len(want) {
		for name := range got {
			found := false
			for _, w := range want {
				if w == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("extra operation: %s", name)
			}
		}
	}
	// Door access must never appear (scope exclusion).
	for _, spec := range catalog {
		if len(spec.Name) > 4 && spec.Name[:4] == "door" {
			t.Errorf("door operations are excluded: %s", spec.Name)
		}
	}
}

func TestMutationFlags(t *testing.T) {
	if !IsWrite("jwc.teacher_eval_submit") {
		t.Fatal("eval submit must be a write")
	}
	if !IsWrite("ykt.utility_pay") || !IsWrite("repair.submit") || !IsWrite("labor.scan_sign_in") {
		t.Fatal("writes must be flagged")
	}
	if IsWrite("jwc.term_scores") || IsWrite("manifest.v2") {
		t.Fatal("reads must not be flagged as writes")
	}
	// Rust parity: jwc.terms is a school-auth read; manifest is public;
	// auth.status is a school-auth read (not a local control).
	if !RequiresAuth("jwc.terms") || RequiresAuth("manifest.v2") {
		t.Fatal("auth requirements wrong")
	}
	if !RequiresAuth("auth.status") {
		t.Fatal("auth.status requires school auth in the Rust catalog")
	}
}

func jsonContains(raw []byte, sub string) bool {
	return len(sub) > 0 && len(raw) >= len(sub) && indexOf(raw, sub) >= 0
}

func indexOf(raw []byte, sub string) int {
	for i := 0; i+len(sub) <= len(raw); i++ {
		if string(raw[i:i+len(sub)]) == sub {
			return i
		}
	}
	return -1
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
	}
	return ok
}
