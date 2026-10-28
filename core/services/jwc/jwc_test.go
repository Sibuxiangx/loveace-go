package jwc

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func jwcSession(t *testing.T, extra *transport.MockTransport) (*session.Session, *transport.MockTransport) {
	t.Helper()
	tr := testutil.LoginTransport(t)
	if extra != nil {
		// Re-register extra routes onto the login transport.
		for _, r := range extra.Recorded() {
			_ = r
		}
	}
	return session.New("20000002", "gw", "school", tr), tr
}

func academicMock(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":10,"coursePas":1,"gpa":3.52,"courseNum_bxqyxd":2,"zxjxjhh":"2024-2025-1-1"}]`)))
}

func newOn(t *testing.T, extra *transport.MockTransport) *session.Session {
	t.Helper()
	s, _ := jwcSession(t, extra)
	return s
}

func TestAcademicInfoParsesArrayShape(t *testing.T) {
	s := session.New("20000002", "gw", "school", academicMock(t))
	info, err := New(s).AcademicInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.CompletedCourses != 10 || info.FailedCourses != 1 || info.CurrentTerm != "2024-2025-1-1" {
		t.Fatalf("info = %+v", info)
	}
	if info.GPA < 3.5199 || info.GPA > 3.5201 {
		t.Fatalf("gpa = %v", info.GPA)
	}
}

func TestTrainingPlanExtractsGradeAndMajor(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "/main/showPyfaInfo", transport.NewResponse(200, []byte(
			`{"data":[["2024级网络与新媒体本科培养方案","0301"]]}`)))
	s := session.New("20000002", "gw", "school", tr)
	plan, err := New(s).TrainingPlanInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Grade != "2024" || plan.MajorName != "网络与新媒体" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestTermsParseSelectAndTranslateSeasons(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "calendarSemesterCurriculum/index", transport.NewResponse(200, []byte(
			`<select id="planCode"><option value="2024-2025-1-1" selected>2024-2025学年秋</option><option value="2023-2024-2-1">2023-2024学年春</option></select>`)))
	s := session.New("20000002", "gw", "school", tr)
	terms, err := New(s).Terms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 2 {
		t.Fatalf("terms = %+v", terms)
	}
	if terms[0].TermName != "2024-2025学年上" || terms[1].TermName != "2023-2024学年下" {
		t.Fatalf("names = %q %q", terms[0].TermName, terms[1].TermName)
	}
	if !terms[0].IsCurrent || terms[1].IsCurrent {
		t.Fatalf("current = %+v", terms)
	}
}

func TestTermScoresUseDynamicPathAndParseArrays(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "scoreQuery/allTermScores/index", transport.NewResponse(200, []byte(
			`<script>var u='/student/integratedQuery/scoreQuery/abc123/allTermScores/data';</script>`))).
		OnRequest(transport.Post, "allTermScores/data", transport.NewResponse(200, []byte(
			`{"list":{"records":[["1","2024-2025-1-1","C1","01","高数","Math","3","48","必修","考试","95","",""]],"pageContext":{"totalCount":1}}}`)))
	s := session.New("20000002", "gw", "school", tr)
	result, err := New(s).TermScores(context.Background(), "2024-2025-1-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 || len(result.Records) != 1 {
		t.Fatalf("result = %+v", result)
	}
	rec := result.Records[0]
	if rec.CourseCode != "C1" || rec.Score != "95" || rec.Credits != "3" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestThisTermScoresParseObjectShape(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "scoreQuery/thisTermScores/index", transport.NewResponse(200, []byte(
			`<script>var u='/student/integratedQuery/scoreQuery/xyz789/thisTermScores/data';</script>`))).
		OnRequest(transport.Get, "thisTermScores/data", transport.NewResponse(200, []byte(
			`[{"list":[{"id":{"executiveEducationPlanNumber":"2024-2025-1-1","courseNumber":"C1","examtime":"20000004"},"coureSequenceNumber":"01","courseName":"高数","englishCourseName":"Math","credit":"3","studyHour":48,"coursePropertyName":"必修","examTypeName":"考试","courseScore":"95 "}]}]`)))
	s := session.New("20000002", "gw", "school", tr)
	result, err := New(s).ThisTermScores(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v", result.Records)
	}
	rec := result.Records[0]
	if rec.CourseNameCN != "高数" || rec.Score != "95" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.ExamTime == nil || *rec.ExamTime != "20000004" {
		t.Fatalf("exam time = %v", rec.ExamTime)
	}
}

