package jwc

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const evalIndexURL = "teachingEvaluation/evaluation/index"

// evalSearchURL is the course search endpoint.
const evalSearchURL = "teachingEvaluation/teachingEvaluation/search"

func evalService(t *testing.T, page string) (*Service, *transport.MockTransport) {
	t.Helper()
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, evalIndexURL, transport.NewResponse(200, []byte(page))).
		OnRequest(transport.Post, evalSearchURL, transport.NewResponse(200, []byte(`{"data":[]}`)))
	return New(session.New("20000002", "gw", "school", tr)), tr
}

func searchRequests(tr *transport.MockTransport) []string {
	var urls []string
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, evalSearchURL) {
			urls = append(urls, r.URL)
		}
	}
	return urls
}

func TestTeacherEvalIndexClosedDefaultsMessage(t *testing.T) {
	// Closed text inside an .alert is used verbatim.
	alertPage := `<div class="alert">评估开关已关闭，请等待通知</div>`
	sv, _ := evalService(t, alertPage)
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsClosed || status.ClosedMessage != "评估开关已关闭，请等待通知" {
		t.Fatalf("status = %+v", status)
	}

	// Closed text outside an .alert falls back to the default message.
	plainPage := `<p>评估开关已关闭</p>`
	sv, _ = evalService(t, plainPage)
	status, err = sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsClosed || status.ClosedMessage != "评价暂未开启" {
		t.Fatalf("status = %+v", status)
	}
}

func TestTeacherEvalIndexIsReadOnly(t *testing.T) {
	sv, tr := evalService(t, `<input id="tokenValue" value="TOK">`)
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.IsClosed || status.TokenValue != "TOK" {
		t.Fatalf("status = %+v", status)
	}
	for _, r := range tr.Recorded() {
		if r.Method == transport.Post && strings.Contains(r.URL, "/student/teachingEvaluation/") {
			t.Fatalf("TeacherEvalIndex issued POST %s", r.URL)
		}
	}
}

// Page state never withholds the list: the search runs even when the page
// carries first-visit markers. The engine owns the wait after the visit.
func TestTeacherEvalCoursesIgnoresPageState(t *testing.T) {
	page := `<input id="tokenValue" value="TOK"><script>var isFirstVisit = "unVisit";</script>`
	sv, tr := evalService(t, page)
	list, err := sv.TeacherEvalCourses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list.IsClosed || list.TokenValue != "TOK" {
		t.Fatalf("list = %+v", list)
	}
	if urls := searchRequests(tr); len(urls) != 1 {
		t.Fatalf("search requests = %v, want exactly one", urls)
	}
}

func TestTeacherEvalCoursesClosedSkipsSearch(t *testing.T) {
	sv, tr := evalService(t, `<div class="alert">评估开关已关闭</div>`)
	list, err := sv.TeacherEvalCourses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !list.IsClosed || len(list.Courses) != 0 {
		t.Fatalf("list = %+v", list)
	}
	if urls := searchRequests(tr); len(urls) != 0 {
		t.Fatalf("search requested while closed: %v", urls)
	}
}
