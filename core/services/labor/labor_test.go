package labor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func laborTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		// Location models the vpn2 gateway's REWRITTEN redirect (see
		// aac_test.go): a healthy session never leaks the raw internal host.
		OnRequest(transport.Get, "/cas/login?service=http%3a%2f%2fapi.ldjlb", transport.NewResponse(302, []byte("m")).
			WithHeader("Location", "http://api-ldjlb-ac-acxk-net.vpn2.aufe.edu.cn:8118/register?ticket=ST-LABOR-1")).
		On("register?ticket=", transport.NewResponse(200, []byte("<html>callback</html>"))).
		OnRequest(transport.Post, "/User/Center/DoGetScoreInfo", transport.NewResponse(200, []byte(
			`{"code":0,"data":{"SumScore":1.5,"Progress":32.5}}`))).
		OnRequest(transport.Post, "/User/Activity/DoGetJoinPageList", transport.NewResponse(200, []byte(
			`{"code":0,"data":[{"ID":"A1","State":1,"StateName":"进行中","TypeName":"劳育","Title":"活动一","MemberNum":5,"PeopleNum":30,"PeopleNumMin":10,"IsJson":0,"IsClose":0,"SignUpStartTime":"2026-01-01","SignUpEndTime":"2026-01-02"}]}`))).
		OnRequest(transport.Post, "/User/Club/DoGetJoinList", transport.NewResponse(200, []byte(
			`{"code":0,"data":[{"ID":"CL1","Name":"棋社","MemberNum":12}]}`))).
		OnRequest(transport.Post, "/User/Activity/DoGetDetail", transport.NewResponse(200, []byte(
			`{"code":0,"data":{"ID":"A1","Title":"活动一"},"formData":[{"ID":"F1","Name":"衣码","IsMust":1,"FieldType":1,"Value":"L"}],"flowData":[{"FlowTypeName":"初审","ExamUserName":"老师1","IsAdopt":true,"ExamTime":"2026-01-03"}],"teacherList":[{"UserName":"老师1","UserNo":"T1"}]}`)))
}

func TestLaborTicketAndProgress(t *testing.T) {
	s := session.New("20000002", "gw", "school", laborTransport(t))
	sv := New(s)
	ticket, err := sv.Ticket(context.Background(), false)
	if err != nil || ticket != "ST-LABOR-1" {
		t.Fatalf("ticket = %q %v", ticket, err)
	}
	progress, err := sv.Progress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if progress.SumScore != 1.5 || progress.Progress != 32.5 {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.FinishCount() != 3 {
		t.Fatalf("finish count = %d", progress.FinishCount())
	}
}

func TestLaborActivitiesAndClubs(t *testing.T) {
	s := session.New("20000002", "gw", "school", laborTransport(t))
	sv := New(s)
	activities, err := sv.JoinedActivities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || activities[0].Title != "活动一" {
		t.Fatalf("activities = %+v", activities)
	}
	if activities[0].CanApply() != true {
		t.Fatalf("can apply = %v", activities[0].CanApply())
	}
	if activities[0].IsJoined == nil || *activities[0].IsJoined {
		t.Fatalf("is_joined = %v", activities[0].IsJoined)
	}
	clubs, err := sv.JoinedClubs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clubs) != 1 || clubs[0].Name != "棋社" {
		t.Fatalf("clubs = %+v", clubs)
	}
}

func TestLaborActivityDetailMergesRootFields(t *testing.T) {
	s := session.New("20000002", "gw", "school", laborTransport(t))
	sv := New(s)
	detail, err := sv.ActivityDetail(context.Background(), "A1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "活动一" {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.FormData) != 1 || detail.FormData[0].Name != "衣码" || !detail.FormData[0].IsMust {
		t.Fatalf("form data = %+v", detail.FormData)
	}
	if len(detail.FlowData) != 1 || detail.FlowData[0].UserName != "老师1" || detail.FlowData[0].IsAdopt == nil || !*detail.FlowData[0].IsAdopt {
		t.Fatalf("flow = %+v", detail.FlowData)
	}
	if len(detail.TeacherList) != 1 || detail.TeacherList[0].UserNo != "T1" {
		t.Fatalf("teachers = %+v", detail.TeacherList)
	}
}

func TestSignInResponseJSON(t *testing.T) {
	raw := SignInResponse{Code: 0, Msg: "ok", Data: json.RawMessage(`{"a":1}`)}
	if !raw.IsSuccess() {
		t.Fatal("must be success")
	}
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Rust field spellings preserved: code/msg/data.
	if string(out) != `{"code":0,"msg":"ok","data":{"a":1}}` {
		t.Fatalf("json = %s", out)
	}
}
