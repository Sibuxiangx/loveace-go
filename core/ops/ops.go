// Package ops implements the operation catalog and JSON dispatch.
//
// The catalog is the single source of truth for CLI and future-server
// operation names, argument schemas, auth requirements and mutation flags.
// Dispatch maps an operation name plus a JSON parameter object onto a typed
// core method.
package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Sibuxiangx/loveace-go/core/diagnostics"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"

	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/services/aac"
	"github.com/Sibuxiangx/loveace-go/core/services/competition"
	"github.com/Sibuxiangx/loveace-go/core/services/isim"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/core/services/labor"
	"github.com/Sibuxiangx/loveace-go/core/services/manifest"
	"github.com/Sibuxiangx/loveace-go/core/services/repair"
	"github.com/Sibuxiangx/loveace-go/core/services/ykt"
)

// ArgType is the argument scalar type.
type ArgType string

// Argument types (Rust serde snake_case spellings).
const (
	ArgString      ArgType = "string"
	ArgInteger     ArgType = "integer"
	ArgBoolean     ArgType = "boolean"
	ArgObject      ArgType = "object"
	ArgStringArray ArgType = "string_array"
)

// ArgSpec describes one operation argument.
type ArgSpec struct {
	Name     string  `json:"name"`
	ArgType  ArgType `json:"arg_type"`
	Required bool    `json:"required"`
}

// Spec is the metadata for one operation.
type Spec struct {
	Name         string    `json:"name"`
	Summary      string    `json:"summary"`
	RequiresAuth bool      `json:"requires_auth"`
	Mutates      bool      `json:"mutates"`
	Args         []ArgSpec `json:"args"`

	// Handler executes the operation. Nil for catalog-only entries.
	Handler func(ctx context.Context, s *session.Session, params json.RawMessage) (any, error) `json:"-"`
}

func req(name string, t ArgType) ArgSpec { return ArgSpec{Name: name, ArgType: t, Required: true} }
func opt(name string, t ArgType) ArgSpec { return ArgSpec{Name: name, ArgType: t} }

func readOp(name, summary string, args ...ArgSpec) Spec {
	return Spec{Name: name, Summary: summary, RequiresAuth: true, Args: args}
}

func publicRead(name, summary string, args ...ArgSpec) Spec {
	s := readOp(name, summary, args...)
	s.RequiresAuth = false
	return s
}

func writeOp(name, summary string, args ...ArgSpec) Spec {
	s := readOp(name, summary, args...)
	s.Mutates = true
	return s
}

func localControl(name, summary string, args ...ArgSpec) Spec {
	s := publicRead(name, summary, args...)
	s.RequiresAuth = false
	s.Mutates = false
	return s
}

// Catalog returns the complete operation catalog (production entries only;
// debug-tools fault injection is behind the debugtools build tag).
func Catalog() []Spec {
	var ops []Spec
	ops = append(ops, authOps()...)
	ops = append(ops, jwcOps()...)
	ops = append(ops, yktOps()...)
	ops = append(ops, aacLaborOps()...)
	ops = append(ops, competitionRepairOps()...)
	ops = append(ops, manifestOps()...)
	ops = append(ops, maintenanceOps()...)
	return ops
}

func authOps() []Spec {
	return []Spec{
		readOp("auth.login", "Gateway + identity login"),
		readOp("auth.ec_login", "Gateway (EC) login only (optional CAPTCHA code)", opt("captcha", ArgString)),
		localControl("auth.ec_captcha", "Fetch the EC CAPTCHA requirement and image"),
		readOp("auth.uaap_login", "Identity (UAAP/CAS) login only"),
		readOp("auth.heartbeat", "Gateway heartbeat probe"),
		readOp("auth.status", "Redacted session inspection"),
		writeOp("auth.logout", "Sign out and clear session state"),
		writeOp("auth.cancel", "Cancel in-flight work"),
	}
}

