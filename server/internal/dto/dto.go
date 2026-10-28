// Package dto defines the server's own API data shapes and the single
// place where school models are converted (DESIGN.md §2/§3). School field
// names never leak: clients see only these types.
package dto

import "time"

// Meta carries freshness information for school-sourced data.
type Meta struct {
	FetchedAt string `json:"fetched_at"` // ISO-8601, +08:00
	Stale     bool   `json:"stale"`
	Source    string `json:"source"` // live | cache
}

// Envelope is the success wrapper: {"data": ..., "meta": ...}.
type Envelope struct {
	Data any  `json:"data"`
	Meta Meta `json:"meta"`
}

// UserView is the account view served to clients. The school account
// number is masked.
type UserView struct {
	ID              string `json:"id"`
	AccountMasked   string `json:"account_masked"`
	CredentialState string `json:"credential_state"`
}

// TokenPair is issued on login and refresh.
type TokenPair struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    int64    `json:"expires_in"` // access-token lifetime in seconds
	User         UserView `json:"user"`
}

// DeviceView is one client device.
type DeviceView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// AcademicSummary mirrors jwc.AcademicInfo in the API contract.
type AcademicSummary struct {
	CompletedCourses int64   `json:"completed_courses"`
	FailedCourses    int64   `json:"failed_courses"`
	PendingCourses   int64   `json:"pending_courses"`
	GPA              float64 `json:"gpa"`
	CurrentTerm      string  `json:"current_term"`
}

// Term is one selectable term.
type Term struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
}

// ScoreRecord mirrors jwc.ScoreRecord; grade strings stay strings because
// the school encodes pass/absent/withheld in them.
type ScoreRecord struct {
	Sequence     int64   `json:"sequence"`
	TermID       string  `json:"term_id"`
	CourseCode   string  `json:"course_code"`
	CourseClass  string  `json:"course_class"`
	CourseNameCN string  `json:"course_name_cn"`
	CourseNameEN string  `json:"course_name_en"`
	Credits      string  `json:"credits"`
	Hours        int64   `json:"hours"`
	CourseType   *string `json:"course_type,omitempty"`
	ExamType     *string `json:"exam_type,omitempty"`
	Score        string  `json:"score"`
	RetakeScore  *string `json:"retake_score,omitempty"`
	MakeupScore  *string `json:"makeup_score,omitempty"`
	ExamTime     *string `json:"exam_time,omitempty"`
}

// ScorePage is one term's score query result.
type ScorePage struct {
	TotalCount int64         `json:"total_count"`
	Records    []ScoreRecord `json:"records"`
}

// PlanCategory is one category of the plan-completion tree.
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
	Credits           *float64 `json:"credits,omitempty"`
	Score             *string  `json:"score,omitempty"`
	IsPassed          bool     `json:"is_passed"`
	StatusDescription string   `json:"status_description"`
}

// PlanCompletion is the plan completion payload; Kind distinguishes the
// info shape from a needs-selection response.
type PlanCompletion struct {
	Kind string `json:"kind"` // info | needs_selection
	// Info is set when Kind == "info".
	Info *PlanCompletionInfo `json:"info,omitempty"`
	// Selection is set when Kind == "needs_selection".
	Selection *PlanSelection `json:"selection,omitempty"`
}

// PlanSelection offers plan options for multi-plan users.
type PlanSelection struct {
	Options []PlanOption `json:"options"`
	Hint    *string      `json:"hint"`
}

// PlanOption is one selectable plan.
type PlanOption struct {
	PlanID    string `json:"plan_id"`
	PlanName  string `json:"plan_name"`
	PlanType  string `json:"plan_type"`
	IsCurrent bool   `json:"is_current"`
}

// PlanCompletionInfo aggregates the plan tree.
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

// FetchedAt renders t in the API's fixed timezone (+08:00, RFC3339).
func FetchedAt(t time.Time) string {
	loc := time.FixedZone("CST", 8*3600)
	return t.In(loc).Format(time.RFC3339)
}

// ExamItem is one exam in the aggregated exam view.
type ExamItem struct {
	CourseName   string `json:"course_name"`
	ExamDate     string `json:"exam_date"`
	ExamTime     string `json:"exam_time"`
	ExamLocation string `json:"exam_location"`
	ExamType     string `json:"exam_type"`
	Note         string `json:"note"`
}

// ExamList is the aggregated exam response.
type ExamList struct {
	TotalCount int64      `json:"total_count"`
	Exams      []ExamItem `json:"exams"`
}

// TrainingPlan is the training-plan summary.
type TrainingPlan struct {
	PlanName  string `json:"plan_name"`
	MajorName string `json:"major_name"`
	Grade     string `json:"grade"`
}

// TimetableSlot is one weekly time/place slot of a course.
type TimetableSlot struct {
	Day             int64  `json:"day"` // 1 = Monday
	StartSession    int64  `json:"start_session"`
	EndSession      int64  `json:"end_session"`
	Location        string `json:"location"`
	WeekDescription string `json:"week_description"`
}

// TimetableCourse is one course in the student timetable.
type TimetableCourse struct {
	CourseCode string          `json:"course_code"`
	CourseSeq  string          `json:"course_seq"`
	CourseName string          `json:"course_name"`
	Teacher    string          `json:"teacher"`
	Units      float64         `json:"units"`
	Status     string          `json:"status"`
	Slots      []TimetableSlot `json:"slots"`
}

// Timetable is the raw student schedule for one term. The calendar merge
// (holidays/makeups → concrete dates) lands with the calendar module (M4).
type Timetable struct {
	Units   float64           `json:"units"`
	Courses []TimetableCourse `json:"courses"`
}
