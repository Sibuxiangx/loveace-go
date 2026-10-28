// Package jwc ports the JWC (academic affairs) service operations and models.
//
// Source methods:
//   - desktop/lib/services/jwc/{academic,term,score,exam,plan,course_schedule,
//     student_schedule,class_curriculum,teacher_evaluation}_service.dart
//   - ios/loveaceios/Service/{JWC,Plan,StudentSchedule,CourseSchedule,
//     TeacherEvaluation}Service.swift
//   - android/.../data/service/JWCService.kt
//
// The union of the three clients is implemented: desktop supplies the
// plan/class-curriculum flows, Android supplies this-term scores and score
// detail, and iOS supplies the teacher-evaluation form flow.
package jwc

import "encoding/json"

// AcademicInfo is the academic overview.
type AcademicInfo struct {
	CompletedCourses int64   `json:"completed_courses"`
	FailedCourses    int64   `json:"failed_courses"`
	GPA              float64 `json:"gpa"`
	PendingCourses   int64   `json:"pending_courses"`
	CurrentTerm      string  `json:"current_term"`
}

// TrainingPlanInfo is the training-plan summary.
type TrainingPlanInfo struct {
	PlanName  string `json:"plan_name"`
	MajorName string `json:"major_name"`
	Grade     string `json:"grade"`
}

// TermItem is one term option.
type TermItem struct {
	TermCode  string `json:"term_code"`
	TermName  string `json:"term_name"`
	IsCurrent bool   `json:"is_current"`
}

// ScoreRecord preserves grade strings such as pass/absent/withheld.
type ScoreRecord struct {
	Sequence     int64   `json:"sequence"`
	TermID       string  `json:"term_id"`
	CourseCode   string  `json:"course_code"`
	CourseClass  string  `json:"course_class"`
	CourseNameCN string  `json:"course_name_cn"`
	CourseNameEN string  `json:"course_name_en"`
	Credits      string  `json:"credits"`
	Hours        int64   `json:"hours"`
	CourseType   *string `json:"course_type"`
	ExamType     *string `json:"exam_type"`
	Score        string  `json:"score"`
	RetakeScore  *string `json:"retake_score"`
	MakeupScore  *string `json:"makeup_score"`
	ExamTime     *string `json:"exam_time,omitempty"`
}

// TermScoreResponse is a score query page.
type TermScoreResponse struct {
	TotalCount int64         `json:"total_count"`
	Records    []ScoreRecord `json:"records"`
}

// ScoreDetailItem is one score-detail row.
type ScoreDetailItem struct {
	ScoreType     string `json:"score_type"`
	UsualScore    string `json:"usual_score"`
	MidtermScore  string `json:"midterm_score"`
	FinalScore    string `json:"final_score"`
	CategoryScore string `json:"category_score"`
	Remark        string `json:"remark"`
}

// ScoreDetail groups detail items.
type ScoreDetail struct {
	Items []ScoreDetailItem `json:"items"`
}

// UnifiedExamInfo is one exam in the aggregated view.
type UnifiedExamInfo struct {
	CourseName   string `json:"course_name"`
	ExamDate     string `json:"exam_date"`
	ExamTime     string `json:"exam_time"`
	ExamLocation string `json:"exam_location"`
	ExamType     string `json:"exam_type"`
	Note         string `json:"note"`
}

// ExamInfoResponse is the aggregated exam response.
type ExamInfoResponse struct {
	Exams      []UnifiedExamInfo `json:"exams"`
	TotalCount int64             `json:"total_count"`
}

// ExamScheduleItem is one school-exam calendar item.
type ExamScheduleItem struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Title string `json:"title"`
	Color string `json:"color"`
}

// SeatInfo is one exam seat entry.
type SeatInfo struct {
	CourseName string `json:"course_name"`
	SeatNumber string `json:"seat_number"`
}