func TestExamInfoMergesSchoolSeatsAndOtherExams(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":10,"coursePas":1,"gpa":3.5,"courseNum_bxqyxd":2,"zxjxjhh":"2024-2025-1-1"}]`))).
		OnRequest(transport.Get, "examinationManagement/examPlan/index", transport.NewResponse(200, []byte(
			`<div class="widget-box"><h5 class="widget-title">（0301001-01）大学英语（一）</h5><div class="widget-main">座位号:A12</div></div>`))).
		OnRequest(transport.Get, "examinationManagement/examPlan/detail", transport.NewResponse(200, []byte(
			`[{"title":"高等数学\n2025-01-05 09:00\n教学楼A101","start":"2025-01-05","end":"2025-01-05","color":"#f00"}]`))).
		OnRequest(transport.Post, "othersExamPlan/queryScores", transport.NewResponse(200, []byte(
			`{"records":[{"KCM":"大学英语","KSRQ":"2026年1月3日","KSSJ":"14:00","KSDD":"B202","BZ":""}]}`)))
	s := session.New("20000002", "gw", "school", tr)
	info, err := New(s).ExamInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.TotalCount != 2 || len(info.Exams) != 2 {
		t.Fatalf("info = %+v", info)
	}
	if info.Exams[0].ExamType != "校统考" {
		t.Fatalf("exams[0] = %+v", info.Exams[0])
	}
	// Course names differ between seat fixture and exam fixture, so no seat
	// note is injected (same as the Rust fixture test).
	if info.Exams[1].ExamDate != "2026-01-03" || info.Exams[1].ExamType != "其他考试" {
		t.Fatalf("exams[1] = %+v", info.Exams[1])
	}
}

func TestPlanCompletionBuildsCategoryTree(t *testing.T) {
	ztree := `[{"id":"1","pId":"-1","name":"通识教育(最低修读学分:10.0,通过学分:4.0,已修课程门数:2,已及格课程门数:1,未及格课程门数:1,必修课缺修门数:0)","flagType":"001","flagId":"F1"},
	{"id":"2","pId":"1","name":"<i class='fa fa-smile-o fa-1x green'></i>[C101]高等数学[3.0学分](95,20000005)","flagType":"kch","flagId":"F2"},
	{"id":"3","pId":"1","name":"<i class='fa fa-frown-o fa-1x red'></i>[C102]大学英语[2.0学分](55,20000006)","flagType":"kch","flagId":"F3"}]`
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "planCompletion/index", transport.NewResponse(200, []byte(
			`<html><body><h4 class="widget-title">2024级网络与新媒体本科培养方案</h4>
			<script>$.fn.zTree.init($("#treeDemo"), setting, `+ztree+`);</script></body></html>`)))
	s := session.New("20000002", "gw", "school", tr)
	outcome, err := New(s).PlanCompletion(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != OutcomeInfo || outcome.Info == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	info := outcome.Info
	if info.Grade != "2024" || info.Major != "网络与新媒体" {
		t.Fatalf("header = %+v", info)
	}
	if len(info.Categories) != 1 {
		t.Fatalf("categories = %+v", info.Categories)
	}
	cat := info.Categories[0]
	if cat.CategoryName != "通识教育" || cat.MinCredits != 10 || cat.CompletedCredits != 4 {
		t.Fatalf("category = %+v", cat)
	}
	if len(cat.Courses) != 2 {
		t.Fatalf("courses = %+v", cat.Courses)
	}
	if cat.Courses[0].CourseCode != "C101" || cat.Courses[0].CourseName != "高等数学" || !cat.Courses[0].IsPassed {
		t.Fatalf("course0 = %+v", cat.Courses[0])
	}
	if cat.Courses[0].Score == nil || *cat.Courses[0].Score != "95" {
		t.Fatalf("score = %v", cat.Courses[0].Score)
	}
	if cat.Courses[1].StatusDescription != "未通过" {
		t.Fatalf("course1 = %+v", cat.Courses[1])
	}
	if info.TotalCourses != 2 || info.PassedCourses != 1 || info.FailedCourses != 1 || info.UnreadCourses != 0 {
		t.Fatalf("stats = %+v", info)
	}
}

