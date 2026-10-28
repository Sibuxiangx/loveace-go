package jwc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"regexp"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

const (
	formContentType = "application/x-www-form-urlencoded"
	ajaxAccept      = "application/json, text/javascript, */*; q=0.01"
)

// Service gives access to JWC operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().JWCBase, path)
}

func (sv *Service) get(caller context.Context, path string, headers [][2]string) (*transport.Response, error) {
	if err := sv.s.AwaitServiceCallback(caller); err != nil {
		return nil, err
	}
	request := transport.NewRequest(transport.Get, sv.url(path))
	request.Headers = append(request.Headers, headers...)
	return sv.s.SendRead(caller, request)
}

func (sv *Service) getURL(caller context.Context, rawURL string) (*transport.Response, error) {
	if err := sv.s.AwaitServiceCallback(caller); err != nil {
		return nil, err
	}
	return sv.s.SendRead(caller, transport.NewRequest(transport.Get, rawURL))
}

func (sv *Service) postForm(caller context.Context, path string, pairs [][2]string, headers [][2]string, kind session.Kind) (*transport.Response, error) {
	if err := sv.s.AwaitServiceCallback(caller); err != nil {
		return nil, err
	}
	request := transport.NewRequest(transport.Post, sv.url(path)).
		Header("Content-Type", formContentType).
		WithBody([]byte(config.FormEncode(pairs)))
	request.Headers = append(request.Headers, headers...)
	if kind == session.Read {
		return sv.s.SendRead(caller, request)
	}
	return sv.s.SendWrite(caller, request)
}

// ==================== Academic overview ====================

// AcademicInfo fetches /main/academicInfo?sf_request_type=ajax (POST form).
// The body is an array; the first object carries the overview fields.
func (sv *Service) AcademicInfo(caller context.Context) (*AcademicInfo, error) {
	referer := sv.url("/index.jsp")
	response, err := sv.postForm(caller, "/main/academicInfo?sf_request_type=ajax", nil,
		[][2]string{{"Referer", referer}}, session.Read)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	items := common.ArrayItems(value)
	if len(items) == 0 {
		return nil, errs.NewParse("academicInfo: expected non-empty array")
	}
	first := items[0]
	return &AcademicInfo{
		CompletedCourses: common.JSONI64(first, "courseNum"),
		FailedCourses:    common.JSONI64(first, "coursePas"),
		GPA:              common.JSONF64(first, "gpa"),
		PendingCourses:   common.JSONI64(first, "courseNum_bxqyxd"),
		CurrentTerm:      common.JSONString(first, "zxjxjhh"),
	}, nil
}

// TrainingPlanInfo fetches /main/showPyfaInfo?sf_request_type=ajax (GET).
// data[0] is [planName, majorCode].
func (sv *Service) TrainingPlanInfo(caller context.Context) (*TrainingPlanInfo, error) {
	response, err := sv.get(caller, "/main/showPyfaInfo?sf_request_type=ajax", nil)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	data := common.RawField(value, "data")
	items := common.ArrayItems(data)
	if len(items) == 0 {
		return nil, errs.NewParse("trainingPlan: data[0] missing")
	}
	planArray := common.ArrayItems(items[0])
	if len(planArray) < 2 {
		return nil, errs.NewParse("trainingPlan: short plan array")
	}
	planName := strings.Trim(strings.TrimSpace(string(planArray[0])), "\"")
	grade := ""
	if m := gradeRe.FindStringSubmatch(planName); m != nil {
		grade = m[1]
	}
	majorName := planName
	if grade != "" {
		majorName = strings.ReplaceAll(majorName, grade+"级", "")
	}
	majorName = strings.ReplaceAll(majorName, "本科培养方案", "")
	majorName = strings.ReplaceAll(majorName, "培养方案", "")
	return &TrainingPlanInfo{
		PlanName:  planName,
		MajorName: strings.TrimSpace(majorName),
		Grade:     grade,
	}, nil
}

var gradeRe = regexp.MustCompile(`(\d{4})级`)

// ==================== Terms ====================

// Terms parses select#planCode from the calendarSemesterCurriculum index.
func (sv *Service) Terms(caller context.Context) ([]TermItem, error) {
	response, err := sv.get(caller, "/student/courseSelect/calendarSemesterCurriculum/index", nil)
	if err != nil {
		return nil, err
	}
	return parseTermSelect(response.BodyText(), "#planCode")
}

// ==================== Scores ====================

