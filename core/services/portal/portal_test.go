package portal

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const testDataKey = "0123456789abcdef0123456789abcdef"

// portalWalk wires the CAS walk into the portal: the gateway rewrites the
// service redirect into the vpn2 namespace, which then lands on the -s host.
func portalWalk(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		OnRequest(transport.Get, "/cas/login?service=http%3A%2F%2Fall", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://all-aufe-edu-cn.vpn2.aufe.edu.cn:8118/wui/cas-entrance.jsp?ticket=ST-PORTAL")).
		OnRequest(transport.Get, "/wui/cas-entrance.jsp?ticket=", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://all-aufe-edu-cn-s.vpn2.aufe.edu.cn:8118/wui/index.html")).
		OnRequest(transport.Get, "/wui/index.html", transport.NewResponse(200, []byte("<html>portal</html>"))).
		OnRequest(transport.Get, "/sf-webproxy/api/vpn-config", transport.NewResponse(200, []byte("{}")))
}

// portalTransport adds a healthy hpdata and one cube: getList opens the key,
// counts reports count rows, and datas answers with the given table JSON.
func portalTransport(t *testing.T, count int, datas string) *transport.MockTransport {
	t.Helper()
	return portalWalk(t).
		OnRequest(transport.Post, "/api/portal/homepage/hpdata", transport.NewResponse(200, []byte(`{"hpinfo":{"userid":"10001"}}`))).
		OnRequest(transport.Post, "/api/cube/search/getList", transport.NewResponse(200, []byte(`{"datas":"`+testDataKey+`","status":"1"}`))).
		OnRequest(transport.Post, "/api/ec/dev/table/counts", transport.NewResponse(200, []byte(fmt.Sprintf(`{"count":%d,"status":true}`, count)))).
		OnRequest(transport.Post, "/api/ec/dev/table/datas", transport.NewResponse(200, []byte(datas)))
}

func countRequests(tr *transport.MockTransport, method transport.Method, substr string) int {
	n := 0
	for _, r := range tr.Recorded() {
		if r.Method == method && strings.Contains(r.URL, substr) {
			n++
		}
	}
	return n
}

func formOf(t *testing.T, r *transport.Request) url.Values {
	t.Helper()
	values, err := url.ParseQuery(string(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestDormitoryAccessRowsMapDirectionAndDetail(t *testing.T) {
	datas := `{"columns":[{"title":"通过时间","dbField":"通过时间"},{"title":"通过方向","dbField":"通过方向"},{"title":"系统名称","dbField":"系统名称"},{"title":"数据ID","dbField":"ID"}],` +
		`"datas":[{"通过时间":"2026-11-10 08:00:00","通过方向":"0","系统名称":"示例门禁","ID":"A1"}]}`
	tr := portalTransport(t, 2, datas)
	page, err := New(session.New("20000001", "gw", "school", tr)).AccessRecords(context.Background(), "dormitory", "this_week", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Records) != 1 {
		t.Fatalf("page = %+v", page)
	}
	got := page.Records[0]
	if got.ID != "A1" || got.Time != "2026-11-10 08:00:00" || got.Direction != "entry" || got.Detail != "示例门禁" {
		t.Fatalf("record = %+v", got)
	}
}

// The library's this-week code is the two-field form "2,,"; the dormitory's is "2".
func TestAccessRangeCodeDiffersForLibraryThisWeek(t *testing.T) {
	for kind, want := range map[string]string{"library": "2,,", "dormitory": "2"} {
		tr := portalTransport(t, 0, `{"columns":[],"datas":[]}`)
		if _, err := New(session.New("20000001", "gw", "school", tr)).AccessRecords(context.Background(), kind, "this_week", 1); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var listed *transport.Request
		for _, r := range tr.Recorded() {
			if strings.Contains(r.URL, "/api/cube/search/getList") {
				listed = r
			}
		}
		field := map[string]string{"library": libraryDateField, "dormitory": dormDateField}[kind]
		if got := formOf(t, listed).Get(field); got != want {
			t.Fatalf("%s this_week code = %q, want %q", kind, got, want)
		}
	}
}

func TestBadArgumentsFailBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		kind string
		rng  string
		page int
	}{
		{name: "unknown kind", kind: "canteen", rng: "all", page: 1},
		{name: "unknown range", kind: "dormitory", rng: "forever", page: 1},
		{name: "zero page", kind: "dormitory", rng: "all", page: 0},
	}
	for _, c := range cases {
		tr := transport.NewMockTransport()
		_, err := New(session.New("20000001", "gw", "school", tr)).AccessRecords(context.Background(), c.kind, c.rng, c.page)
		if errs.CodeOf(err) != errs.InvalidArgument {
			t.Fatalf("%s: code = %v, want invalid_argument", c.name, errs.CodeOf(err))
		}
		if n := len(tr.Recorded()); n != 0 {
			t.Fatalf("%s: %d requests reached the network", c.name, n)
		}
	}
}

func TestBorrowingSplitsCurrentFromHistory(t *testing.T) {
	datas := `{"columns":[{"title":"书名","dbField":"书名"},{"title":"借阅时间","dbField":"借阅时间"},` +
		`{"title":"借书时间","dbField":"借书时间"},{"title":"还书时间","dbField":"还书时间"},{"title":"数据ID","dbField":"ID"}],` +
		`"datas":[{"书名":"示例书","借阅时间":"2026-11-01","借书时间":"2026-10-01","还书时间":"2026-10-20","ID":"L1"}]}`
	tr := portalTransport(t, 1, datas)
	loans, err := New(session.New("20000001", "gw", "school", tr)).Borrowing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loans.Current) != 1 || loans.Current[0].BorrowedAt != "2026-11-01" || loans.Current[0].ReturnedAt != nil {
		t.Fatalf("current = %+v", loans.Current)
	}
	if len(loans.History) != 1 || loans.History[0].BorrowedAt != "2026-10-01" ||
		loans.History[0].ReturnedAt == nil || *loans.History[0].ReturnedAt != "2026-10-20" {
		t.Fatalf("history = %+v", loans.History)
	}
}

func TestConsumptionRowsMapPortalColumns(t *testing.T) {
	datas := `{"columns":[{"title":"交易日期","dbField":"交易日期"},{"title":"交易时间","dbField":"交易时间"},` +
		`{"title":"交易地点","dbField":"交易地点"},{"title":"交易金额","dbField":"交易金额"},{"title":"数据ID","dbField":"ID"}],` +
		`"datas":[{"交易日期":"2026-11-10","交易时间":"12:10:00","交易地点":"示例食堂","交易金额":"12.50","ID":"C1"}]}`
	tr := portalTransport(t, 1, datas)
	page, err := New(session.New("20000001", "gw", "school", tr)).ConsumptionRecords(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := ConsumptionRecord{ID: "C1", Date: "2026-11-10", Time: "12:10:00", Area: "示例食堂", Amount: "12.50"}
	if page.Total != 1 || len(page.Records) != 1 || page.Records[0] != want {
		t.Fatalf("page = %+v", page)
	}
	if got := formOf(t, lastCubeList(tr)).Get(consumptionDateField); got != consumptionMonth {
		t.Fatalf("consumption date code = %q, want the month code", got)
	}
}

func lastCubeList(tr *transport.MockTransport) *transport.Request {
	var out *transport.Request
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, listPath) {
			out = r
		}
	}
	return out
}

func TestMailInitialPasswordIsNilWithoutRows(t *testing.T) {
	tr := portalTransport(t, 0, `{"columns":[],"datas":[]}`)
	got, err := New(session.New("20000001", "gw", "school", tr)).MailInitialPassword(context.Background())
	if err != nil || got != nil {
		t.Fatalf("mail = %+v, %v; want nil without error", got, err)
	}
}

func TestMailInitialPasswordMapsRow(t *testing.T) {
	datas := `{"columns":[{"title":"登录名","dbField":"LOGINID"},{"title":"邮箱初始密码","dbField":"DEFAULTPWD"}],` +
		`"datas":[{"LOGINID":"student.one","DEFAULTPWD":"Init-0001"}]}`
	tr := portalTransport(t, 1, datas)
	got, err := New(session.New("20000001", "gw", "school", tr)).MailInitialPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.LoginID != "student.one" || got.Password != "Init-0001" {
		t.Fatalf("mail = %+v", got)
	}
}

func TestMailInitialPasswordRejectsEmptyPassword(t *testing.T) {
	datas := `{"columns":[{"title":"登录名","dbField":"LOGINID"},{"title":"邮箱初始密码","dbField":"DEFAULTPWD"}],` +
		`"datas":[{"LOGINID":"student.one","DEFAULTPWD":""}]}`
	tr := portalTransport(t, 1, datas)
	_, err := New(session.New("20000001", "gw", "school", tr)).MailInitialPassword(context.Background())
	if errs.CodeOf(err) != errs.Parse {
		t.Fatalf("code = %v, want parse", errs.CodeOf(err))
	}
}

// A portal that answers with its login page has expired the session. The
// read replays once after a fresh handshake, then reports the expiry.
func TestLoginPageReplaysOnceThenReportsExpiry(t *testing.T) {
	tr := portalWalk(t).
		OnRequest(transport.Post, "/api/portal/homepage/hpdata", transport.NewResponse(200, []byte(`{"hpinfo":{"userid":"10001"}}`))).
		OnRequest(transport.Post, "/api/cube/search/getList", transport.NewResponse(200, []byte(`<html>login_auth.csp</html>`)))
	_, err := New(session.New("20000001", "gw", "school", tr)).Borrowing(context.Background())
	if errs.CodeOf(err) != errs.AuthRequired {
		t.Fatalf("code = %v, want auth_required", errs.CodeOf(err))
	}
	if n := countRequests(tr, transport.Post, "/api/portal/homepage/hpdata"); n != 2 {
		t.Fatalf("hpdata sent %d times, want 2 (one replay)", n)
	}
}

// A VPN security intercept starts a cooldown. Calls inside the window fail
// without touching the network, so a retry storm cannot extend the block.
func TestSecurityInterceptCoolsDownWithoutNetwork(t *testing.T) {
	tr := portalWalk(t).
		OnRequest(transport.Post, "/api/portal/homepage/hpdata",
			transport.NewResponse(404, []byte("")).WithHeader("Errormsg", "securityIntercept"))
	sv := New(session.New("20000001", "gw", "school", tr))
	if _, err := sv.Borrowing(context.Background()); errs.CodeOf(err) != errs.TemporarilyUnavailable {
		t.Fatalf("first call code = %v, want temporarily_unavailable", errs.CodeOf(err))
	}
	if _, err := sv.Borrowing(context.Background()); errs.CodeOf(err) != errs.TemporarilyUnavailable {
		t.Fatalf("second call code = %v, want temporarily_unavailable", errs.CodeOf(err))
	}
	if n := countRequests(tr, transport.Post, "/api/portal/homepage/hpdata"); n != 1 {
		t.Fatalf("hpdata sent %d times during cooldown, want 1", n)
	}
}
