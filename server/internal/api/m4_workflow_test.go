package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// TestCalendarPublishWorkflow pins §11.1's admin flow: rules are drafted,
// previewable, then published (version bump + ETag change); the public
// view only ever renders the published set; drafts are deletable,
// published rules are not.
func TestCalendarPublishWorkflow(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "adm-secret")

	res, _ := doJSON(t, h, http.MethodPut, "/v1/admin/calendar/terms", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "first_week_monday": "2026-09-07", "weeks": 20})
	if res.StatusCode != 200 {
		t.Fatalf("term upsert = %d", res.StatusCode)
	}
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/rules", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "day": "2026-10-01", "kind": "holiday"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("draft rule = %d", res.StatusCode)
	}

	// Draft: the preview shows the holiday, the public view does not.
	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/admin/calendar/preview?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-01", "adm-secret", nil)
	days, _ := rawOut.([]any)
	if res.StatusCode != 200 || len(days) != 1 || asMap(t, days[0])["kind"] != "holiday" {
		t.Fatalf("preview = %d %v", res.StatusCode, rawOut)
	}
	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/public/calendar?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-01", "", nil)
	days, _ = rawOut.([]any)
	if res.StatusCode != 200 || len(days) != 1 || asMap(t, days[0])["kind"] != "teaching" {
		t.Fatalf("public before publish = %d %v", res.StatusCode, rawOut)
	}

	// Publish: version 1, public flips, ETag present.
	res, out := doJSON(t, h, http.MethodPost, "/v1/admin/calendar/publish", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1"})
	outMap := asMap(t, out)
	if res.StatusCode != 200 || outMap["version"] != float64(1) {
		t.Fatalf("publish = %d %v", res.StatusCode, outMap)
	}
	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/public/calendar?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-01", "", nil)
	days, _ = rawOut.([]any)
	if asMap(t, days[0])["kind"] != "holiday" {
		t.Fatalf("public after publish = %v", days[0])
	}
	etagV1 := res.Header.Get("ETag")
	if etagV1 == "" {
		t.Fatal("publish did not produce an ETag")
	}

	// Conditional request: matching If-None-Match → 304.
	req := httptest.NewRequest(http.MethodGet, "/v1/public/calendar?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-01", nil)
	req.Header.Set("If-None-Match", etagV1)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match = %d, want 304", rec.Code)
	}

	// Drafts are deletable; published rules are not.
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/rules", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "day": "2026-12-25", "kind": "vacation"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("second draft = %d", res.StatusCode)
	}

	// A second publish retires version 1's representation: the ETag moves.
	res, out = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/publish", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1"})
	outMap = asMap(t, out)
	if outMap["version"] != float64(2) {
		t.Fatalf("second publish version = %v", outMap)
	}
	res, rawOut = doJSON(t, h, http.MethodGet, "/v1/public/calendar?term_id=2026-2027-1-1&from=2026-10-01&to=2026-10-01", "", nil)
	if res.StatusCode != 200 || res.Header.Get("ETag") == etagV1 {
		t.Fatalf("ETag did not advance: %q", res.Header.Get("ETag"))
	}
	_ = rawOut
}