func scoreQueryForm(termCode, pageSize string) [][2]string {
	return [][2]string{
		{"zxjxjhh", termCode},
		{"kch", ""},
		{"kcm", ""},
		{"pageNum", "1"},
		{"pageSize", pageSize},
		{"sf_request_type", "ajax"},
	}
}

var dynamicAllTermRe = regexp.MustCompile(`/([A-Za-z0-9]+)/allTermScores/data`)
var dynamicThisTermRe = regexp.MustCompile(`/([A-Za-z0-9]+)/thisTermScores/data`)
var dynamicScoreDetailRe = regexp.MustCompile(`/([A-Za-z0-9]+)/thisterm/coursePropertyScores/serchScoreDetail`)

func extractDynamicPath(page string, re *regexp.Regexp) (string, error) {
	if m := re.FindStringSubmatch(page); m != nil {
		return m[1], nil
	}
	return "", errs.NewParse("dynamic path not found for " + re.String())
}

// TermScores fetches all-term score query (dynamic path + POST form).
func (sv *Service) TermScores(caller context.Context, termCode string) (*TermScoreResponse, error) {
	prePath := "/student/integratedQuery/scoreQuery/allTermScores/index"
	pre, err := sv.get(caller, prePath, nil)
	if err != nil {
		return nil, err
	}
	preURL := sv.url(prePath)
	dynamic, err := extractDynamicPath(pre.BodyText(), dynamicAllTermRe)
	if err != nil {
		return nil, err
	}
	scorePath := "/student/integratedQuery/scoreQuery/" + dynamic + "/allTermScores/data"
	response, err := sv.postForm(caller, scorePath, scoreQueryForm(termCode, "50"),
		[][2]string{{"Referer", preURL}}, session.Read)
	if err != nil {
		return nil, err
	}
	return parseTermScoreResponse(response.BodyText())
}

// AllTermScores fetches every supplied term's scores using one dynamic path.
func (sv *Service) AllTermScores(caller context.Context, termCodes []string) ([]ScoreRecord, error) {
	prePath := "/student/integratedQuery/scoreQuery/allTermScores/index"
	pre, err := sv.get(caller, prePath, nil)
	if err != nil {
		return nil, err
	}
	preURL := sv.url(prePath)
	dynamic, err := extractDynamicPath(pre.BodyText(), dynamicAllTermRe)
	if err != nil {
		return nil, err
	}
	scorePath := "/student/integratedQuery/scoreQuery/" + dynamic + "/allTermScores/data"
	var records []ScoreRecord
	for _, term := range termCodes {
		response, err := sv.postForm(caller, scorePath, scoreQueryForm(term, "100"),
			[][2]string{{"Referer", preURL}}, session.Read)
		if err != nil {
			if errs.CodeOf(err) == errs.Cancelled {
				return nil, err
			}
			continue
		}
		if page, err := parseTermScoreResponse(response.BodyText()); err == nil {
			records = append(records, page.Records...)
		}
	}
	return records, nil
}

// ThisTermScores fetches this-term scores (Android/iOS shape: array of {list:[...]}).
func (sv *Service) ThisTermScores(caller context.Context) (*TermScoreResponse, error) {
	prePath := "/student/integratedQuery/scoreQuery/thisTermScores/index"
	pre, err := sv.get(caller, prePath, nil)
	if err != nil {
		return nil, err
	}
	preURL := sv.url(prePath)
	dynamic, err := extractDynamicPath(pre.BodyText(), dynamicThisTermRe)
	if err != nil {
		return nil, err
	}
	path := "/student/integratedQuery/scoreQuery/" + dynamic + "/thisTermScores/data?sf_request_type=ajax"
	response, err := sv.get(caller, path, [][2]string{
		{"Accept", ajaxAccept},
		{"Referer", preURL},
		{"X-Requested-With", "XMLHttpRequest"},
	})
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	items := common.ArrayItems(value)
	var list []json.RawMessage
	if len(items) > 0 {
		list = common.ArrayItems(common.RawField(items[0], "list"))
	}
	var records []ScoreRecord
	for _, item := range list {
		if rec, ok := parseThisTermScoreRecord(item); ok {
			records = append(records, rec)
		}
	}
	return &TermScoreResponse{TotalCount: int64(len(records)), Records: records}, nil
}