// OtherExamRecord is one other-exam record.
type OtherExamRecord struct {
	TermCode     string `json:"term_code"`
	TermName     string `json:"term_name"`
	ExamName     string `json:"exam_name"`
	CourseCode   string `json:"course_code"`
	CourseName   string `json:"course_name"`
	ClassNumber  string `json:"class_number"`
	StudentID    string `json:"student_id"`
	StudentName  string `json:"student_name"`
	ExamLocation string `json:"exam_location"`
	ExamDate     string `json:"exam_date"`
	ExamTime     string `json:"exam_time"`
	Note         string `json:"note"`
	RowNumber    string `json:"row_number"`
}

// ScheduleCourseID is the composite id of a schedule course.
type ScheduleCourseID struct {
	ExecutiveEducationPlanNumber string `json:"executive_education_plan_number"`
	CoureNumber                  string `json:"coure_number"`
	CoureSequenceNumber          string `json:"coure_sequence_number"`
	StudentNumber                string `json:"student_number"`
}

// ScheduleTimePlace is one time/place slot.
type ScheduleTimePlace struct {
	ID                           string `json:"id"`
	ExecutiveEducationPlanNumber string `json:"executive_education_plan_number"`
	CoureNumber                  string `json:"coure_number"`
	CoureSequenceNumber          string `json:"coure_sequence_number"`
	StudentNumber                string `json:"student_number"`
	ClassWeek                    string `json:"class_week"`
	ClassDay                     int64  `json:"class_day"`
	ClassSessions                int64  `json:"class_sessions"`
	ContinuingSession            int64  `json:"continuing_session"`
	CampusName                   string `json:"campus_name"`
	TeachingBuildingName         string `json:"teaching_building_name"`
	ClassroomName                string `json:"classroom_name"`
	WeekDescription              string `json:"week_description"`
	CoursePropertiesName         string `json:"course_properties_name"`
	CoureName                    string `json:"coure_name"`
}

// EndSession is the last session index (class_sessions + continuing - 1).
func (t ScheduleTimePlace) EndSession() int64 {
	return t.ClassSessions + t.ContinuingSession - 1
}

// LocationDescription renders campus/building/classroom.
func (t ScheduleTimePlace) LocationDescription() string {
	return t.CampusName + " " + t.TeachingBuildingName + " " + t.ClassroomName
}

// ScheduleCourse is one schedule course.
type ScheduleCourse struct {
	ID                     ScheduleCourseID    `json:"id"`
	ProgramPlanNumber      string              `json:"program_plan_number"`
	CourseName             string              `json:"course_name"`
	Unit                   float64             `json:"unit"`
	ProgramPlanName        string              `json:"program_plan_name"`
	AttendClassTeacher     string              `json:"attend_class_teacher"`
	StudyModeCode          string              `json:"study_mode_code"`
	StudyModeName          string              `json:"study_mode_name"`
	CoursePropertiesCode   string              `json:"course_properties_code"`
	CoursePropertiesName   string              `json:"course_properties_name"`
	ExamTypeCode           string              `json:"exam_type_code"`
	ExamTypeName           string              `json:"exam_type_name"`
	TimeAndPlaceList       []ScheduleTimePlace `json:"time_and_place_list"`
	SelectCourseStatusCode string              `json:"select_course_status_code"`
	SelectCourseStatusName string              `json:"select_course_status_name"`
}

// CourseCode mirrors the Rust helper.
func (c ScheduleCourse) CourseCode() string { return c.ID.CoureNumber }

// CourseSequence mirrors the Rust helper.
func (c ScheduleCourse) CourseSequence() string { return c.ID.CoureSequenceNumber }

// UniqueKey mirrors the Rust helper.
func (c ScheduleCourse) UniqueKey() string {
	return c.ID.CoureNumber + "_" + c.ID.CoureSequenceNumber
}

// ScheduleDateInfo is one schedule date block.
type ScheduleDateInfo struct {
	ProgramPlanCode  string           `json:"program_plan_code"`
	ProgramPlanName  string           `json:"program_plan_name"`
	TotalUnits       float64          `json:"total_units"`
	SelectCourseList []ScheduleCourse `json:"select_course_list"`
}