func jwcOps() []Spec {
	return []Spec{
		readOp("jwc.academic_info", "Academic overview"),
		readOp("jwc.training_plan", "Training-plan summary"),
		readOp("jwc.terms", "Term list"),
		readOp("jwc.term_scores", "Scores for one term", req("term_code", ArgString)),
		readOp("jwc.all_term_scores", "Scores across terms", req("term_codes", ArgStringArray)),
		readOp("jwc.this_term_scores", "Current-term scores"),
		readOp("jwc.score_detail", "Score detail for a record",
			req("term_id", ArgString), req("course_code", ArgString),
			req("course_class", ArgString), opt("exam_time", ArgString)),
		readOp("jwc.exams", "Aggregated exam schedule"),
		readOp("jwc.seat_info", "Exam seat information"),
		readOp("jwc.other_exams", "Other exam records", req("term_code", ArgString)),
		readOp("jwc.plan_completion", "Plan completion or selection", opt("plan_id", ArgString)),
		readOp("jwc.plan_options", "Selectable plans"),
		readOp("jwc.student_schedule", "Student schedule for a term", req("term_code", ArgString)),
		readOp("jwc.course_schedule_terms", "Course-query terms"),
		readOp("jwc.course_schedule", "Course offering by course code",
			req("course_code", ArgString), req("term_code", ArgString),
			opt("page_num", ArgInteger), opt("page_size", ArgInteger)),
		readOp("jwc.course_schedule_all", "All offerings for a course",
			req("course_code", ArgString), req("term_code", ArgString)),
		readOp("jwc.course_schedule_term", "All offerings for a term", req("term_code", ArgString)),
		readOp("jwc.class_terms", "Class-curriculum terms"),
		readOp("jwc.class_departments", "Class-curriculum departments"),
		readOp("jwc.class_subjects", "Subjects for a department", req("department_code", ArgString)),
		readOp("jwc.class_list", "Classes for a department/subject",
			req("plan_code", ArgString), req("department_code", ArgString), opt("subject_code", ArgString)),
		readOp("jwc.class_curriculum", "Class curriculum",
			req("plan_code", ArgString), req("class_code", ArgString)),
		readOp("jwc.teacher_eval_courses", "Teacher-evaluation courses"),
		writeOp("jwc.teacher_eval_prepare", "Prepare an evaluation form",
			req("course", ArgObject), req("pending_count", ArgInteger), req("index_token", ArgString),
			opt("seed", ArgInteger)),
		writeOp("jwc.teacher_eval_submit", "Submit an evaluation form",
			req("course", ArgObject), req("questionnaire_title", ArgString), req("form_data", ArgObject)),
		readOp("jwc.teacher_eval_verify", "Verify a course is evaluated", req("course", ArgObject)),
	}
}

func yktOps() []Spec {
	return []Spec{
		readOp("ykt.init_session", "Establish campus-card session"),
		readOp("ykt.balance", "Campus-card balance"),
		readOp("ykt.transactions", "Transactions in a date range",
			req("start_date", ArgString), req("end_date", ArgString)),
		readOp("ykt.recent_transactions", "Transactions over 7 days"),
		readOp("ykt.monthly_transactions", "Transactions over 30 days"),
		readOp("ykt.utility_page_info", "Recharge page info"),
		readOp("ykt.utility_options", "Recharge options",
			req("dorm_id", ArgString), req("building_id", ArgString),
			req("floor_id", ArgString), req("dorm_name", ArgString)),
		writeOp("ykt.utility_pay", "Electricity recharge",
			req("room_id", ArgString), req("dorm_id", ArgString), req("dorm_name", ArgString),
			req("build_name", ArgString), req("floor_name", ArgString), req("room_name", ArgString),
			req("acc_id", ArgString), req("balances", ArgString), req("money", ArgInteger),
			opt("pay_type", ArgString), opt("choose_pay_type", ArgString)),
		readOp("ykt.purchase_history", "Electricity purchase history",
			req("start_date", ArgString), req("end_date", ArgString)),
		readOp("ykt.recent_purchase_history", "Purchase history over 7 days"),
	}
}

