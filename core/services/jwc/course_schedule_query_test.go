package jwc

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func TestCourseSchedulePageSendsEveryFilter(t *testing.T) {
	tr := freeClassroomTransport(t).OnRequest(transport.Post, "courseSchdule/courseInfo", transport.NewResponse(200, []byte(
		`{"list":{"records":[],"pageContext":{"totalCount":0}}}`)))
	_, total, err := freeClassroomService(tr).CourseSchedulePage(context.Background(), CourseScheduleQuery{
		TermCode: "2025-1", CourseName: "高数", Teacher: "张", Department: "d1",
		Campus: "2", Building: "36", Category: "c1", Weekday: 2, Section: 3,
	}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	form := requestForm(t, tr, "courseSchdule/courseInfo")
	want := map[string]string{
		"zxjxjhh": "2025-1", "kcm": "高数", "skjs": "张", "kkxsh": "d1",
		"kkxqh": "2", "jxlh": "36", "kclb": "c1", "skxq": "2", "skjc": "3",
		"kch": "", "pageNum": "1", "pageSize": "20",
	}
	for field, value := range want {
		if got := form.Get(field); got != value {
			t.Errorf("%s = %q, want %q", field, got, value)
		}
	}
}

func TestCourseSchedulePageLeavesUnsetFiltersAsAny(t *testing.T) {
	tr := freeClassroomTransport(t).OnRequest(transport.Post, "courseSchdule/courseInfo", transport.NewResponse(200, []byte(
		`{"list":{"records":[],"pageContext":{"totalCount":0}}}`)))
	if _, _, err := freeClassroomService(tr).CourseSchedulePage(context.Background(), CourseScheduleQuery{TermCode: "2025-1"}, 1, 20); err != nil {
		t.Fatal(err)
	}
	form := requestForm(t, tr, "courseSchdule/courseInfo")
	if form.Get("skxq") != "" || form.Get("skjc") != "" {
		t.Errorf("weekday and section = %q/%q, want empty for any", form.Get("skxq"), form.Get("skjc"))
	}
}

func TestCourseSchedulePageRejectsOutOfRangeWeekdayWithoutRequest(t *testing.T) {
	tr := freeClassroomTransport(t)
	_, _, err := freeClassroomService(tr).CourseSchedulePage(context.Background(), CourseScheduleQuery{TermCode: "2025-1", Weekday: 8}, 1, 20)
	if errs.CodeOf(err) != errs.InvalidArgument {
		t.Fatalf("code = %v, want invalid argument", errs.CodeOf(err))
	}
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, "courseInfo") {
			t.Fatalf("an out-of-range weekday must not reach the school: %s", r.URL)
		}
	}
}

func TestCourseScheduleChoicesReadTheIndexSelects(t *testing.T) {
	tr := freeClassroomTransport(t).OnRequest(transport.Get, courseScheduleIndexPath, transport.NewResponse(200, []byte(
		`<html><select id="kkxsh"><option value="">全部</option><option value="d1">计算机学院</option></select>`+
			`<select name="kclb"><option value="c1">专业课</option></select></html>`)))
	choices, err := freeClassroomService(tr).CourseScheduleChoices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(choices.Departments) != 1 || choices.Departments[0].Code != "d1" || choices.Departments[0].Name != "计算机学院" {
		t.Fatalf("departments = %+v, want the one named department", choices.Departments)
	}
	if len(choices.Categories) != 1 || choices.Categories[0].Code != "c1" {
		t.Fatalf("categories = %+v, want the one category", choices.Categories)
	}
	if len(choices.Campuses) != 0 || len(choices.Buildings) != 0 {
		t.Fatalf("campuses/buildings = %+v/%+v, want empty for missing selects", choices.Campuses, choices.Buildings)
	}
}