// ScoreDetail fetches the score detail for a this-term record.
func (sv *Service) ScoreDetail(caller context.Context, record *ScoreRecord) (*ScoreDetail, error) {
	prePath := "/student/integratedQuery/scoreQuery/thisTermScores/index"
	pre, err := sv.get(caller, prePath, nil)
	if err != nil {
		return nil, err
	}
	preURL := sv.url(prePath)
	dynamic, err := extractDynamicPath(pre.BodyText(), dynamicScoreDetailRe)
	if err != nil {
		return nil, err
	}
	path := "/student/integratedQuery/scoreQuery/" + dynamic + "/thisterm/coursePropertyScores/serchScoreDetail?sf_request_type=ajax"
	examTime := ""
	if record.ExamTime != nil {
		examTime = *record.ExamTime
	}
	form := [][2]string{
		{"zxjxjhh", record.TermID},
		{"kch", record.CourseCode},
		{"kssj", examTime},
		{"kxh", record.CourseClass},
	}
	response, err := sv.postForm(caller, path, form, [][2]string{
		{"Accept", ajaxAccept},
		{"Referer", preURL},
		{"X-Requested-With", "XMLHttpRequest"},
	}, session.Read)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	var items []ScoreDetailItem
	for _, item := range common.ArrayItems(common.RawField(value, "mx")) {
		code := common.JSONString(common.RawField(item, "id"), "scoreTypeCode")
		items = append(items, ScoreDetailItem{
			ScoreType:     scoreTypeName(code),
			UsualScore:    common.JSONString(item, "pscj"),
			MidtermScore:  common.JSONString(item, "qzcj"),
			FinalScore:    common.JSONString(item, "qmcj"),
			CategoryScore: common.JSONString(item, "zcj"),
			Remark:        common.JSONString(item, "remark"),
		})
	}
	return &ScoreDetail{Items: items}, nil
}

func scoreTypeName(code string) string {
	switch code {
	case "001":
		return "课堂成绩"
	case "002":
		return "实验成绩"
	case "003":
		return "实践成绩"
	default:
		return "成绩分类"
	}
}

// ==================== Exams ====================

// ExamInfo aggregates school exams + seat info + other exams.
func (sv *Service) ExamInfo(caller context.Context) (*ExamInfoResponse, error) {
	academic, err := sv.AcademicInfo(caller)
	if err != nil {
		return nil, err
	}
	termCode := academic.CurrentTerm
	now := timeutil.NowShanghai()
	start := timeutil.FormatYMD(now)
	end := fmt.Sprintf("%d-09-30", now.Year())
	if strings.HasSuffix(termCode, "1") {
		end = fmt.Sprintf("%d-03-30", now.Year()+1)
	}

	indexResp, err := sv.get(caller, "/student/examinationManagement/examPlan/index", nil)
	if err != nil {
		return nil, err
	}
	seats := parseExamSeats(indexResp.BodyText())
	school, err := sv.fetchSchoolExams(caller, start, end)
	if err != nil {
		return nil, err
	}
	others, err := sv.OtherExamRecords(caller, termCode)
	if err != nil {
		return nil, err
	}

	var all []UnifiedExamInfo
	for i := range school {
		if exam, ok := convertSchoolExam(&school[i], seats); ok {
			all = append(all, *exam)
		}
	}
	for i := range others {
		if exam := convertOtherExam(&others[i]); exam != nil {
			all = append(all, *exam)
		}
	}
	sortExams(all)
	return &ExamInfoResponse{TotalCount: int64(len(all)), Exams: all}, nil
}

func sortExams(all []UnifiedExamInfo) {
	for i := 1; i < len(all); i++ {
		for j := i; j > 0; j-- {
			if all[j].ExamDate < all[j-1].ExamDate ||
				(all[j].ExamDate == all[j-1].ExamDate && all[j].ExamTime < all[j-1].ExamTime) {
				all[j], all[j-1] = all[j-1], all[j]
			} else {
				break
			}
		}
	}
}

func (sv *Service) fetchSchoolExams(caller context.Context, start, end string) ([]ExamScheduleItem, error) {
	prePath := "/student/examinationManagement/examPlan/index"
	if _, err := sv.get(caller, prePath, nil); err != nil {
		return nil, err
	}
	timestamp := timeutil.NowMillis()
	path := fmt.Sprintf("/student/examinationManagement/examPlan/detail?start=%s&end=%s&_=%d",
		start, end, timestamp)
	response, err := sv.get(caller, path, [][2]string{
		{"Accept", ajaxAccept},
		{"Accept-Charset", "utf-8"},
		{"X-Requested-With", "XMLHttpRequest"},
	})
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(response.BodyText())
	if body == "" || body == "]" {
		return nil, nil
	}
	value, err := common.ParseJSON(body)
	if err != nil {
		return nil, err
	}
	var items []ExamScheduleItem
	for _, item := range common.ArrayItems(value) {
		items = append(items, ExamScheduleItem{
			Title: common.JSONString(item, "title"),
			Start: common.JSONString(item, "start"),
			End:   common.JSONString(item, "end"),
			Color: common.JSONString(item, "color"),
		})
	}
	return items, nil
}