// StudentScheduleResponse is the student schedule document.
type StudentScheduleResponse struct {
	AllUnits     float64            `json:"all_units"`
	ErrorMessage string             `json:"error_message"`
	XKXX         []json.RawMessage  `json:"xkxx"`
	ShowSite     bool               `json:"show_site"`
	SchoolID     string             `json:"school_id"`
	DateList     []ScheduleDateInfo `json:"date_list"`
}

// Courses flattens all select course lists.
func (r StudentScheduleResponse) Courses() []ScheduleCourse {
	var out []ScheduleCourse
	for _, info := range r.DateList {
		out = append(out, info.SelectCourseList...)
	}
	return out
}

// CourseScheduleRecord keeps upstream field spellings.
type CourseScheduleRecord struct {
	ID      *string `json:"id"`
	Zxjxjhh *string `json:"zxjxjhh"`
	Kch     *string `json:"kch"`
	Kxh     *string `json:"kxh"`
	Kcm     *string `json:"kcm"`
	Xf      *int64  `json:"xf"`
	Xs      *int64  `json:"xs"`
	Kkxsh   *string `json:"kkxsh"`
	Kkxsjc  *string `json:"kkxsjc"`
	Kslxdm  *string `json:"kslxdm"`
	Kslxmc  *string `json:"kslxmc"`
	Skjs    *string `json:"skjs"`
	Bkskrl  *int64  `json:"bkskrl"`
	Bkskyl  *int64  `json:"bkskyl"`
	Xqh     *string `json:"xqh"`
	Jxlh    *string `json:"jxlh"`
	Jash    *string `json:"jash"`
	Skzc    *string `json:"skzc"`
	Skxq    *int64  `json:"skxq"`
	Skjc    *int64  `json:"skjc"`
	Cxjc    *int64  `json:"cxjc"`
	Zcsm    *string `json:"zcsm"`
	Xqm     *string `json:"xqm"`
	Jxlm    *string `json:"jxlm"`
	Jasm    *string `json:"jasm"`
	Mxbj    *string `json:"mxbj"`
	Xss     *int64  `json:"xss"`
}

// ClassTimeStr renders "start-end" sessions.
func (r CourseScheduleRecord) ClassTimeStr() *string {
	if r.Skjc == nil || r.Cxjc == nil {
		return nil
	}
	s := itoa(*r.Skjc) + "-" + itoa(*r.Skjc+*r.Cxjc-1)
	return &s
}

// WeekdayStr renders the Chinese weekday.
func (r CourseScheduleRecord) WeekdayStr() *string {
	weekdays := []string{"", "周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	if r.Skxq != nil && *r.Skxq >= 1 && *r.Skxq <= 7 {
		s := weekdays[*r.Skxq]
		return &s
	}
	return nil
}

// ScheduleDescription renders day/sessions/weeks/location.
func (r CourseScheduleRecord) ScheduleDescription() string {
	var parts []string
	if day := r.WeekdayStr(); day != nil {
		parts = append(parts, *day)
	}
	if t := r.ClassTimeStr(); t != nil {
		parts = append(parts, "第"+*t+"节")
	}
	if r.Zcsm != nil && *r.Zcsm != "" {
		parts = append(parts, *r.Zcsm)
	}
	if r.Jxlm != nil && r.Jasm != nil {
		parts = append(parts, *r.Jxlm+*r.Jasm)
	} else if r.Xqm != nil {
		parts = append(parts, *r.Xqm)
	}
	return joinStrings(parts, " ")
}

// PlanCategory is a plan completion category tree node.
type PlanCategory struct {
	CategoryID             string         `json:"category_id"`
	CategoryName           string         `json:"category_name"`
	MinCredits             float64        `json:"min_credits"`
	CompletedCredits       float64        `json:"completed_credits"`
	TotalCourses           int64          `json:"total_courses"`
	PassedCourses          int64          `json:"passed_courses"`
	FailedCourses          int64          `json:"failed_courses"`
	MissingRequiredCourses int64          `json:"missing_required_courses"`
	Subcategories          []PlanCategory `json:"subcategories"`
	Courses                []PlanCourse   `json:"courses"`
}

