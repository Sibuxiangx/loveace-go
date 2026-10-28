package ykt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func yktTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		On("/casLogin.jsp", transport.NewResponse(200, []byte("<html>cas ok</html>"))).
		On("/queryUserBalances.action", transport.NewResponse(200, []byte(
			`<div><label>校园卡余额：</label><label> 88.50 元</label></div>`))).
		On("/queryUserCostList.action", transport.NewResponse(200, []byte(
			`<table><tr>
			<td>2026-01-02 08:00:00</td><td>2026-01-02 08:01</td><td>12.50</td><td></td>
			<td>消费</td><td>76.00</td><td>东区</td><td>T01</td></tr></table>`))).
		On("/utilityUnBindUserPowerPayInit.action", transport.NewResponse(200, []byte(
			`<div><label>编号</label><label>20000002</label>
			<label>姓名</label><label>张三</label>
			<label>账户状态</label><label>正常</label>
			<label>卡状态</label><label>正常</label>
			<label>校园余额</label><label>88.50</label>
			<input name="accId" value = "12345"></div>`))).
		On("/utilitBindXiaoQuData.action", transport.NewResponse(200, []byte("1,一号楼|2,二号楼||bad"))).
		On("/utilityQueryRunningAccountInfo.action", transport.NewResponse(200, []byte(
			`<table><tr><td>张三</td><td>20000002</td><td>东区</td><td>X1-101</td><td>20.00</td><td>2026-01-01</td><td>后勤处</td></tr>
			<tr><td>姓名</td><td></td><td></td><td></td><td></td><td></td><td></td></tr></table>`)))
}

func TestYktBalance(t *testing.T) {
	s := session.New("20000002", "gw", "school", yktTransport(t))
	sv := New(s)
	balance, err := sv.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balance.Balance != 88.5 || balance.BalanceText != "88.50元" {
		t.Fatalf("balance = %+v", balance)
	}
}

func TestYktTransactions(t *testing.T) {
	s := session.New("20000002", "gw", "school", yktTransport(t))
	sv := New(s)
	result, err := sv.Transactions(context.Background(), "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v", result.Records)
	}
	rec := result.Records[0]
	if rec.Expense == nil || *rec.Expense != 12.5 {
		t.Fatalf("expense = %v", rec.Expense)
	}
	if rec.Balance != 76.0 || rec.Area != "东区" || rec.TerminalID != "T01" {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.IsExpense() || rec.IsIncome() {
		t.Fatal("sign classification failed")
	}
	if rec.Amount() != -12.5 || rec.AmountText() != "-12.50元" {
		t.Fatalf("amount = %v %q", rec.Amount(), rec.AmountText())
	}
	if result.TotalExpense() != 12.5 || result.Count() != 1 {
		t.Fatalf("totals = %v %d", result.TotalExpense(), result.Count())
	}
}

func TestYktUtilityPageInfoAndOptions(t *testing.T) {
	s := session.New("20000002", "gw", "school", yktTransport(t))
	sv := New(s)
	info, err := sv.UtilityPageInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.StudentID != "20000002" || info.Name != "张三" || info.AccID != "12345" || info.Balance != 88.5 {
		t.Fatalf("info = %+v", info)
	}
	options, err := sv.UtilityOptions(context.Background(), "1", "1", "1", "X区")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Name != "一号楼" {
		t.Fatalf("options = %+v", options)
	}
}

func TestYktPurchaseHistory(t *testing.T) {
	s := session.New("20000002", "gw", "school", yktTransport(t))
	sv := New(s)
	result, err := sv.PurchaseHistory(context.Background(), "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v", result.Records)
	}
	rec := result.Records[0]
	if rec.Name != "张三" || rec.Amount != 20 || rec.Department != "后勤处" {
		t.Fatalf("record = %+v", rec)
	}
	if result.Count() != 1 || result.TotalAmount() != 20 {
		t.Fatalf("totals = %d %v", result.Count(), result.TotalAmount())
	}
}

func TestSelectOptionParsing(t *testing.T) {
	options := ParseSelectOptions("1,一号楼|2,二号楼||bad")
	if len(options) != 2 || options[0].Name != "一号楼" {
		t.Fatalf("options = %+v", options)
	}
}

func TestPaymentFormHasSourceFields(t *testing.T) {
	request := DefaultUtilityPaymentRequest()
	request.RoomID = "r1"
	request.Money = 20
	form := request.ToFormData()
	found := map[string]string{}
	for _, kv := range form {
		found[kv[0]] = kv[1]
	}
	if found["money"] != "20" || found["payType"] != "2" || found["choosePayType"] != "1" {
		t.Fatalf("form = %v", found)
	}
	// JSON field spellings match the Rust model.
	out, _ := json.Marshal(request)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	for _, key := range []string{"room_id", "pay_type", "choose_pay_type", "money"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing json key %s in %s", key, out)
		}
	}
}

func TestParsePaymentResult(t *testing.T) {
	ok, _ := parsePaymentResult(`<input id="message" value = "充值成功">`)
	if !ok.Success {
		t.Fatal("must succeed")
	}
	fail, _ := parsePaymentResult(`<input id="message" value = "余额不足">`)
	if fail.Success || fail.Message != "余额不足" {
		t.Fatalf("fail = %+v", fail)
	}
	unknown, _ := parsePaymentResult("<html>nothing</html>")
	if unknown.Success || unknown.Message != "未知结果" {
		t.Fatalf("unknown = %+v", unknown)
	}
}