// TestMergedTimetable pins §11.1's merged view: a holiday day carries no
// classes; a makeup day shows the followed week/weekday's classes.
func TestMergedTimetable(t *testing.T) {
	tr := successTransport(t)
	// The schedule endpoints (not part of successTransport): pre-page with
	// the dynamic path, then the schedule document (school camelCase).
	tr.OnRequest(transport.Get, "/student/courseSelect/calendarSemesterCurriculum/index",
		transport.NewResponse(200, []byte(
			`<html>/student/courseSelect/thisSemesterCurriculum/dyn42/ajaxStudentSchedule</html>`)))
	tr.OnRequest(transport.Post, "ajaxStudentSchedule/past/callback", transport.NewResponse(200, []byte(
		`{"allUnits":3,"errorMessage":"","dateList":[{"programPlanName":"p",
		  "selectCourseList":[{"id":{"coureNumber":"C1","coureSequenceNumber":"01",
		  "studentNumber":"20000003"},"courseName":"高等数学","unit":4.0,
		  "attendClassTeacher":"张三","selectCourseStatusName":"已选",
		  "timeAndPlaceList":[{"id":"x","classWeek":"1-16","classDay":1,
		  "classSessions":1,"continuingSession":2,"campusName":"校本部",
		  "teachingBuildingName":"一教","classroomName":"101","coureName":"高等数学"}]}]}]}`)))
	// Exam chain: empty seat page, empty school-exam calendar, one
	// "other exam" on 2027-01-10 — AFTER teaching ends (week 19): the
	// case the raw timetable never showed.
	tr.OnRequest(transport.Get, "/student/examinationManagement/examPlan/index",
		transport.NewResponse(200, []byte(`<html></html>`)))
	tr.OnRequest(transport.Get, "/student/examinationManagement/examPlan/detail",
		transport.NewResponse(200, []byte(``)))
	tr.OnRequest(transport.Post, "othersExamPlan/queryScores", transport.NewResponse(200, []byte(
		`{"records":[["1","20000003","大学英语（期末）","E1","2027年1月10日","09:00-11:00","二教 203","闭卷"]]}`)))
	_, h := newGroupTestServer(t, tr, "adm-secret")

	// Fixture course: 高等数学, weeks 1-16, Monday, sessions 1-2.
	res, _ := doJSON(t, h, http.MethodPut, "/v1/admin/calendar/terms", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1", "first_week_monday": "2026-09-07", "weeks": 20})
	if res.StatusCode != 200 {
		t.Fatalf("term = %d", res.StatusCode)
	}
	// 2026-10-05 (Monday, week 5) is a holiday; 2026-10-06 (Tuesday) is a
	// makeup day following week 4's Monday timetable.
	for _, rule := range []map[string]any{
		{"term_id": "2026-2027-1-1", "day": "2026-10-05", "kind": "holiday"},
		{"term_id": "2026-2027-1-1", "day": "2026-10-06", "kind": "makeup", "follow_week": 4, "follow_weekday": 1},
	} {
		res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/rules", "adm-secret", rule)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("rule %v = %d", rule, res.StatusCode)
		}
	}
	res, _ = doJSON(t, h, http.MethodPost, "/v1/admin/calendar/publish", "adm-secret",
		map[string]any{"term_id": "2026-2027-1-1"})
	if res.StatusCode != 200 {
		t.Fatalf("publish = %d", res.StatusCode)
	}

	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	// Span teaching weeks and the exam season: 2026-10-05 (holiday) …
	// 2027-01-10 (the exam, week 19 — beyond the 1-16 teaching weeks).
	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/me/academic/timetable/merged?term=2026-2027-1-1&from=2026-10-05&to=2027-01-10", access, nil)
	days, ok := rawOut.([]any)
	if res.StatusCode != 200 || !ok || len(days) != 98 {
		t.Fatalf("merged = %d n=%d", res.StatusCode, len(days))
	}
	holiday := asMap(t, days[0])
	if holiday["kind"] != "holiday" {
		t.Fatalf("day1 = %v", holiday)
	}
	if classes, _ := holiday["classes"].([]any); len(classes) != 0 {
		t.Fatalf("holiday carried classes: %v", classes)
	}
	makeup := asMap(t, days[1])
	if makeup["kind"] != "makeup" {
		t.Fatalf("day2 = %v", makeup)
	}
	classes, _ := makeup["classes"].([]any)
	if len(classes) != 1 {
		t.Fatalf("makeup day classes = %v, want the followed Monday's 高等数学", classes)
	}
	cls := asMap(t, classes[0])
	if cls["course_name"] != "高等数学" || cls["start_session"] != float64(1) || cls["end_session"] != float64(2) {
		t.Fatalf("makeup class = %v", cls)
	}
	// THE FIX: the exam on 2027-01-10 (week 19, no classes anymore) shows
	// up on its own date — the raw timetable never displayed these.
	examDay := asMap(t, days[len(days)-1])
	if examDay["date"] != "2027-01-10" {
		t.Fatalf("last day = %v", examDay)
	}
	exams, _ := examDay["exams"].([]any)
	if len(exams) != 1 {
		t.Fatalf("exam missing on 2027-01-10: %v", examDay)
	}
	exam := asMap(t, exams[0])
	if exam["course_name"] != "大学英语（期末）" || exam["time"] != "09:00-11:00" ||
		exam["exam_type"] != "其他考试" || exam["location"] != "二教 203" {
		t.Fatalf("exam view = %v", exam)
	}
}