// PlanCourse is one course inside a plan category.
type PlanCourse struct {
	CourseCode        string   `json:"course_code"`
	CourseName        string   `json:"course_name"`
	Credits           *float64 `json:"credits"`
	Score             *string  `json:"score"`
	ExamDate          *string  `json:"exam_date"`
	CourseType        *string  `json:"course_type"`
	IsPassed          bool     `json:"is_passed"`
	StatusDescription string   `json:"status_description"`
}

// PlanCompletionInfo is the aggregated plan completion.
type PlanCompletionInfo struct {
	PlanName      string         `json:"plan_name"`
	Major         string         `json:"major"`
	Grade         string         `json:"grade"`
	Categories    []PlanCategory `json:"categories"`
	TotalCourses  int64          `json:"total_courses"`
	PassedCourses int64          `json:"passed_courses"`
	FailedCourses int64          `json:"failed_courses"`
	UnreadCourses int64          `json:"unread_courses"`
}

// CalculateStatistics counts courses recursively.
func (p PlanCompletionInfo) CalculateStatistics() PlanCompletionInfo {
	var total, passed, failed int64
	for i := range p.Categories {
		countCategory(&p.Categories[i], &total, &passed, &failed)
	}
	p.TotalCourses = total
	p.PassedCourses = passed
	p.FailedCourses = failed
	unread := total - passed - failed
	if unread < 0 {
		unread = 0
	}
	p.UnreadCourses = unread
	return p
}

func countCategory(category *PlanCategory, total, passed, failed *int64) {
	for _, course := range category.Courses {
		*total++
		if course.IsPassed {
			*passed++
		} else if course.StatusDescription == "未通过" {
			*failed++
		}
	}
	for i := range category.Subcategories {
		countCategory(&category.Subcategories[i], total, passed, failed)
	}
}

// PlanOption is one selectable plan.
type PlanOption struct {
	PlanID    string `json:"plan_id"`
	PlanName  string `json:"plan_name"`
	PlanType  string `json:"plan_type"`
	IsCurrent bool   `json:"is_current"`
}

// PlanSelectionResponse offers plan options for multi-plan users.
type PlanSelectionResponse struct {
	Options []PlanOption `json:"options"`
	Hint    *string      `json:"hint"`
}

// OutcomeKind tags the plan completion outcome (serde tag parity).
type OutcomeKind string

// Outcome kinds.
const (
	OutcomeInfo           OutcomeKind = "info"
	OutcomeNeedsSelection OutcomeKind = "needs_selection"
)

// PlanCompletionOutcome is either data or a selection requirement. Serialized
// as {"kind":"info","info":{...}} or {"kind":"needs_selection","selection":{...}}
// to match the Rust serde tag = "kind", rename_all = "snake_case".
type PlanCompletionOutcome struct {
	Kind      OutcomeKind            `json:"kind"`
	Info      *PlanCompletionInfo    `json:"info,omitempty"`
	Selection *PlanSelectionResponse `json:"selection,omitempty"`
}

// TeacherEvaluationCourse is one evaluation course.
type TeacherEvaluationCourse struct {
	Name                    string `json:"name"`
	Teacher                 string `json:"teacher"`
	EvaluatedPeople         string `json:"evaluated_people"`
	EvaluatedPeopleNumber   string `json:"evaluated_people_number"`
	CoureSequenceNumber     string `json:"coure_sequence_number"`
	EvaluationContentNumber string `json:"evaluation_content_number"`
	QuestionnaireCode       string `json:"questionnaire_code"`
	QuestionnaireName       string `json:"questionnaire_name"`
	IsEvaluated             bool   `json:"is_evaluated"`
}

// TeacherEvaluationIndexProtection is the first-visit reading gate on the evaluation index page.
type TeacherEvaluationIndexProtection struct {
	SchoolID             string `json:"school_id"`
	InfoOff              string `json:"info_off"`
	InfoTimeSeconds      int    `json:"info_time_seconds"`
	IsFinished           string `json:"is_finished"`
	IsFinishedResolved   bool   `json:"is_finished_resolved"`
	IsFirstVisit         string `json:"is_first_visit"`
	IsFirstVisitResolved bool   `json:"is_first_visit_resolved"`
	HasAhcjTimer         bool   `json:"has_ahcj_timer"`
}

