package competition

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const competitionPage1 = `<!DOCTYPE html><html><body>
<span id="ContentPlaceHolder1_lblXM">姓名：20000002</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblXkjsxf">2.5</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblKyxf">无</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblKzjslxf">1.0</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblCxcyxf">0.5</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblNlzgxf">无</span>
<span id="ContentPlaceHolder1_ContentPlaceHolder2_lblQtxf">0.0</span>
<form action="/xsXmMain.aspx" method="post">
<input type="hidden" name="__VIEWSTATE" value="VS1"/>
<input type="hidden" name="__VIEWSTATEGENERATOR" value="G1"/>
<input type="hidden" name="__EVENTVALIDATION" value="EV1"/>
</form>
<span id="gvHj_LabelPageCount">1</span>
<table id="gvHj">
<tr><td>101</td><td>互联网+</td><td>国家级</td><td>2023级</td><td>2026-01-02</td><td>20000002</td><td>张三</td><td>1</td><td>2.5</td><td>0.5</td><td>已认定</td><td>通过</td></tr>
<tr><td>当前第1页</td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td></tr>
</table>
</body></html>`

func competitionTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		On("/xsXmMain.aspx", transport.NewResponse(200, []byte(competitionPage1)))
}

func TestCompetitionInfo(t *testing.T) {
	s := session.New("20000002", "gw", "school", competitionTransport(t))
	sv := New(s)
	info, err := sv.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.StudentID != "20000002" {
		t.Fatalf("student = %q", info.StudentID)
	}
	if info.TotalAwardsCount != 1 || len(info.Awards) != 1 {
		t.Fatalf("awards = %+v", info.Awards)
	}
	award := info.Awards[0]
	if award.ProjectID != "101" || award.ProjectName != "互联网+" || award.Level != "国家级" {
		t.Fatalf("award = %+v", award)
	}
	if award.Credits != 2.5 || award.Bonus != 0.5 || award.Order != 1 {
		t.Fatalf("award numbers = %+v", award)
	}
	if info.CreditsSummary == nil {
		t.Fatal("credits summary missing")
	}
	cs := info.CreditsSummary
	if cs.DisciplineCompetitionCredits == nil || *cs.DisciplineCompetitionCredits != 2.5 {
		t.Fatalf("discipline = %v", cs.DisciplineCompetitionCredits)
	}
	if cs.ScientificResearchCredits != nil {
		t.Fatal("无 must map to nil")
	}
	total := cs.TotalCredits()
	if total != 4.0 {
		t.Fatalf("total credits = %v", total)
	}
}

func TestCheckCompetitionSession(t *testing.T) {
	if err := checkCompetitionSession("<html>var g_lines = []</html>"); err == nil {
		t.Fatal("gateway portal must be auth-required")
	}
	if err := checkCompetitionSession("<html>用户登录 login password</html>"); err == nil {
		t.Fatal("login page must be auth-required")
	}
	if err := checkCompetitionSession(competitionPage1); err != nil {
		t.Fatalf("normal page must pass: %v", err)
	}
}

func TestExtractCompetitionFormDefaults(t *testing.T) {
	form := extractCompetitionForm(competitionPage1)
	found := map[string]string{}
	for _, kv := range form {
		found[kv[0]] = kv[1]
	}
	if found["__VIEWSTATE"] != "VS1" {
		t.Fatalf("viewstate = %q", found["__VIEWSTATE"])
	}
	if found["__EVENTTARGET"] != defaultEventTarget {
		t.Fatalf("event target = %q", found["__EVENTTARGET"])
	}
	// ddlSslb is forced to %
	if found["ctl00$ContentPlaceHolder1$ContentPlaceHolder2$ddlSslb"] != "%" {
		t.Fatalf("ddls = %q", found["ctl00$ContentPlaceHolder1$ContentPlaceHolder2$ddlSslb"])
	}
}

func TestParseTotalPages(t *testing.T) {
	if got := parseTotalPages(competitionPage1); got != 1 {
		t.Fatalf("pages = %d", got)
	}
	if got := parseTotalPages("<html><span id=\"gvHj_LabelPageCount\">7</span></html>"); got != 7 {
		t.Fatalf("pages = %d", got)
	}
	if got := parseTotalPages("<html>none</html>"); got != 1 {
		t.Fatalf("default pages = %d", got)
	}
}

func TestNextPageFormUsesPostbackTarget(t *testing.T) {
	page := strings.Replace(competitionPage1,
		`<span id="gvHj_LabelPageCount">1</span>`,
		`<span id="gvHj_LabelPageCount">3</span><a id="gvHj_LinkButtonNextPage" href="javascript:__doPostBack('ctl00$gvHj$ctl02$LinkButtonNextPage','')">下一页</a>`, 1)
	form := extractNextPageForm(page, 2)
	found := map[string]string{}
	for _, kv := range form {
		found[kv[0]] = kv[1]
	}
	if found["__EVENTTARGET"] != "ctl00$gvHj$ctl02$LinkButtonNextPage" {
		t.Fatalf("event target = %q", found["__EVENTTARGET"])
	}
	if _, err := strconv.Atoi(found["__VIEWSTATE"]); err == nil && found["__VIEWSTATE"] != "VS1" {
		t.Fatalf("viewstate = %q", found["__VIEWSTATE"])
	}
}
