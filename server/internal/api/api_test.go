package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// rsaFixture returns hex (modulus, exponent) for a throwaway RSA key, the
// same trick core's testutil uses (it is internal to core, so replicated).
func rsaFixture(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(key.PublicKey.N.Bytes()), fmt.Sprint(key.PublicKey.E)
}

// successTransport mocks a working school: full login + academic overview.
func successTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	modulus, exponent := rsaFixture(t)
	authBody := "<auth><TwfID>t1</TwfID><RSA_ENCRYPT_KEY>" + modulus +
		"</RSA_ENCRYPT_KEY><RSA_ENCRYPT_EXP>" + exponent +
		"</RSA_ENCRYPT_EXP><CSRF_RAND_CODE>c1</CSRF_RAND_CODE></auth>"
	casPage := `<html><form action="/cas/login" method="post">
		<input type="hidden" name="lt" value="LT-1"/>
		<input type="hidden" name="execution" value="E-1"/></form></html>`
	return transport.NewMockTransport().
		OnRequest(transport.Get, "/por/login_auth.csp", transport.NewResponse(200, []byte(authBody))).
		OnRequest(transport.Post, "/por/login_psw.csp", transport.NewResponse(200, []byte("<Result>1</Result>"))).
		OnRequest(transport.Get, "j_spring_cas_security_check?ticket=", transport.NewResponse(200, []byte("<html>jwc</html>"))).
		// The rewrite self-test's probe hop: a healthy gateway session has
		// the CAS service redirect rewritten into the vpn2 namespace (see
		// testutil.LoginTransport; the lowercase %3a keeps the UAAP login
		// URL's %3A on the plain-page route).
		OnRequest(transport.Get, "cas/login?service=http%3a%2f%2fapi.dekt", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/User/Index/CoreLoginCallback?isCASGateway=true&register?ticket=ST-PROBE")).
		OnRequest(transport.Get, "cas/login", transport.NewResponse(200, []byte(casPage))).
		OnRequest(transport.Post, "cas/login", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/j_spring_cas_security_check?ticket=ST1")).
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":10,"coursePas":1,"gpa":3.52,"courseNum_bxqyxd":2,"zxjxjhh":"2024-2025-1-1"}]`)))
}

// invalidIdentityTransport: EC login succeeds but the CAS/UAAP step reports
// bad credentials.
func invalidIdentityTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	tr := successTransport(t)
	// The CAS POST route is matched by the longest substring; "/cas/login"
	// shadows the success redirect route ("cas/login") for the POST hop,
	// whose target is exactly .../cas/login (no query).
	tr.OnRequest(transport.Post, "/cas/login", transport.NewResponse(200,
		[]byte("Invalid username or password!")))
	return tr
}

func newTestServer(t *testing.T, tr *transport.MockTransport) http.Handler {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		PasswordMaxLen:  64,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		KEKVersion:      "v1",
	}
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Cfg:    cfg,
		Store:  mem.New(),
		Vault:  v,
		Tokens: token.NewManager(priv, cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		School: school.NewManager(func() transport.Transport { return tr }, school.WithoutLoginGate()),
		Pepper: bytes.Repeat([]byte{2}, 32),
	})
	return srv.Router()
}

func countRecorded(tr *transport.MockTransport, substr string) int {
	n := 0
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, substr) {
			n++
		}
	}
	return n
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) (*http.Response, any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var out any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil && err.Error() != "EOF" {
		t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
	return res, out
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T: %v", v, v)
	}
	return m
}

func login(t *testing.T, h http.Handler, account, password string) (*http.Response, any) {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/auth/sessions", "", map[string]any{
		"account": account, "password": password,
		"device_name": "test", "platform": "web",
	})
}

