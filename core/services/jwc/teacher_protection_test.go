package jwc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const evalIndexURL = "teachingEvaluation/evaluation/index"

// evalSearchURL is the course search endpoint; it must never be requested
// while the first-visit protection wait is pending.
const evalSearchURL = "teachingEvaluation/teachingEvaluation/search"

// protectionIndexPage builds an open evaluation index with first-visit
// protection markers.
func protectionIndexPage(infoOff, infoTime, isFinished, isFirstVisit string) string {
	return fmt.Sprintf(`
        <html><body>
          <input id="tokenValue" value="TOK">
          <input id="schoolId" value="S1">
          <input id="infoOff" value=" %s ">
          <input id="infoTime" value="%s">
          <script>
            var isFinished = "%s"; var isFirstVisit = "%s";
            tsxxAhcj();
          </script>
        </body></html>`, infoOff, infoTime, isFinished, isFirstVisit)
}

func protectionService(t *testing.T, page string) (*Service, *transport.MockTransport) {
	t.Helper()
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, evalIndexURL, transport.NewResponse(200, []byte(page)))
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
	sv, _ := protectionService(t, alertPage)
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsClosed || status.ClosedMessage != "评估开关已关闭，请等待通知" {
		t.Fatalf("status = %+v", status)
	}

	// Closed text outside an .alert falls back to the default message.
	plainPage := `<p>评估开关已关闭</p>`
	sv, _ = protectionService(t, plainPage)
	status, err = sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsClosed || status.ClosedMessage != "评价暂未开启" {
		t.Fatalf("status = %+v", status)
	}
}

func TestTeacherEvalIndexOpenWithoutMarkersDoesNotWait(t *testing.T) {
	sv, tr := protectionService(t, `<input id="tokenValue" value="TOK">`)
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.IsClosed || status.TokenValue != "TOK" {
		t.Fatalf("status = %+v", status)
	}
	if status.Protection.RequiresInitialWait() || status.Protection.WaitSeconds() != 0 {
		t.Fatalf("protection = %+v", status.Protection)
	}
	for _, r := range tr.Recorded() {
		if r.Method == transport.Post && strings.Contains(r.URL, "/student/teachingEvaluation/") {
			t.Fatalf("TeacherEvalIndex issued POST %s", r.URL)
		}
	}
}

func TestTeacherEvalProtectionRequiresWaitWhenUnseen(t *testing.T) {
	sv, _ := protectionService(t, protectionIndexPage("1", "30", "unfinished", "unVisit"))
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := status.Protection
	if p.SchoolID != "S1" || p.InfoOff != "1" || p.InfoTimeSeconds != 30 || !p.HasAhcjTimer {
		t.Fatalf("protection = %+v", p)
	}
	if !p.RequiresInitialWait() {
		t.Fatalf("RequiresInitialWait = false, protection = %+v", p)
	}
	if p.WaitSeconds() != 35 {
		t.Fatalf("WaitSeconds = %d, want 35", p.WaitSeconds())
	}
}

func TestTeacherEvalProtectionFinishedDoesNotWait(t *testing.T) {
	sv, _ := protectionService(t, protectionIndexPage("1", "30", "finished", "unVisit"))
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Protection.RequiresInitialWait() {
		t.Fatalf("protection = %+v", status.Protection)
	}
}

func TestTeacherEvalProtectionVisitedDoesNotWait(t *testing.T) {
	sv, _ := protectionService(t, protectionIndexPage("1", "30", "unfinished", "visited"))
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Protection.RequiresInitialWait() {
		t.Fatalf("protection = %+v", status.Protection)
	}
}

func TestTeacherEvalProtectionCoursesWithheldWithoutSearch(t *testing.T) {
	sv, tr := protectionService(t, protectionIndexPage("1", "30", "unfinished", "unVisit"))
	list, err := sv.TeacherEvalCourses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Courses) != 0 || list.IsClosed || list.TokenValue != "TOK" {
		t.Fatalf("list = %+v", list)
	}
	if !list.Protection.RequiresInitialWait() {
		t.Fatalf("protection = %+v", list.Protection)
	}
	if urls := searchRequests(tr); len(urls) != 0 {
		t.Fatalf("search requested during protection wait: %v", urls)
	}
}

func TestTeacherEvalProtectionInfoOffZeroDoesNotWait(t *testing.T) {
	sv, _ := protectionService(t, protectionIndexPage("0", "30", "unfinished", "unVisit"))
	status, err := sv.TeacherEvalIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Protection.RequiresInitialWait() {
		t.Fatalf("protection = %+v", status.Protection)
	}
}
