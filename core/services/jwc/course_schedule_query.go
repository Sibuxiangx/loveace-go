package jwc

import (
	"context"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// The course-offering (开课查询) pages. The query form takes every field below; the school filters the
// records itself, so a client never has to read a whole term to search it.

const (
	courseScheduleIndexPath = "/student/integratedQuery/course/courseSchdule/index"
	courseScheduleInfoPath  = "/student/integratedQuery/course/courseSchdule/courseInfo?sf_request_type=ajax"
)

// CourseScheduleQuery is one course-offering search. An empty string, or a zero weekday or section,
// is the form's "any".
type CourseScheduleQuery struct {
	TermCode   string
	CourseCode string
	CourseName string
	Teacher    string
	// Department is 开课院系, Campus 开课校区, Building 教学楼 and Category 课程类别.
	Department string
	Campus     string
	Building   string
	Category   string
	// Weekday is 上课星期, 1 to 7; Section is 开始节次, 1 to 10.
	Weekday int64
	Section int64
}

// CourseScheduleChoices are the drop-down choices of the offering query, from its index page. A choice
// the page does not show is an empty list.
type CourseScheduleChoices struct {
	Departments []ClassCurriculumOption
	Campuses    []ClassCurriculumOption
	Buildings   []ClassCurriculumOption
	Categories  []ClassCurriculumOption
}

// CourseScheduleChoices reads the offering query's drop-down choices from its index page.
func (sv *Service) CourseScheduleChoices(caller context.Context) (*CourseScheduleChoices, error) {
	response, err := sv.get(caller, courseScheduleIndexPath, nil)
	if err != nil {
		return nil, err
	}
	page := response.BodyText()
	return &CourseScheduleChoices{
		Departments: courseChoicesIn(page, "kkxsh"),
		Campuses:    courseChoicesIn(page, "kkxqh"),
		Buildings:   courseChoicesIn(page, "jxlh"),
		Categories:  courseChoicesIn(page, "kclb"),
	}, nil
}

func courseChoicesIn(page, name string) []ClassCurriculumOption {
	for _, selector := range []string{"select#" + name, `select[name="` + name + `"]`} {
		options, err := parseSelectOptions(page, selector)
		if err == nil && len(options) > 0 {
			return options
		}
	}
	return []ClassCurriculumOption{}
}

// CourseSchedulePage reads one page of course offerings for q, with every filter the school takes.
// The total is the school's count of the offerings that match.
func (sv *Service) CourseSchedulePage(caller context.Context, q CourseScheduleQuery, pageNum, pageSize int64) ([]CourseScheduleRecord, int64, error) {
	if q.Weekday < 0 || q.Weekday > freeMaxWeekday || q.Section < 0 || q.Section > freeMaxSection {
		return nil, 0, errs.NewInvalidArgument("course schedule: weekday must be 0-7 and section 0-10")
	}
	form := [][2]string{
		{"zxjxjhh", q.TermCode},
		{"kkxsh", q.Department},
		{"kkxqh", q.Campus},
		{"jxlh", q.Building},
		{"jash", ""},
		{"skxq", optionalInt(q.Weekday)},
		{"skjc", optionalInt(q.Section)},
		{"kch", q.CourseCode},
		{"kcm", q.CourseName},
		{"kclb", q.Category},
		{"skjs", q.Teacher},
		{"xqname", ""},
		{"jcname", ""},
		{"jxlname", ""},
		{"jasname", ""},
		{"pageNum", itoa(pageNum)},
		{"pageSize", itoa(pageSize)},
	}
	referer := sv.url(courseScheduleIndexPath)
	response, err := sv.postForm(caller, courseScheduleInfoPath, form, [][2]string{{"Referer", referer}}, session.Read)
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

// optionalInt is a number as a form field, or "" for the form's any.
func optionalInt(v int64) string {
	if v == 0 {
		return ""
	}
	return itoa(v)
}