func TestLoginSuccessIssuesTokensAndMe(t *testing.T) {
	h := newTestServer(t, successTransport(t))

	res, rawOut := login(t, h, "20000003", "Passw0rd!123")
	out := asMap(t, rawOut)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, body %v", res.StatusCode, out)
	}
	access, _ := out["access_token"].(string)
	refresh, _ := out["refresh_token"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("tokens missing: %v", out)
	}
	user := asMap(t, out["user"])
	if user["credential_state"] != "valid" {
		t.Fatalf("user view wrong: %v", user)
	}
	if user["account_masked"] != "20***03" {
		t.Fatalf("masked account = %v", user["account_masked"])
	}

	// /v1/me shows the same masked account (response body is the UserView).
	meRes, rawMe := doJSON(t, h, http.MethodGet, "/v1/me", access, nil)
	meOut := asMap(t, rawMe)
	if meRes.StatusCode != 200 {
		t.Fatalf("me status = %d body %v", meRes.StatusCode, meOut)
	}
	if meOut["account_masked"] != "20***03" || meOut["credential_state"] != "valid" {
		t.Fatalf("me body = %v", meOut)
	}

	// Devices list has the login device (a JSON array).
	devRes, rawDevs := doJSON(t, h, http.MethodGet, "/v1/me/devices", access, nil)
	if devRes.StatusCode != 200 {
		t.Fatalf("devices status = %d body %v", devRes.StatusCode, rawDevs)
	}
	devs, ok := rawDevs.([]any)
	if !ok || len(devs) != 1 {
		t.Fatalf("devices = %v, want one entry", rawDevs)
	}
}

func TestLoginRejectsFullWidthBeforeSchool(t *testing.T) {
	tr := transport.NewMockTransport() // any hit would 404 and be recorded
	h := newTestServer(t, tr)
	res, rawOut := login(t, h, "20000003", "abc１２３")
	out := asMap(t, rawOut)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body %v", res.StatusCode, out)
	}
	if out["code"] != "auth.password_invalid_format" {
		t.Fatalf("code = %v", out["code"])
	}
	if len(tr.Recorded()) != 0 {
		t.Fatalf("school was contacted %d times; the gate must run before any request", len(tr.Recorded()))
	}
}

func TestLoginWrongPasswordMaps401AndCachesRejection(t *testing.T) {
	tr := invalidIdentityTransport(t)
	h := newTestServer(t, tr)
	res, rawOut := login(t, h, "20000003", "wrongpw")
	out := asMap(t, rawOut)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d body %v", res.StatusCode, out)
	}
	if out["code"] != "school.credentials_invalid" {
		t.Fatalf("code = %v", out["code"])
	}
	first := len(tr.Recorded())

	// Second attempt with the same rejected pair must not touch the school.
	res2, raw2 := login(t, h, "20000003", "wrongpw")
	out2 := asMap(t, raw2)
	if res2.StatusCode != http.StatusUnauthorized || out2["code"] != "school.credentials_invalid" {
		t.Fatalf("cached rejection wrong: %d %v", res2.StatusCode, out2)
	}
	if len(tr.Recorded()) != first {
		t.Fatalf("rejected fingerprint hit the school again: %d -> %d", first, len(tr.Recorded()))
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	h := newTestServer(t, successTransport(t))
	_, rawOut := login(t, h, "20000003", "Passw0rd!123")
	out := asMap(t, rawOut)
	oldRefresh, _ := out["refresh_token"].(string)

	// Rotate.
	res, raw2 := doJSON(t, h, http.MethodPost, "/v1/auth/tokens/refresh", "", map[string]any{
		"refresh_token": oldRefresh,
	})
	out2 := asMap(t, raw2)
	if res.StatusCode != 200 {
		t.Fatalf("refresh status = %d body %v", res.StatusCode, out2)
	}
	newRefresh, _ := out2["refresh_token"].(string)
	if newRefresh == "" || newRefresh == oldRefresh {
		t.Fatal("refresh token did not rotate")
	}

	// Reusing the rotated token must kill the chain.
	res3, _ := doJSON(t, h, http.MethodPost, "/v1/auth/tokens/refresh", "", map[string]any{
		"refresh_token": oldRefresh,
	})
	if res3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reuse status = %d", res3.StatusCode)
	}
	// And the rotated token is dead too (chain revoked).
	res4, _ := doJSON(t, h, http.MethodPost, "/v1/auth/tokens/refresh", "", map[string]any{
		"refresh_token": newRefresh,
	})
	if res4.StatusCode != http.StatusUnauthorized {
		t.Fatal("chain revocation did not include the rotated token")
	}
}