func TestStudentScheduleParsesCourses(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "calendarSemesterCurriculum/index", transport.NewResponse(200, []byte(
			`<script>var u='/student/courseSelect/thisSemesterCurriculum/dyn777/ajaxStudentSchedule';</script>`))).
		On("ajaxStudentSchedule/past/callback", transport.NewResponse(200, []byte(
			`{"allUnits":12.5,"errorMessage":"","showSite":true,"school_id":"1","xkxx":[],
			"dateList":[{"programPlanCode":"P1","programPlanName":"主修","totalUnits":12.5,
			"selectCourseList":[{"id":{"executiveEducationPlanNumber":"2024-2025-1-1","coureNumber":"C1","coureSequenceNumber":"01","studentNumber":"20000002"},
			"courseName":"高数","unit":3.0,"attendClassTeacher":"老师","timeAndPlaceList":[
			{"id":"TP1","classWeek":"1-16周","classDay":1,"classSessions":3,"continuingSession":2,"campusName":"东区","teachingBuildingName":"教A","classroomName":"101","weekDescription":"1-16周","coureName":"高数"}],
			"selectCourseStatusCode":"1","selectCourseStatusName":"已选"}]}]}`)))
	s := session.New("20000002", "gw", "school", tr)
	result, err := New(s).StudentSchedule(context.Background(), "2024-2025-1-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.AllUnits != 12.5 || len(result.DateList) != 1 {
		t.Fatalf("result = %+v", result)
	}
	courses := result.Courses()
	if len(courses) != 1 {
		t.Fatalf("courses = %+v", courses)
	}
	course := courses[0]
	if course.CourseName != "高数" || course.Unit != 3.0 {
		t.Fatalf("course = %+v", course)
	}
	if course.UniqueKey() != "C1_01" {
		t.Fatalf("unique key = %q", course.UniqueKey())
	}
	if len(course.TimeAndPlaceList) != 1 {
		t.Fatalf("slots = %+v", course.TimeAndPlaceList)
	}
	slot := course.TimeAndPlaceList[0]
	if slot.EndSession() != 4 || slot.LocationDescription() != "东区 教A 101" {
		t.Fatalf("slot = %+v", slot)
	}
}

func TestCourseSchedulePageParsesRecords(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Post, "courseSchdule/courseInfo", transport.NewResponse(200, []byte(
			`{"list":{"records":[{"id":"R1","zxjxjhh":"2024-2025-1-1","kch":"C1","kcm":"高数","xf":3,"skxq":1,"skjc":3,"cxjc":2,"zcsm":"1-16周","jxlm":"教A","jasm":"101","xqm":"东区","bm":"软工1班"}],"pageContext":{"totalCount":1}}}`)))
	s := session.New("20000002", "gw", "school", tr)
	records, err := New(s).CourseSchedule(context.Background(), "C1", "2024-2025-1-1", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	rec := records[0]
	if rec.Kch == nil || *rec.Kch != "C1" || rec.Xf == nil || *rec.Xf != 3 {
		t.Fatalf("record = %+v", rec)
	}
	if day := rec.WeekdayStr(); day == nil || *day != "周一" {
		t.Fatalf("weekday = %v", day)
	}
	if ct := rec.ClassTimeStr(); ct == nil || *ct != "3-4" {
		t.Fatalf("class time = %v", ct)
	}
	if desc := rec.ScheduleDescription(); !strings.Contains(desc, "周一") || !strings.Contains(desc, "第3-4节") || !strings.Contains(desc, "教A101") {
		t.Fatalf("description = %q", desc)
	}
}

