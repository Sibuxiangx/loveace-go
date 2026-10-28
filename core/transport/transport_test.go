package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockTransportMatchesAndRecords(t *testing.T) {
	mock := NewMockTransport().
		On("/jwc/data", NewResponse(200, []byte(`{"ok":true}`))).
		OnRequest(Post, "/login", NewResponse(302, []byte("moved")).WithHeader("Location", "/next"))

	resp, err := mock.Execute(context.Background(), NewRequest(Get, "http://x/jwc/data"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.FinalURL != "http://x/jwc/data" {
		t.Fatalf("FinalURL = %q", resp.FinalURL)
	}

	if _, err := mock.Execute(context.Background(), NewRequest(Post, "http://x/login")); err != nil {
		t.Fatal(err)
	}
	if len(mock.Recorded()) != 2 {
		t.Fatalf("recorded = %d", len(mock.Recorded()))
	}
	if mock.LastRequest().Method != Post {
		t.Fatalf("last = %+v", mock.LastRequest())
	}
}

func TestMockLongestPatternWins(t *testing.T) {
	mock := NewMockTransport().
		On("j_spring_cas_security_check", NewResponse(200, []byte("bare"))).
		On("j_spring_cas_security_check?ticket=", NewResponse(200, []byte("ticket")))
	resp, err := mock.Execute(context.Background(),
		NewRequest(Get, "http://jwc/j_spring_cas_security_check?ticket=ST1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "ticket" {
		t.Fatalf("longest match failed: %q", resp.Body)
	}
}

func TestMockNoRouteReturns404(t *testing.T) {
	mock := NewMockTransport()
	resp, err := mock.Execute(context.Background(), NewRequest(Get, "http://x/none"))
	if err != nil {
		t.Fatal(err)
	}
	// Rust parity: unmatched requests return a synthetic 404 so tests fail
	// loudly rather than touching the network.
	if resp.Status != 404 {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestBodyTextCharset(t *testing.T) {
	utf8Resp := NewResponse(200, []byte(`{"a":"北京"}`))
	if got := utf8Resp.BodyText(); got != `{"a":"北京"}` {
		t.Fatalf("utf8: %q", got)
	}
	enc := gbkEncoderEncode(t, "学号123")
	gbkResp := NewResponse(200, enc).WithHeader("Content-Type", "text/html; charset=gbk")
	if got := gbkResp.BodyText(); got != "学号123" {
		t.Fatalf("gbk decode: %q", got)
	}
}

func TestSetCookieHeadersAndRedirect(t *testing.T) {
	r := NewResponse(302, nil).
		WithHeader("Set-Cookie", "A=1; Path=/").
		WithHeader("set-cookie", "B=2")
	if got := r.SetCookieHeaders(); len(got) != 2 || got[0] != "A=1; Path=/" {
		t.Fatalf("set-cookies = %v", got)
	}
	if !r.IsRedirect() {
		t.Fatal("302 must be a redirect")
	}
}

// TestEmptyPostSendsContentLengthZero mirrors the Rust wire-behaviour test:
// the school gateway replies 411 to a length-less empty form POST.
func TestEmptyPostSendsContentLengthZero(t *testing.T) {
	var gotMethod, gotLength, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		buf, _ := io.ReadAll(r.Body)
		_ = buf
		gotLength = r.Header.Get("Content-Length")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tset := NewHTTPTransport(0)
	req := NewRequest(Post, srv.URL+"/empty").Header("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tset.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	if gotMethod != "POST" {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotLength != "0" {
		t.Fatalf("Content-Length = %q, want 0", gotLength)
	}
	if gotUA != UADesktopDefault {
		t.Fatalf("UA = %q", gotUA)
	}
}

func TestNonCanonicalHeadersPreserved(t *testing.T) {
	var gotTicket, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTicket = r.Header.Get("ticket")
		gotSession = r.Header.Get("sdp-app-session")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tset := NewHTTPTransport(0)
	req := NewRequest(Post, srv.URL+"/").
		Header("ticket", "ST-123").
		Header("sdp-app-session", "twfid-1")
	if _, err := tset.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if gotTicket != "ST-123" || gotSession != "twfid-1" {
		t.Fatalf("ticket=%q session=%q", gotTicket, gotSession)
	}
}

func TestMaxBytesBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()
	tset := NewHTTPTransport(0)
	req := NewRequest(Get, srv.URL).WithMaxBytes(10)
	_, err := tset.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected size-bound error")
	}
}

const UADesktopDefault = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

func gbkEncoderEncode(t *testing.T, s string) []byte {
	t.Helper()
	out, err := gbkDecoder.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