func aacLaborOps() []Spec {
	return []Spec{
		readOp("aac.ticket", "AAC ticket readiness", opt("force_refresh", ArgBoolean)),
		readOp("aac.credit_info", "AAC total credit"),
		readOp("aac.credit_list", "AAC credit detail"),
		writeOp("aac.reset_ticket", "Reset cached AAC ticket"),
		readOp("labor.ticket", "Labor ticket readiness", opt("force_refresh", ArgBoolean)),
		readOp("labor.progress", "Labor-course progress"),
		readOp("labor.joined_activities", "Joined activities"),
		readOp("labor.joined_clubs", "Joined clubs"),
		readOp("labor.club_activities", "Activities for a club",
			req("club_id", ArgString), opt("page_index", ArgInteger), opt("page_size", ArgInteger)),
		writeOp("labor.apply_activity", "Apply to an activity",
			req("activity_id", ArgString), opt("reason", ArgString)),
		writeOp("labor.scan_sign_in", "QR sign-in",
			req("content", ArgString), req("location", ArgString)),
		readOp("labor.sign_list", "Sign-in list",
			req("activity_id", ArgString), opt("type", ArgInteger),
			opt("page_index", ArgInteger), opt("page_size", ArgInteger)),
		readOp("labor.activity_detail", "Activity detail", req("activity_id", ArgString)),
		writeOp("labor.reset_ticket", "Reset cached labor ticket"),
		readOp("isim.buildings", "Dormitory buildings"),
		readOp("isim.floors", "Floors for a building", req("building_code", ArgString)),
		readOp("isim.rooms", "Rooms for a floor", req("floor_code", ArgString)),
		readOp("isim.electricity", "Electricity balance and records for a room",
			req("room_code", ArgString), opt("display_text", ArgString)),
	}
}

func competitionRepairOps() []Spec {
	return []Spec{
		readOp("competition.info", "Competition awards and credits"),
		readOp("repair.orders", "Repair orders"),
		readOp("repair.order_detail", "Repair order detail", req("task_id", ArgString)),
		readOp("repair.form_data", "Repair area/project data"),
		writeOp("repair.upload_image", "Upload a repair image", req("data", ArgString)),
		writeOp("repair.submit", "Submit a repair order",
			req("area_id", ArgString), req("area_name", ArgString),
			req("project_id", ArgString), req("project_name", ArgString),
			req("phone", ArgString), req("address", ArgString),
			req("description", ArgString), opt("pic_urls", ArgString)),
	}
}

func manifestOps() []Spec {
	return []Spec{
		publicRead("manifest.v2", "Fetch Manifest v2"),
		publicRead("announcements.list", "Visible announcements", opt("platform", ArgString)),
	}
}

// maintenanceOps are the production maintenance/diagnostic controls. These are
// deliberately not feature-gated: they operate only on the local session.
func maintenanceOps() []Spec {
	return []Spec{
		localControl("session.clear_cookies", "Clear cookies by scope (preserves credentials)",
			req("scope", ArgString)),
		localControl("session.expire_service_ticket", "Expire service-local tickets (preserves gateway login)"),
		localControl("diagnostics.export_report", "Assemble a sanitized local diagnostic report"),
	}
}

// Find returns the spec for an operation name.
func Find(name string) (Spec, bool) {
	for _, spec := range Catalog() {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

// IsWrite reports whether an operation mutates server state.
func IsWrite(name string) bool {
	spec, ok := Find(name)
	return ok && spec.Mutates
}

// RequiresAuth reports whether an operation needs school authentication.
func RequiresAuth(name string) bool {
	spec, ok := Find(name)
	return ok && spec.RequiresAuth
}

// Dispatch runs an operation against a session.
//
// Sets the session's operation context for the duration so HTTP debug events
// carry the operation name, then clears it. One logical operation emits
// exactly one KindOperation observation (the health layer's sample unit,
// DESIGN §9) — retry/backoff inside the operation stays invisible.
func Dispatch(ctx context.Context, s *session.Session, operation string, params json.RawMessage) (any, error) {
	s.SetOperationContext(operation)
	defer s.SetOperationContext("")
	startedMS := timeutil.NowMillis()
	data, err := dispatchInner(ctx, s, operation, params)
	outcome := "success"
	if err != nil {
		outcome = "failure:" + errs.CodeOf(err).String()
	}
	s.Emit(events.KindOperation, operation, outcome, timeutil.NowMillis()-startedMS)
	return data, err
}

// ---- argument accessors (Rust dispatch parity) ----

func paramsMap(params json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(params) == 0 {
		return m
	}
	_ = json.Unmarshal(params, &m)
	return m
}

func strArg(params json.RawMessage, key string) (string, error) {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return "", errs.NewInvalidArgument("missing required argument: " + key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errs.NewInvalidArgument("argument " + key + " must be a string")
	}
	return s, nil
}

func optStrArg(params json.RawMessage, key string) *string {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	if s == "" {
		return nil
	}
	return &s
}

func i64Arg(params json.RawMessage, key string) (int64, error) {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return 0, errs.NewInvalidArgument("missing required argument: " + key)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	return 0, errs.NewInvalidArgument("argument " + key + " must be an integer")
}

func i64ArgOr(params json.RawMessage, key string, fallback int64) int64 {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return fallback
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	return fallback
}

func boolArgOr(params json.RawMessage, key string, fallback bool) bool {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return fallback
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return fallback
}

func objectArg(params json.RawMessage, key string) (json.RawMessage, error) {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return nil, errs.NewInvalidArgument("missing required argument: " + key)
	}
	return raw, nil
}

func stringArrayArg(params json.RawMessage, key string) ([]string, error) {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return nil, errs.NewInvalidArgument("missing required argument: " + key)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errs.NewInvalidArgument("argument " + key + " must be a string array")
	}
	return out, nil
}