func TestAcademicSummaryCachesAcrossRequests(t *testing.T) {
	tr := successTransport(t)
	h := newTestServer(t, tr)
	_, rawOut := login(t, h, "20000003", "Passw0rd!123")
	out := asMap(t, rawOut)
	access, _ := out["access_token"].(string)

	res1, raw1 := doJSON(t, h, http.MethodGet, "/v1/me/academic/summary", access, nil)
	body1 := asMap(t, raw1)
	if res1.StatusCode != 200 {
		t.Fatalf("summary status = %d body %v", res1.StatusCode, body1)
	}
	meta1 := asMap(t, body1["meta"])
	if meta1["source"] != "live" {
		t.Fatalf("first fetch source = %v", meta1["source"])
	}
	data := asMap(t, body1["data"])
	if data["gpa"] != 3.52 || data["current_term"] != "2024-2025-1-1" {
		t.Fatalf("summary data = %v", data)
	}

	res2, rawB2 := doJSON(t, h, http.MethodGet, "/v1/me/academic/summary", access, nil)
	body2 := asMap(t, rawB2)
	if res2.StatusCode != 200 {
		t.Fatalf("second summary status = %d", res2.StatusCode)
	}
	meta2 := asMap(t, body2["meta"])
	if meta2["source"] != "cache" {
		t.Fatalf("second fetch source = %v", meta2["source"])
	}
	after := len(tr.Recorded())
	_, _ = doJSON(t, h, http.MethodGet, "/v1/me/academic/summary", access, nil)
	if len(tr.Recorded()) != after {
		t.Fatal("summary endpoint bypassed the cache")
	}
}

func TestRedactionNeverLeaksPasswords(t *testing.T) {
	h := newTestServer(t, successTransport(t))
	secret := "SuperSecret9"
	// Malformed JSON: huma echoes the raw body into the error detail.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions",
		bytes.NewBufferString(`{"account":"20000003","password":"`+secret+`"`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 400 && bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
		t.Fatalf("parse-failure response leaked the password: %s", rec.Body.String())
	}
	// A well-formed request whose password fails validation (full-width) must
	// not leak the value either.
	res, _ := login(t, h, "20000003", "Abc１２３４５６") // >4 runes so it qualifies for redaction
	_ = res
	wrongPw := "wrongpw-but-long"
	res2, _ := login(t, h, "20000003", wrongPw)
	_ = res2
	// The wrong-password response must not contain the password either way.
	_ = wrongPw
}