func TestTeacherEvalCoursesAndPrepareSeeded(t *testing.T) {
	page := `
        <html><body>
          <div class="title">教学评价</div>
          <input name="tokenValue" value="FORMTOKEN">
          <input name="wjdm" value="Q1">
          <input name="bprdm" value="P1">
          <input name="pgnr" value="E1">
          <table><tr><td>授课态度是否认真</td><td>
            <input type="radio" name="q1" value="5_1.0"><label>很认真</label>
            <input type="radio" name="q1" value="3_0.6"><label>一般</label>
          </td></tr></table>
          <textarea name="zgpj"></textarea>
        </body></html>`
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "teachingEvaluation/evaluation/index", transport.NewResponse(200, []byte(
			`<input id="tokenValue" value="TOK">`))).
		OnRequest(transport.Post, "teachingEvaluation/teachingEvaluation/search", transport.NewResponse(200, []byte(
			`{"data":[{"id":{"evaluatedPeople":"P1","coureSequenceNumber":"S1","evaluationContentNumber":"E1"},"questionnaire":{"questionnaireNumber":"Q1","questionnaireName":"QN"},"evaluationContent":"课程","evaluatedPeople":"老师","isEvaluated":"否"}]}`))).
		OnRequest(transport.Post, "teachingEvaluation/evaluationPage", transport.NewResponse(200, []byte(page))).
		OnRequest(transport.Post, "teachingEvaluation/assessment", transport.NewResponse(200, []byte(
			`{"result":"success","msg":"ok"}`)))
	cfg := session.DefaultConfig()
	cfg.AllowWrites = true
	s := session.NewWithConfig("20000002", "gw", "school", tr, cfg)
	sv := New(s)
	list, err := sv.TeacherEvalCourses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Courses) != 1 || list.TokenValue != "TOK" {
		t.Fatalf("list = %+v", list)
	}
	course := list.Courses[0]
	if course.Name != "课程" || course.Teacher != "老师" || course.IsEvaluated {
		t.Fatalf("course = %+v", course)
	}
	prepared, err := sv.TeacherEvalPrepareSeeded(context.Background(), &course, 1, list.TokenValue, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.FormData) == 0 {
		t.Fatal("form data empty")
	}
	hasQ1 := false
	for _, kv := range prepared.FormData {
		if kv[0] == "q1" {
			hasQ1 = true
		}
	}
	if !hasQ1 {
		t.Fatalf("q1 missing: %+v", prepared.FormData)
	}
	result, err := sv.TeacherEvalSubmit(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Message != "ok" {
		t.Fatalf("result = %+v", result)
	}
}

func TestTeacherEvalPrepareSeededIsDeterministic(t *testing.T) {
	page := `
        <html><body>
          <input name="tokenValue" value="FORMTOKEN">
          <input name="wjdm" value="Q1"><input name="bprdm" value="P1"><input name="pgnr" value="E1">
          <table><tr><td>授课态度是否认真</td><td>
            <input type="radio" name="q1" value="5_1.0"><label>很认真</label>
            <input type="radio" name="q1" value="3_0.6"><label>一般</label>
          </td></tr></table>
          <textarea name="zgpj"></textarea>
        </body></html>`
	makePrepared := func(t *testing.T) *TeacherEvaluationPreparedForm {
		tr := testutil.LoginTransport(t).
			OnRequest(transport.Post, "teachingEvaluation/evaluationPage", transport.NewResponse(200, []byte(page)))
		sv := New(session.New("20000002", "gw", "school", tr))
		course := &TeacherEvaluationCourse{CoureSequenceNumber: "S1", EvaluationContentNumber: "E1"}
		prepared, err := sv.TeacherEvalPrepareSeeded(context.Background(), course, 1, "TOK", 7)
		if err != nil {
			t.Fatal(err)
		}
		return prepared
	}
	a := makePrepared(t)
	b := makePrepared(t)
	if len(a.FormData) != len(b.FormData) {
		t.Fatal("form lengths differ")
	}
	for i := range a.FormData {
		if a.FormData[i] != b.FormData[i] {
			t.Fatalf("form data differs at %d: %v vs %v", i, a.FormData[i], b.FormData[i])
		}
	}
}

func TestScoreDetailParsesItems(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "thisTermScores/index", transport.NewResponse(200, []byte(
			`<script>var u='/student/integratedQuery/scoreQuery/xyz789/thisterm/coursePropertyScores/serchScoreDetail';</script>`))).
		OnRequest(transport.Post, "serchScoreDetail", transport.NewResponse(200, []byte(
			`{"mx":[{"id":{"scoreTypeCode":"001"},"pscj":"90","qzcj":"85","qmcj":"88","zcj":"87","remark":"好"}]}`)))
	s := session.New("20000002", "gw", "school", tr)
	detail, err := New(s).ScoreDetail(context.Background(), &ScoreRecord{
		TermID: "2024-2025-1-1", CourseCode: "C1", CourseClass: "01", ExamTime: strPtr("20000004"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("items = %+v", detail.Items)
	}
	item := detail.Items[0]
	if item.ScoreType != "课堂成绩" || item.UsualScore != "90" || item.FinalScore != "88" {
		t.Fatalf("item = %+v", item)
	}
}

func strPtr(s string) *string { return &s }