// ExamSeatInfo parses seat info from the exam index page.
func (sv *Service) ExamSeatInfo(caller context.Context) ([]SeatInfo, error) {
	response, err := sv.get(caller, "/student/examinationManagement/examPlan/index", nil)
	if err != nil {
		return nil, err
	}
	return parseExamSeats(response.BodyText()), nil
}

// OtherExamRecords fetches other-exam records (queryScores?sf_request_type=ajax).
func (sv *Service) OtherExamRecords(caller context.Context, termCode string) ([]OtherExamRecord, error) {
	response, err := sv.postForm(caller,
		"/student/examinationManagement/othersExamPlan/queryScores?sf_request_type=ajax",
		[][2]string{
			{"zxjxjhh", termCode},
			{"tab", "0"},
			{"pageNum", "1"},
			{"pageSize", "30"},
		},
		[][2]string{
			{"Accept", ajaxAccept},
			{"X-Requested-With", "XMLHttpRequest"},
		}, session.Read)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	var out []OtherExamRecord
	for _, record := range common.ExtractRecords(value) {
		if rec, ok := parseOtherExam(record); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ==================== Training plan ====================

// PlanCompletion returns either data or a selection requirement.
func (sv *Service) PlanCompletion(caller context.Context, planID *string) (*PlanCompletionOutcome, error) {
	path := "/student/integratedQuery/planCompletion/index"
	if planID != nil && *planID != "" {
		path = "/student/integratedQuery/planCompletion/getPyfaIndex/" + *planID
	}
	response, err := sv.get(caller, path, nil)
	if err != nil {
		return nil, err
	}
	page := response.BodyText()
	if selection, ok := parsePlanSelection(page); ok && len(selection.Options) > 0 {
		return &PlanCompletionOutcome{Kind: OutcomeNeedsSelection, Selection: selection}, nil
	}
	nodes, err := extractZTreeNodes(page)
	if err != nil {
		return nil, err
	}
	categories := buildCategoryTree(nodes)
	if len(categories) == 0 {
		return nil, errs.NewParse("planCompletion: no categories parsed")
	}
	planName, major, grade := parsePlanHeader(page)
	info := PlanCompletionInfo{
		PlanName:   planName,
		Major:      major,
		Grade:      grade,
		Categories: categories,
	}.CalculateStatistics()
	return &PlanCompletionOutcome{Kind: OutcomeInfo, Info: &info}, nil
}

// PlanOptions returns plan options for multi-plan users.
func (sv *Service) PlanOptions(caller context.Context) (*PlanSelectionResponse, error) {
	response, err := sv.get(caller, "/student/integratedQuery/planCompletion/index", nil)
	if err != nil {
		return nil, err
	}
	if selection, ok := parsePlanSelection(response.BodyText()); ok {
		return selection, nil
	}
	hint := "无需选择培养方案"
	return &PlanSelectionResponse{Options: []PlanOption{}, Hint: &hint}, nil
}

// ==================== Student schedule ====================

// StudentSchedule fetches the student schedule for a term.
func (sv *Service) StudentSchedule(caller context.Context, termCode string) (*StudentScheduleResponse, error) {
	prePath := "/student/courseSelect/calendarSemesterCurriculum/index"
	pre, err := sv.get(caller, prePath, nil)
	if err != nil {
		return nil, err
	}
	preURL := sv.url(prePath)
	dynamic, err := extractDynamicPath(pre.BodyText(),
		regexp.MustCompile(`/student/courseSelect/thisSemesterCurriculum/([A-Za-z0-9]+)/ajaxStudentSchedule`))
	if err != nil {
		return nil, err
	}
	path := "/student/courseSelect/thisSemesterCurriculum/" + dynamic + "/ajaxStudentSchedule/past/callback"
	request := transport.NewRequest(transport.Post, sv.url(path)).
		Header("Referer", preURL).
		Header("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8").
		Header("X-Requested-With", "XMLHttpRequest").
		Header("Accept", ajaxAccept).
		WithBody([]byte("&planCode=" + termCode))
	response, err := sv.s.SendRead(caller, request)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	errorMessage := common.JSONString(value, "errorMessage")
	if errorMessage != "" {
		return nil, errs.NewServer(-1, errorMessage)
	}
	return parseStudentSchedule(value)
}

// ==================== Course offering query ====================

// CourseScheduleTerms fetches course-offering terms.
func (sv *Service) CourseScheduleTerms(caller context.Context) ([]TermItem, error) {
	response, err := sv.get(caller, "/student/integratedQuery/course/courseSchdule/index", nil)
	if err != nil {
		return nil, err
	}
	page := response.BodyText()
	for _, selector := range []string{"#zxjxjhh", `select[name="zxjxjhh"]`} {
		terms, err := parseTermSelect(page, selector)
		if err == nil && len(terms) > 0 {
			return terms, nil
		}
	}
	return nil, errs.NewParse("courseSchedule: no term select")
}

// CourseSchedule queries course offerings by course code.
func (sv *Service) CourseSchedule(caller context.Context, courseCode, termCode string, pageNum, pageSize int64) ([]CourseScheduleRecord, error) {
	records, _, err := sv.courseSchedulePage(caller, courseCode, termCode, pageNum, pageSize)
	return records, err
}

// CourseScheduleAll queries all course offerings for a course (paged until exhausted).
func (sv *Service) CourseScheduleAll(caller context.Context, courseCode, termCode string) ([]CourseScheduleRecord, error) {
	var all []CourseScheduleRecord
	page := int64(1)
	for {
		records, total, err := sv.courseSchedulePage(caller, courseCode, termCode, page, 50)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
		if int64(len(all)) >= total || page >= 100 {
			break
		}
		if page > 1 && len(all) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// CourseScheduleForTerm queries all offerings for a term (no course filter).
func (sv *Service) CourseScheduleForTerm(caller context.Context, termCode string) ([]CourseScheduleRecord, error) {
	var all []CourseScheduleRecord
	page := int64(1)
	for {
		records, total, err := sv.courseSchedulePage(caller, "", termCode, page, 200)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
		if int64(len(all)) >= total || page >= 200 {
			break
		}
		page++
	}
	return all, nil
}

func (sv *Service) courseSchedulePage(caller context.Context, courseCode, termCode string, pageNum, pageSize int64) ([]CourseScheduleRecord, int64, error) {
	form := [][2]string{
		{"zxjxjhh", termCode},
		{"kkxsh", ""},
		{"kkxqh", ""},
		{"jxlh", ""},
		{"jash", ""},
		{"skxq", ""},
		{"skjc", ""},
		{"kch", courseCode},
		{"kcm", ""},
		{"kclb", ""},
		{"skjs", ""},
		{"xqname", ""},
		{"jcname", ""},
		{"jxlname", ""},
		{"jasname", ""},
		{"pageNum", itoa(pageNum)},
		{"pageSize", itoa(pageSize)},
	}
	referer := sv.url("/student/integratedQuery/course/courseSchdule/index")
	response, err := sv.postForm(caller,
		"/student/integratedQuery/course/courseSchdule/courseInfo?sf_request_type=ajax",
		form, [][2]string{{"Referer", referer}}, session.Read)
	if err != nil {
		return nil, 0, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, 0, err
	}
	list := common.RawField(value, "list")
	var records []CourseScheduleRecord
	for _, item := range common.ArrayItems(common.RawField(list, "records")) {
		records = append(records, parseCourseScheduleRecord(item))
	}
	total := int64(0)
	if ctx := common.RawField(list, "pageContext"); ctx != nil {
		total = common.JSONI64(ctx, "totalCount")
	}
	return records, total, nil
}

// ==================== Class curriculum ====================

// ClassCurriculumTerms fetches class-curriculum terms.
func (sv *Service) ClassCurriculumTerms(caller context.Context) ([]TermItem, error) {
	response, err := sv.get(caller, "/student/teachingResources/classCurriculum/index", nil)
	if err != nil {
		return nil, err
	}
	page := response.BodyText()
	terms, err := parseTermSelect(page, `select[name="executiveEducationPlanNum"]`)
	if err == nil {
		return terms, nil
	}
	return parseTermSelect(page, "select#executiveEducationPlanNum")
}

// ClassCurriculumDepartments fetches departments for class curriculum.
func (sv *Service) ClassCurriculumDepartments(caller context.Context) ([]ClassCurriculumOption, error) {
	response, err := sv.get(caller, "/student/teachingResources/classCurriculum/index", nil)
	if err != nil {
		return nil, err
	}
	page := response.BodyText()
	for _, selector := range []string{"select#departmentNum", `select[name="departmentNum"]`} {
		options, err := parseSelectOptions(page, selector)
		if err == nil && len(options) > 0 {
			return options, nil
		}
	}
	return []ClassCurriculumOption{}, nil
}

// ClassCurriculumSubjects fetches subjects for a department.
func (sv *Service) ClassCurriculumSubjects(caller context.Context, departmentCode string) ([]ClassCurriculumOption, error) {
	target := config.WithQuery(
		sv.url("/student/teachingResources/gradeAndClassCurriculum/subjectJson"),
		[][2]string{{"departmentNum", departmentCode}})
	response, err := sv.getURL(caller, target)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	var out []ClassCurriculumOption
	for _, item := range common.ArrayItems(value) {
		option := ClassCurriculumOption{
			Code: common.JSONString(item, "subjectCode"),
			Name: common.JSONString(item, "subjectName"),
		}
		if option.Code != "" {
			out = append(out, option)
		}
	}
	return out, nil
}

// ClassCurriculumClasses fetches classes matching a department and optional subject.
func (sv *Service) ClassCurriculumClasses(caller context.Context, planCode, departmentCode string, subjectCode *string) ([]ClassCurriculumClassOption, error) {
	subject := ""
	if subjectCode != nil {
		subject = *subjectCode
	}
	response, err := sv.postForm(caller,
		"/student/teachingResources/classCurriculum/search",
		[][2]string{
			{"executiveEducationPlanNum", planCode},
			{"yearNum", ""},
			{"departmentNum", departmentCode},
			{"subjectNum", subject},
			{"classNum", ""},
			{"pageNum", "1"},
			{"pageSize", "500"},
		}, nil, session.Read)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	var first json.RawMessage
	if items := common.ArrayItems(value); len(items) > 0 {
		first = items[0]
	}
	var out []ClassCurriculumClassOption
	for _, record := range common.ArrayItems(common.RawField(first, "records")) {
		id := common.RawField(record, "id")
		plan := common.JSONString(id, "executiveEducationPlanNumber")
		if plan == "" {
			plan = planCode
		}
		className := common.JSONString(record, "className")
		if className == "" {
			className = common.JSONString(id, "classNum")
		}
		option := ClassCurriculumClassOption{
			PlanCode:       plan,
			ClassCode:      common.JSONString(id, "classNum"),
			ClassName:      className,
			DepartmentName: common.JSONOptString(record, "departmentName"),
			SubjectName:    common.JSONOptString(record, "subjectName"),
		}
		if option.ClassCode != "" {
			out = append(out, option)
		}
	}
	return out, nil
}

// ClassCurriculum fetches the class curriculum for a plan/class.
func (sv *Service) ClassCurriculum(caller context.Context, planCode, classCode string) ([]CourseScheduleRecord, error) {
	target := config.WithQuery(
		sv.url("/student/teachingResources/classCurriculum/searchCurriculumInfo/callback"),
		[][2]string{
			{"planCode", planCode},
			{"classCode", classCode},
			{"sf_request_type", "ajax"},
		})
	response, err := sv.getURL(caller, target)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	data := common.RawField(value, "data")
	kbInfo := common.ArrayItems(common.RawField(data, "kbInfo"))
	if len(kbInfo) == 0 {
		return nil, nil
	}
	first := common.ArrayItems(kbInfo[0])
	if len(first) == 0 {
		return nil, nil
	}
	var out []CourseScheduleRecord
	for _, item := range first {
		out = append(out, parseCourseScheduleRecordFromClass(item))
	}
	return out, nil
}

// ==================== Teacher evaluation ====================

// teacherEvalIndexPage fetches the evaluation index page (GET only).
func (sv *Service) teacherEvalIndexPage(caller context.Context) (string, error) {
	indexResp, err := sv.get(caller, "/student/teachingEvaluation/evaluation/index",
		[][2]string{{"User-Agent", config.UAIOS}})
	if err != nil {
		return "", err
	}
	return indexResp.BodyText(), nil
}

// TeacherEvalIndex loads the evaluation index state (token, closed state and
// first-visit protection). It never issues the course search request.
func (sv *Service) TeacherEvalIndex(caller context.Context) (*TeacherEvaluationIndexStatus, error) {
	index, err := sv.teacherEvalIndexPage(caller)
	if err != nil {
		return nil, err
	}
	token := parseEvalToken(index)
	closed := isEvalClosed(index)
	closedMessage := evalClosedMessage(index)
	protection := parseEvalProtection(index)
	if closed {
		if closedMessage == "" {
			closedMessage = "评价暂未开启"
		}
		return &TeacherEvaluationIndexStatus{
			TokenValue: token, IsClosed: true, ClosedMessage: closedMessage,
			Protection: protection,
		}, nil
	}
	if token == "" {
		return nil, errs.NewParse("teacherEvaluation: token missing")
	}
	return &TeacherEvaluationIndexStatus{
		TokenValue: token, IsClosed: false, Protection: protection,
	}, nil
}

// TeacherEvalCourses loads evaluation courses (and index token / closed state).
func (sv *Service) TeacherEvalCourses(caller context.Context) (*TeacherEvaluationCourseList, error) {
	index, err := sv.teacherEvalIndexPage(caller)
	if err != nil {
		return nil, err
	}
	token := parseEvalToken(index)
	closed := isEvalClosed(index)
	closedMessage := evalClosedMessage(index)
	protection := parseEvalProtection(index)
	if closed {
		if closedMessage == "" {
			closedMessage = "评价暂未开启"
		}
		return &TeacherEvaluationCourseList{
			TokenValue: token, IsClosed: true, ClosedMessage: closedMessage,
			Courses: []TeacherEvaluationCourse{}, Protection: protection,
		}, nil
	}
	if token == "" {
		return nil, errs.NewParse("teacherEvaluation: token missing")
	}
	// First-visit protection: withhold the list (no search request) until the
	// caller has waited out the countdown, as the Android client does.
	if protection.RequiresInitialWait() {
		return &TeacherEvaluationCourseList{
			TokenValue: token, IsClosed: false, Courses: []TeacherEvaluationCourse{},
			Protection: protection,
		}, nil
	}
	response, err := sv.postForm(caller,
		"/student/teachingEvaluation/teachingEvaluation/search?sf_request_type=ajax",
		[][2]string{{"optType", "1"}, {"pagesize", "50"}},
		[][2]string{
			{"Accept", ajaxAccept},
			{"X-Requested-With", "XMLHttpRequest"},
			{"User-Agent", config.UAIOS},
		}, session.Read)
	if err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	var courses []TeacherEvaluationCourse
	for _, item := range common.ArrayItems(common.RawField(value, "data")) {
		if course, ok := parseEvalCourse(item); ok {
			courses = append(courses, course)
		}
	}
	return &TeacherEvaluationCourseList{
		TokenValue: token, IsClosed: false, Courses: courses, Protection: protection,
	}, nil
}

// TeacherEvalScoreMode selects how radio questions are answered.
type TeacherEvalScoreMode string

const (
	// TeacherEvalScoreRandom is the default: 80% a random full-weight option,
	// otherwise an option from the second weight group.
	TeacherEvalScoreRandom TeacherEvalScoreMode = "random"
	// TeacherEvalScoreMax picks uniformly among full-weight options, so every
	// radio answer is a top-weight answer.
	TeacherEvalScoreMax TeacherEvalScoreMode = "max"
)

// TeacherEvalPrepareSeeded prepares an evaluation form with a deterministic
// RNG seed (reproducible form_data for the given seed), using random scoring.
func (sv *Service) TeacherEvalPrepareSeeded(caller context.Context, course *TeacherEvaluationCourse, pendingCount int64, indexToken string, seed uint64) (*TeacherEvaluationPreparedForm, error) {
	return sv.TeacherEvalPrepareWithMode(caller, course, pendingCount, indexToken, seed, TeacherEvalScoreRandom)
}

// TeacherEvalPrepareWithMode prepares an evaluation form with a deterministic
// RNG seed; mode selects radio scoring. An empty mode means random.
func (sv *Service) TeacherEvalPrepareWithMode(caller context.Context, course *TeacherEvaluationCourse, pendingCount int64, indexToken string, seed uint64, mode TeacherEvalScoreMode) (*TeacherEvaluationPreparedForm, error) {
	switch mode {
	case "":
		mode = TeacherEvalScoreRandom
	case TeacherEvalScoreRandom, TeacherEvalScoreMax:
	default:
		return nil, errs.NewInvalidArgument("unknown teacher evaluation score mode: " + string(mode))
	}
	if indexToken == "" {
		return nil, errs.NewInvalidArgument("missing index token")
	}
	pageForm := evalPageForm(course, pendingCount, indexToken)
	response, err := sv.postForm(caller,
		"/student/teachingEvaluation/teachingEvaluation/evaluationPage",
		pageForm,
		[][2]string{
			{"Accept", ajaxAccept},
			{"X-Requested-With", "XMLHttpRequest"},
			{"User-Agent", config.UAIOS},
		}, session.Read)
	if err != nil {
		return nil, err
	}
	questionnaire, err := parseQuestionnaire(response.BodyText())
	if err != nil {
		return nil, err
	}
	if questionnaire.TokenValue == "" {
		return nil, errs.NewParse("evaluationPage: token missing")
	}
	if len(questionnaire.RadioQuestions) == 0 && len(questionnaire.TextQuestions) == 0 {
		return nil, errs.NewParse("evaluationPage: no questions")
	}
	rng := common.NewSimpleRng(seed)
	form := [][2]string{
		{"optType", "submit"},
		{"tokenValue", questionnaire.TokenValue},
		{"questionnaireCode", ifEmpty(course.QuestionnaireCode, questionnaire.QuestionnaireCode)},
		{"evaluationContent", ifEmpty(course.EvaluationContentNumber, questionnaire.EvaluationContent)},
		{"evaluatedPeopleNumber", ifEmpty(course.EvaluatedPeopleNumber, questionnaire.EvaluatedPeopleNumber)},
		{"count", itoa(pendingCount)},
	}
	for i := range questionnaire.RadioQuestions {
		question := &questionnaire.RadioQuestions[i]
		if option, ok := chooseOption(question, rng, mode); ok {
			form = append(form, [2]string{question.Key, option})
		}
	}
	for i := range questionnaire.TextQuestions {
		question := &questionnaire.TextQuestions[i]
		form = append(form, [2]string{question.Key, randomText(question.Kind, rng)})
	}
	return &TeacherEvaluationPreparedForm{
		Course:             *course,
		QuestionnaireTitle: questionnaire.Title,
		FormData:           form,
	}, nil
}

// TeacherEvalPrepare prepares an evaluation form using a time-seeded RNG.
func (sv *Service) TeacherEvalPrepare(caller context.Context, course *TeacherEvaluationCourse, pendingCount int64, indexToken string) (*TeacherEvaluationPreparedForm, error) {
	return sv.TeacherEvalPrepareSeeded(caller, course, pendingCount, indexToken, uint64(timeutil.NowMillis()))
}

// TeacherEvalSubmit submits a prepared evaluation (write; never auto-replayed).
func (sv *Service) TeacherEvalSubmit(caller context.Context, prepared *TeacherEvaluationPreparedForm) (*TeacherEvaluationSubmitResult, error) {
	response, err := sv.postForm(caller,
		"/student/teachingEvaluation/teachingEvaluation/assessment?sf_request_type=ajax",
		prepared.FormData,
		[][2]string{
			{"Accept", ajaxAccept},
			{"X-Requested-With", "XMLHttpRequest"},
			{"User-Agent", config.UAIOS},
		}, session.Write)
	if err != nil {
		return nil, err
	}
	body := response.BodyText()
	var parsed json.RawMessage
	if json.Valid([]byte(body)) {
		parsed = json.RawMessage(body)
	}
	result := ""
	message := ""
	if parsed != nil {
		result = common.JSONString(parsed, "result")
		message = common.JSONString(parsed, "msg")
	}
	if message == "" {
		switch {
		case strings.EqualFold(result, "success"):
			message = "提交成功"
		case parsed != nil:
			message = "提交失败，服务端返回错误"
		default:
			message = truncateRunes(body, 120)
		}
	}
	return &TeacherEvaluationSubmitResult{
		Success: strings.EqualFold(result, "success"),
		Message: message,
	}, nil
}

// TeacherEvalVerify re-loads courses and checks whether the course is evaluated.
func (sv *Service) TeacherEvalVerify(caller context.Context, course *TeacherEvaluationCourse) (bool, error) {
	list, err := sv.TeacherEvalCourses(caller)
	if err != nil {
		return false, err
	}
	for _, item := range list.Courses {
		if item.CoureSequenceNumber == course.CoureSequenceNumber &&
			item.EvaluationContentNumber == course.EvaluationContentNumber {
			return item.IsEvaluated, nil
		}
	}
	return false, nil
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func ifEmpty(primary, fallback string) string {
	if primary == "" {
		return fallback
	}
	return primary
}