func optI64Arg(params json.RawMessage, key string) *int64 {
	m := paramsMap(params)
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n
	}
	return nil
}

func marshal(v any) (any, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, &errs.Error{Code: errs.Other, Msg: "serialization failed: " + err.Error()}
	}
	return json.RawMessage(out), nil
}

// marshalResult adapts (value, error) service returns into dispatch returns.
func marshalResult(v any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return marshal(v)
}

func formPairsArg(params json.RawMessage, key string) ([][2]string, error) {
	raw, err := objectArg(params, key)
	if err != nil {
		return nil, err
	}
	// Accept either {"k":"v"} or [["k","v"],...] (Rust Vec<(String,String)>).
	var objectForm map[string]string
	if err := json.Unmarshal(raw, &objectForm); err == nil {
		out := make([][2]string, 0, len(objectForm))
		for k, v := range objectForm {
			out = append(out, [2]string{k, v})
		}
		return out, nil
	}
	var arrayForm [][2]string
	if err := json.Unmarshal(raw, &arrayForm); err == nil {
		return arrayForm, nil
	}
	return nil, errs.NewInvalidArgument("argument " + key + " must be a form object")
}

func dispatchInner(ctx context.Context, s *session.Session, operation string, params json.RawMessage) (any, error) {
	switch {
	case operation == "auth.login":
		if err := s.Login(ctx); err != nil {
			return nil, err
		}
		return marshal(s.Inspect())
	case operation == "auth.ec_login":
		if code := optStrArg(params, "captcha"); code != nil {
			return marshal(s.ECLoginWithCaptcha(ctx, *code))
		}
		return marshal(s.ECLogin(ctx))
	case operation == "auth.ec_captcha":
		captcha, err := s.EcCaptcha(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"required": captcha.Required, "mime": captcha.Mime}
		if captcha.Image != nil {
			out["image_base64"] = base64.StdEncoding.EncodeToString(captcha.Image)
		}
		return out, nil
	case operation == "auth.uaap_login":
		return marshal(s.UAAPLogin(ctx))
	case operation == "auth.heartbeat":
		return marshal(s.Heartbeat(ctx))
	case operation == "auth.status":
		return marshal(s.Inspect())
	case operation == "auth.logout":
		s.Logout()
		return map[string]any{"signed_out": true}, nil
	case operation == "auth.cancel":
		s.CancelOperation()
		return map[string]any{"cancelled": true}, nil
	}

	j := jwc.New(s)
	switch operation {
	case "jwc.academic_info":
		return marshalResult(j.AcademicInfo(ctx))
	case "jwc.training_plan":
		return marshalResult(j.TrainingPlanInfo(ctx))
	case "jwc.terms":
		return marshalResult(j.Terms(ctx))
	case "jwc.term_scores":
		term, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.TermScores(ctx, term))
	case "jwc.all_term_scores":
		codes, err := stringArrayArg(params, "term_codes")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.AllTermScores(ctx, codes))
	case "jwc.this_term_scores":
		return marshalResult(j.ThisTermScores(ctx))
	case "jwc.score_detail":
		termID, err := strArg(params, "term_id")
		if err != nil {
			return nil, err
		}
		courseCode, err := strArg(params, "course_code")
		if err != nil {
			return nil, err
		}
		courseClass, err := strArg(params, "course_class")
		if err != nil {
			return nil, err
		}
		record := jwc.ScoreRecord{
			TermID: termID, CourseCode: courseCode, CourseClass: courseClass,
			ExamTime: optStrArg(params, "exam_time"),
		}
		return marshalResult(j.ScoreDetail(ctx, &record))
	case "jwc.exams":
		return marshalResult(j.ExamInfo(ctx))
	case "jwc.seat_info":
		return marshalResult(j.ExamSeatInfo(ctx))
	case "jwc.other_exams":
		term, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.OtherExamRecords(ctx, term))
	case "jwc.plan_completion":
		return marshalResult(j.PlanCompletion(ctx, optStrArg(params, "plan_id")))
	case "jwc.plan_options":
		return marshalResult(j.PlanOptions(ctx))
	case "jwc.student_schedule":
		term, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.StudentSchedule(ctx, term))
	case "jwc.course_schedule_terms":
		return marshalResult(j.CourseScheduleTerms(ctx))
	case "jwc.course_schedule":
		courseCode, err := strArg(params, "course_code")
		if err != nil {
			return nil, err
		}
		termCode, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.CourseSchedule(ctx, courseCode, termCode,
			i64ArgOr(params, "page_num", 1), i64ArgOr(params, "page_size", 50)))
	case "jwc.course_schedule_all":
		courseCode, err := strArg(params, "course_code")
		if err != nil {
			return nil, err
		}
		termCode, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.CourseScheduleAll(ctx, courseCode, termCode))
	case "jwc.course_schedule_term":
		term, err := strArg(params, "term_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.CourseScheduleForTerm(ctx, term))
	case "jwc.class_terms":
		return marshalResult(j.ClassCurriculumTerms(ctx))
	case "jwc.class_departments":
		return marshalResult(j.ClassCurriculumDepartments(ctx))
	case "jwc.class_subjects":
		department, err := strArg(params, "department_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.ClassCurriculumSubjects(ctx, department))
	case "jwc.class_list":
		planCode, err := strArg(params, "plan_code")
		if err != nil {
			return nil, err
		}
		departmentCode, err := strArg(params, "department_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.ClassCurriculumClasses(ctx, planCode, departmentCode, optStrArg(params, "subject_code")))
	case "jwc.class_curriculum":
		planCode, err := strArg(params, "plan_code")
		if err != nil {
			return nil, err
		}
		classCode, err := strArg(params, "class_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(j.ClassCurriculum(ctx, planCode, classCode))
	case "jwc.teacher_eval_courses":
		return marshalResult(j.TeacherEvalCourses(ctx))
	case "jwc.teacher_eval_prepare":
		courseRaw, err := objectArg(params, "course")
		if err != nil {
			return nil, err
		}
		var course jwc.TeacherEvaluationCourse
		if err := json.Unmarshal(courseRaw, &course); err != nil {
			return nil, errs.NewInvalidArgument("course must be a TeacherEvaluationCourse object")
		}
		pending, err := i64Arg(params, "pending_count")
		if err != nil {
			return nil, err
		}
		token, err := strArg(params, "index_token")
		if err != nil {
			return nil, err
		}
		if seed := optI64Arg(params, "seed"); seed != nil {
			return marshalResult(j.TeacherEvalPrepareSeeded(ctx, &course, pending, token, uint64(*seed)))
		}
		return marshalResult(j.TeacherEvalPrepare(ctx, &course, pending, token))
	case "jwc.teacher_eval_submit":
		courseRaw, err := objectArg(params, "course")
		if err != nil {
			return nil, err
		}
		var course jwc.TeacherEvaluationCourse
		if err := json.Unmarshal(courseRaw, &course); err != nil {
			return nil, errs.NewInvalidArgument("course must be a TeacherEvaluationCourse object")
		}
		title, err := strArg(params, "questionnaire_title")
		if err != nil {
			return nil, err
		}
		formData, err := formPairsArg(params, "form_data")
		if err != nil {
			return nil, err
		}
		prepared := jwc.TeacherEvaluationPreparedForm{
			Course: course, QuestionnaireTitle: title, FormData: formData,
		}
		return marshalResult(j.TeacherEvalSubmit(ctx, &prepared))
	case "jwc.teacher_eval_verify":
		courseRaw, err := objectArg(params, "course")
		if err != nil {
			return nil, err
		}
		var course jwc.TeacherEvaluationCourse
		if err := json.Unmarshal(courseRaw, &course); err != nil {
			return nil, errs.NewInvalidArgument("course must be a TeacherEvaluationCourse object")
		}
		return marshalResult(j.TeacherEvalVerify(ctx, &course))
	}

	y := ykt.New(s)
	switch operation {
	case "ykt.init_session":
		if err := y.InitSession(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"initialized": true}, nil
	case "ykt.balance":
		return marshalResult(y.Balance(ctx))
	case "ykt.transactions":
		start, err := strArg(params, "start_date")
		if err != nil {
			return nil, err
		}
		end, err := strArg(params, "end_date")
		if err != nil {
			return nil, err
		}
		return marshalResult(y.Transactions(ctx, start, end))
	case "ykt.recent_transactions":
		return marshalResult(y.RecentTransactions(ctx))
	case "ykt.monthly_transactions":
		return marshalResult(y.MonthlyTransactions(ctx))
	case "ykt.utility_page_info":
		return marshalResult(y.UtilityPageInfo(ctx))
	case "ykt.utility_options":
		dormID, err := strArg(params, "dorm_id")
		if err != nil {
			return nil, err
		}
		buildingID, err := strArg(params, "building_id")
		if err != nil {
			return nil, err
		}
		floorID, err := strArg(params, "floor_id")
		if err != nil {
			return nil, err
		}
		dormName, err := strArg(params, "dorm_name")
		if err != nil {
			return nil, err
		}
		return marshalResult(y.UtilityOptions(ctx, dormID, buildingID, floorID, dormName))
	case "ykt.utility_pay":
		money, err := i64Arg(params, "money")
		if err != nil {
			return nil, err
		}
		request := ykt.DefaultUtilityPaymentRequest()
		if v := optStrArg(params, "pay_type"); v != nil {
			request.PayType = *v
		}
		if v := optStrArg(params, "choose_pay_type"); v != nil {
			request.ChoosePayType = *v
		}
		request.RoomID = mustStr(params, "room_id")
		request.DormID = mustStr(params, "dorm_id")
		request.DormName = mustStr(params, "dorm_name")
		request.BuildName = mustStr(params, "build_name")
		request.FloorName = mustStr(params, "floor_name")
		request.RoomName = mustStr(params, "room_name")
		request.AccID = mustStr(params, "acc_id")
		request.Balances = mustStr(params, "balances")
		request.Money = money
		return marshalResult(y.UtilityPay(ctx, &request))
	case "ykt.purchase_history":
		start, err := strArg(params, "start_date")
		if err != nil {
			return nil, err
		}
		end, err := strArg(params, "end_date")
		if err != nil {
			return nil, err
		}
		return marshalResult(y.PurchaseHistory(ctx, start, end))
	case "ykt.recent_purchase_history":
		return marshalResult(y.RecentPurchaseHistory(ctx))
	}

	a := aac.New(s)
	l := labor.New(s)
	switch operation {
	case "aac.ticket":
		force := boolArgOr(params, "force_refresh", false)
		ticket, err := a.Ticket(ctx, force)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ready": true, "cached": !force, "ticket_length": len(ticket)}, nil
	case "aac.credit_info":
		return marshalResult(a.CreditInfo(ctx))
	case "aac.credit_list":
		return marshalResult(a.CreditList(ctx))
	case "aac.reset_ticket":
		a.ResetTicket()
		return map[string]any{"reset": true}, nil
	case "labor.ticket":
		force := boolArgOr(params, "force_refresh", false)
		ticket, err := l.Ticket(ctx, force)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ready": true, "cached": !force, "ticket_length": len(ticket)}, nil
	case "labor.progress":
		return marshalResult(l.Progress(ctx))
	case "labor.joined_activities":
		return marshalResult(l.JoinedActivities(ctx))
	case "labor.joined_clubs":
		return marshalResult(l.JoinedClubs(ctx))
	case "labor.club_activities":
		clubID, err := strArg(params, "club_id")
		if err != nil {
			return nil, err
		}
		return marshalResult(l.ClubActivities(ctx, clubID,
			i64ArgOr(params, "page_index", 1), i64ArgOr(params, "page_size", 100)))
	case "labor.apply_activity":
		activityID, err := strArg(params, "activity_id")
		if err != nil {
			return nil, err
		}
		reason := ""
		if v := optStrArg(params, "reason"); v != nil {
			reason = *v
		}
		return marshalResult(l.ApplyActivity(ctx, activityID, reason))
	case "labor.scan_sign_in":
		content, err := strArg(params, "content")
		if err != nil {
			return nil, err
		}
		location, err := strArg(params, "location")
		if err != nil {
			return nil, err
		}
		return marshalResult(l.ScanSignIn(ctx, &labor.SignInRequest{Content: content, Location: location}))
	case "labor.sign_list":
		activityID, err := strArg(params, "activity_id")
		if err != nil {
			return nil, err
		}
		return marshalResult(l.SignList(ctx, activityID,
			i64ArgOr(params, "type", 1), i64ArgOr(params, "page_index", 1), i64ArgOr(params, "page_size", 100)))
	case "labor.activity_detail":
		activityID, err := strArg(params, "activity_id")
		if err != nil {
			return nil, err
		}
		return marshalResult(l.ActivityDetail(ctx, activityID))
	case "labor.reset_ticket":
		l.ResetTicket()
		return map[string]any{"reset": true}, nil
	}

	i := isim.New(s)
	switch operation {
	case "isim.buildings":
		return marshalResult(i.Buildings(ctx))
	case "isim.floors":
		building, err := strArg(params, "building_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(i.Floors(ctx, building))
	case "isim.rooms":
		floor, err := strArg(params, "floor_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(i.Rooms(ctx, floor))
	case "isim.electricity":
		room, err := strArg(params, "room_code")
		if err != nil {
			return nil, err
		}
		return marshalResult(i.ElectricityInfo(ctx, room, optStrArg(params, "display_text")))
	}

	c := competition.New(s)
	r := repair.New(s)
	switch operation {
	case "competition.info":
		return marshalResult(c.Info(ctx))
	case "repair.orders":
		return marshalResult(r.Orders(ctx))
	case "repair.order_detail":
		taskID, err := strArg(params, "task_id")
		if err != nil {
			return nil, err
		}
		return marshalResult(r.OrderDetail(ctx, taskID))
	case "repair.form_data":
		return marshalResult(r.FormData(ctx))
	case "repair.upload_image":
		data, err := strArg(params, "data")
		if err != nil {
			return nil, err
		}
		return marshalResult(r.UploadImage(ctx, data))
	case "repair.submit":
		request := repair.SubmitRequest{}
		var err error
		if request.AreaID, err = strArg(params, "area_id"); err != nil {
			return nil, err
		}
		if request.AreaName, err = strArg(params, "area_name"); err != nil {
			return nil, err
		}
		if request.ProjectID, err = strArg(params, "project_id"); err != nil {
			return nil, err
		}
		if request.ProjectName, err = strArg(params, "project_name"); err != nil {
			return nil, err
		}
		if request.Phone, err = strArg(params, "phone"); err != nil {
			return nil, err
		}
		if request.Address, err = strArg(params, "address"); err != nil {
			return nil, err
		}
		if request.Description, err = strArg(params, "description"); err != nil {
			return nil, err
		}
		request.PicURLs = optStrArg(params, "pic_urls")
		return marshalResult(r.Submit(ctx, &request))
	}

	m := manifest.New(s)
	switch operation {
	case "manifest.v2":
		return marshalResult(m.FetchV2(ctx))
	case "announcements.list":
		platform := "desktop"
		if v := optStrArg(params, "platform"); v != nil {
			platform = *v
		}
		return marshalResult(m.Announcements(ctx, platform))
	}

	switch operation {
	case "session.clear_cookies":
		scopeStr, err := strArg(params, "scope")
		if err != nil {
			return nil, err
		}
		var scope cookies.Scope
		switch scopeStr {
		case "gateway":
			scope = cookies.Gateway
		case "identity":
			scope = cookies.Identity
		case "service":
			scope = cookies.Service
		case "all":
			scope = cookies.All
		default:
			return nil, errs.NewInvalidArgument("unknown cookie scope: " + scopeStr)
		}
		s.ClearCookies(scope)
		return map[string]any{"cleared": true}, nil
	case "session.expire_service_ticket":
		s.ExpireServiceTicket()
		return map[string]any{"expired": true}, nil
	case "diagnostics.export_report":
		collector := diagnostics.NewCollector(diagnostics.DefaultOptions())
		return marshal(collector.BuildReport(
			fmt.Sprintf("local-%d", timeutil.NowMillis()),
			diagnostics.IncidentFingerprint("core", "diagnostics", "none", "none", "v1"),
			"Local diagnostic report",
		))
	}

	return nil, errs.NewNotImplemented("unknown operation: " + operation)
}

func mustStr(params json.RawMessage, key string) string {
	v, _ := strArg(params, key)
	return v
}
