package common

import (
	"encoding/json"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func TestCleansHTML(t *testing.T) {
	if got := CleanHTMLText("<td>座位号：A12</td>"); got != "座位号：A12" {
		t.Fatalf("got %q", got)
	}
	if got := CleanHTMLText("a&nbsp;b&amp;c"); got != "a b&c" {
		t.Fatalf("got %q", got)
	}
}

func TestRNGIsDeterministic(t *testing.T) {
	a := NewSimpleRng(42)
	b := NewSimpleRng(42)
	for i := 0; i < 10; i++ {
		if a.NextU64() != b.NextU64() {
			t.Fatal("rng diverged")
		}
	}
	if NewSimpleRng(1).Below(5) >= 5 {
		t.Fatal("below must be < bound")
	}
}

func TestParsesAmounts(t *testing.T) {
	if v := ParseAmount("￥12.50元"); v == nil || *v != 12.5 {
		t.Fatalf("got %v", v)
	}
	if ParseAmount("--") != nil {
		t.Fatal("-- must not parse")
	}
}

func TestRequireSuccessRejectsNon200(t *testing.T) {
	if err := RequireSuccess(transport.NewResponse(200, []byte("<html>ok</html>"))); err != nil {
		t.Fatal(err)
	}
	err := RequireSuccess(transport.NewResponse(500, []byte("<html>login</html>")))
	var e *errs.Error
	if !asErr(err, &e) || e.Code != errs.HTTP || e.Status != 500 {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvelope(t *testing.T) {
	code, data, err := Envelope(`{"code":0,"data":{"a":1},"msg":"ok"}`)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil || m["a"] != 1 {
		t.Fatalf("data = %s", data)
	}
	// Missing code maps to -1 like the Rust default.
	code, _, err = Envelope(`{"msg":"x"}`)
	if err != nil || code != -1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestAccessors(t *testing.T) {
	value := json.RawMessage(`{"s":"str","n":42,"f":1.5,"b":1,"neg":-3,"numstr":"77","fstr":"2.25","missing":null}`)
	if JSONString(value, "s") != "str" {
		t.Fatal("string")
	}
	if JSONI64(value, "n") != 42 || JSONI64(value, "neg") != -3 || JSONI64(value, "numstr") != 77 {
		t.Fatalf("i64: %d %d %d", JSONI64(value, "n"), JSONI64(value, "neg"), JSONI64(value, "numstr"))
	}
	if JSONF64(value, "f") != 1.5 || JSONF64(value, "fstr") != 2.25 {
		t.Fatal("f64")
	}
	if !JSONBool(value, "b") {
		t.Fatal("bool")
	}
	if JSONString(value, "missing") != "" {
		t.Fatal("missing must be empty")
	}
	if JSONOptString(value, "missing") != nil {
		t.Fatal("opt missing must be nil")
	}
}

func TestExtractRecords(t *testing.T) {
	if items := ExtractRecords(json.RawMessage(`{"records":[{"a":1}]}`)); len(items) != 1 {
		t.Fatalf("records = %v", items)
	}
	if items := ExtractRecords(json.RawMessage(`{"list":{"records":[{"a":1}]}}`)); len(items) != 1 {
		t.Fatalf("nested = %v", items)
	}
	if items := ExtractRecords(json.RawMessage(`{}`)); len(items) != 0 {
		t.Fatalf("empty = %v", items)
	}
}

func TestExtractTicketFromURL(t *testing.T) {
	ticket, ok := ExtractTicketFromURL("http://x/cb?ticket=ST-123&foo=bar")
	if !ok || ticket != "ST-123" {
		t.Fatalf("ticket = %q %v", ticket, ok)
	}
	ticket, ok = ExtractTicketFromURL("http://x/cb?ticket=%E4%B8%AD")
	if !ok || ticket != "中" {
		t.Fatalf("percent decode = %q %v", ticket, ok)
	}
	if _, ok := ExtractTicketFromURL("http://x/cb?foo=1"); ok {
		t.Fatal("no ticket must not match")
	}
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
	}
	return ok
}