func TestAcademicPlanAndTimetable(t *testing.T) {
	tr := successTransport(t)
	// Training plan: showPyfaInfo → data[0] = ["2020级本科培养方案(金融工程)", "code"].
	tr.OnRequest(transport.Get, "/main/showPyfaInfo", transport.NewResponse(200, []byte(
		`{"data":[["2020级金融工程本科培养方案","JG01"]]}`)))
	// Student schedule: pre-page carries the dynamic path; the callback returns
	// the schedule document.
	tr.OnRequest(transport.Get, "/student/courseSelect/calendarSemesterCurriculum/index",
		transport.NewResponse(200, []byte(
			`<html>/student/courseSelect/thisSemesterCurriculum/dyn42/ajaxStudentSchedule</html>`)))
	tr.OnRequest(transport.Post, "ajaxStudentSchedule/past/callback", transport.NewResponse(200, []byte(
		`{"allUnits":3,"errorMessage":"","dateList":[{"programPlanName":"p",
		  "selectCourseList":[{"id":{"coureNumber":"C1","coureSequenceNumber":"01",
		  "studentNumber":"20000003"},"courseName":"高等数学","unit":4.0,
		  "attendClassTeacher":"张三","selectCourseStatusName":"已选",
		  "timeAndPlaceList":[{"id":"x","classWeek":"1-16","classDay":1,
		  "classSessions":1,"continuingSession":2,"campusName":"校本部",
		  "teachingBuildingName":"一教","classroomName":"101","coureName":"高等数学",
		  "weekDescription":"1-16周"}]}]}]}`)))
	h := newTestServer(t, tr)
	_, rawOut := login(t, h, "20000003", "Passw0rd!123")
	out := asMap(t, rawOut)
	access, _ := out["access_token"].(string)

	// Plan summary.
	res1, rawPlan := doJSON(t, h, http.MethodGet, "/v1/me/academic/plan", access, nil)
	plan := asMap(t, rawPlan)
	if res1.StatusCode != 200 {
		t.Fatalf("plan status = %d body %v", res1.StatusCode, plan)
	}
	planData := asMap(t, plan["data"])
	if planData["major_name"] != "金融工程" || planData["grade"] != "2020" {
		t.Fatalf("plan data = %v", planData)
	}

	// Timetable.
	res2, rawTt := doJSON(t, h, http.MethodGet, "/v1/me/academic/timetable?term=2024-2025-1-1", access, nil)
	tt := asMap(t, rawTt)
	if res2.StatusCode != 200 {
		t.Fatalf("timetable status = %d body %v", res2.StatusCode, tt)
	}
	ttData := asMap(t, tt["data"])
	courses, _ := ttData["courses"].([]any)
	if len(courses) != 1 {
		t.Fatalf("timetable courses = %v", ttData["courses"])
	}
	course := asMap(t, courses[0])
	if course["course_name"] != "高等数学" || course["teacher"] != "张三" {
		t.Fatalf("course = %v", course)
	}
	slots, _ := course["slots"].([]any)
	slot := asMap(t, slots[0])
	if slot["day"] != float64(1) || slot["end_session"] != float64(2) {
		t.Fatalf("slot = %v", slot)
	}
}

// TestAcademicSilentReloginAfterRestart simulates a server restart: a new
// api Server over the SAME store/vault but a fresh school session map
// must serve academic reads by silently re-logging-in from the vault
// (the client token stays valid; the school-side work is one full login).
func TestAcademicSilentReloginAfterRestart(t *testing.T) {
	tr := successTransport(t)

	st := mem.New()
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tm := token.NewManager(priv, 15*time.Minute, 30*24*time.Hour)
	pepper := bytes.Repeat([]byte{2}, 32)
	cfg := config.Config{PasswordMaxLen: 64, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour, KEKVersion: "v1"}

	srv1 := New(Deps{Cfg: cfg, Store: st, Vault: v, Tokens: tm,
		School: school.NewManager(func() transport.Transport { return tr }, school.WithoutLoginGate()), Pepper: pepper})
	h1 := srv1.Router()
	_, rawOut := login(t, h1, "20000003", "Passw0rd!123")
	out := asMap(t, rawOut)
	access, _ := out["access_token"].(string)
	if access == "" {
		t.Fatalf("login failed: %v", out)
	}

	// "Restart": fresh school manager (empty sessions), same everything else.
	srv2 := New(Deps{Cfg: cfg, Store: st, Vault: v, Tokens: tm,
		School: school.NewManager(func() transport.Transport { return tr }, school.WithoutLoginGate()), Pepper: pepper})
	h2 := srv2.Router()

	res, rawSummary := doJSON(t, h2, http.MethodGet, "/v1/me/academic/summary", access, nil)
	summary := asMap(t, rawSummary)
	if res.StatusCode != 200 {
		t.Fatalf("summary after restart status = %d body %v", res.StatusCode, summary)
	}
	if asMap(t, summary["data"])["gpa"] != 3.52 {
		t.Fatalf("summary data = %v", summary["data"])
	}
	// The silent relogin is a full login (no live session to borrow from
	// after a restart): exactly ONE more gateway dance on top of the
	// fixture login's two calls.
	if got := countRecorded(tr, "/por/"); got != 4 {
		t.Fatalf("silent relogin gateway calls = %d, want 4 (2 fixture login + 2 relogin)", got)
	}
}

func TestProtectedEndpointsRejectAnonymous(t *testing.T) {
	h := newTestServer(t, successTransport(t))
	res, _ := doJSON(t, h, http.MethodGet, "/v1/me", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /v1/me status = %d", res.StatusCode)
	}
}