// RequiresInitialWait reports whether the index withholds the course list
// until the first-visit protection countdown has elapsed.
func (p TeacherEvaluationIndexProtection) RequiresInitialWait() bool {
	if p.InfoOff != "1" || p.InfoTimeSeconds <= 0 || !p.HasAhcjTimer {
		return false
	}
	finishedKnown := p.IsFinishedResolved && p.IsFinished != "unfinished"
	visitedKnown := p.IsFirstVisitResolved && p.IsFirstVisit != "unVisit"
	return !(finishedKnown || visitedKnown)
}

// WaitSeconds is the protection wait: countdown plus a 5s safety margin.
func (p TeacherEvaluationIndexProtection) WaitSeconds() int {
	if p.InfoTimeSeconds > 0 {
		return p.InfoTimeSeconds + 5
	}
	return 0
}

// TeacherEvaluationCourseList is the evaluation index.
type TeacherEvaluationCourseList struct {
	TokenValue    string                           `json:"token_value"`
	IsClosed      bool                             `json:"is_closed"`
	ClosedMessage string                           `json:"closed_message"`
	Courses       []TeacherEvaluationCourse        `json:"courses"`
	Protection    TeacherEvaluationIndexProtection `json:"protection"`
}

// TeacherEvaluationIndexStatus is the evaluation index state without course data.
type TeacherEvaluationIndexStatus struct {
	TokenValue    string                           `json:"token_value"`
	IsClosed      bool                             `json:"is_closed"`
	ClosedMessage string                           `json:"closed_message"`
	Protection    TeacherEvaluationIndexProtection `json:"protection"`
}

// TeacherEvaluationOption is one radio option.
type TeacherEvaluationOption struct {
	Key    string  `json:"key"`
	Value  string  `json:"value"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
	Label  string  `json:"label"`
}

// TeacherEvaluationRadioQuestion is one radio question group.
type TeacherEvaluationRadioQuestion struct {
	Key      string                    `json:"key"`
	Category string                    `json:"category"`
	Title    string                    `json:"title"`
	Options  []TeacherEvaluationOption `json:"options"`
}

// TeacherEvaluationTextQuestion is one free-text question.
type TeacherEvaluationTextQuestion struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
	Kind     string `json:"kind"`
}

// TeacherEvaluationQuestionnaire is the parsed evaluation page.
type TeacherEvaluationQuestionnaire struct {
	Title                 string                           `json:"title"`
	TokenValue            string                           `json:"token_value"`
	QuestionnaireCode     string                           `json:"questionnaire_code"`
	EvaluatedPeopleNumber string                           `json:"evaluated_people_number"`
	EvaluationContent     string                           `json:"evaluation_content"`
	EvaluatedPerson       string                           `json:"evaluated_person"`
	RadioQuestions        []TeacherEvaluationRadioQuestion `json:"radio_questions"`
	TextQuestions         []TeacherEvaluationTextQuestion  `json:"text_questions"`
}

// TeacherEvaluationPreparedForm is a prepared submission.
type TeacherEvaluationPreparedForm struct {
	Course             TeacherEvaluationCourse `json:"course"`
	QuestionnaireTitle string                  `json:"questionnaire_title"`
	FormData           [][2]string             `json:"form_data"`
}

// TeacherEvaluationSubmitResult is the submit outcome.
type TeacherEvaluationSubmitResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ClassCurriculumOption is a code/name option.
type ClassCurriculumOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ClassCurriculumClassOption is one class option.
type ClassCurriculumClassOption struct {
	PlanCode       string  `json:"plan_code"`
	ClassCode      string  `json:"class_code"`
	ClassName      string  `json:"class_name"`
	DepartmentName *string `json:"department_name"`
	SubjectName    *string `json:"subject_name"`
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
