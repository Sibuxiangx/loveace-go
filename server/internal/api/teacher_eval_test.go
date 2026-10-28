package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/teachereval"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

const teachIndexOpen = `<html><body><input id="tokenValue" value="TOK"></body></html>`
const teachIndexClosed = `<html><body><div class="alert">评估开关已关闭</div></body></html>`

// teachIndexProtected is a first-visit index: the reading gate holds the task
// on the clock, so it stays active for the duplicate-create assertions.
const teachIndexProtected = `<html><head><script>var isFinished = "unfinished"; var isFirstVisit = "unVisit"; var tsxxAhcj = 1;</script></head><body><input id="tokenValue" value="TOK"><input id="schoolId" value="S1"><input id="infoOff" value="1"><input id="infoTime" value="30"></body></html>`

// newTeachTestServer mirrors newGroupTestServer and injects a teach pool that
// waits on a manual clock, so no test ever sleeps through a 140s stagger.
func newTeachTestServer(t *testing.T, tr *transport.MockTransport, enabled bool, adminToken string) http.Handler {
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
		AdminToken:      adminToken,
	}
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	reg := health.New()
	st := mem.New()
	sm := school.NewManager(func() transport.Transport { return tr },
		school.WithSinkFactory(reg.SinkFor), school.WithoutLoginGate())
	clock := teachereval.NewManualClock(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC))
	pool := teachereval.New(teachereval.Config{Enabled: enabled}, st, teachereval.SchoolFactory{School: sm}, clock)
	t.Cleanup(pool.Shutdown)
	srv := New(Deps{
		Cfg:    cfg,
		Store:  st,
		Vault:  v,
		Tokens: token.NewManager(priv, cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		School: sm,
		Health: reg,
		Pepper: bytes.Repeat([]byte{2}, 32),
		Teach:  pool,
	})
	return srv.Router()
}

// teachDo sends one request, optionally with an Idempotency-Key.
func teachDo(t *testing.T, h http.Handler, method, path, token, key string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestTeacherEvalStatusReportsClosedWithoutFeatureFlag(t *testing.T) {
	tr := successTransport(t)
	tr.OnRequest(transport.Get, "/student/teachingEvaluation/evaluation/index", transport.NewResponse(200, []byte(teachIndexClosed)))
	h := newTeachTestServer(t, tr, false, "")
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")

	code, out := teachDo(t, h, http.MethodGet, "/v1/me/teacher-eval/status", access, "", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d %v", code, out)
	}
	if out["open"] != false || out["closed_message"] == "" || out["feature_enabled"] != false {
		t.Fatalf("closed school must read open=false with its message: %v", out)
	}
}

func TestTeacherEvalStatusReportsOpen(t *testing.T) {
	tr := successTransport(t)
	tr.OnRequest(transport.Get, "/student/teachingEvaluation/evaluation/index", transport.NewResponse(200, []byte(teachIndexOpen)))
	h := newTeachTestServer(t, tr, true, "")
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")

	code, out := teachDo(t, h, http.MethodGet, "/v1/me/teacher-eval/status", access, "", nil)
	if code != http.StatusOK || out["open"] != true || out["feature_enabled"] != true {
		t.Fatalf("open school status = %d %v", code, out)
	}
}

func TestTeacherEvalStatusRequiresLogin(t *testing.T) {
	h := newTeachTestServer(t, successTransport(t), true, "")
	if code, _ := teachDo(t, h, http.MethodGet, "/v1/me/teacher-eval/status", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", code)
	}
}

func TestTeacherEvalCreateGatesAndIdempotency(t *testing.T) {
	tr := successTransport(t)
	tr.OnRequest(transport.Get, "/student/teachingEvaluation/evaluation/index", transport.NewResponse(200, []byte(teachIndexProtected)))
	h := newTeachTestServer(t, tr, true, "")
	access := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	const path = "/v1/me/teacher-eval/tasks"

	if code, _ := teachDo(t, h, http.MethodPost, path, access, "", map[string]any{}); code != http.StatusPreconditionRequired {
		t.Fatalf("create without key = %d, want 428", code)
	}
	if code, out := teachDo(t, h, http.MethodPost, path, access, "k-bad", map[string]any{"score_mode": "weird"}); code != http.StatusUnprocessableEntity || out["code"] != "invalid_argument" {
		t.Fatalf("invalid score mode = %d %v, want 422", code, out)
	}

	code, first := teachDo(t, h, http.MethodPost, path, access, "k-0", map[string]any{"score_mode": "max"})
	if code != http.StatusAccepted {
		t.Fatalf("create = %d %v", code, first)
	}
	taskID, _ := first["id"].(string)
	if taskID == "" {
		t.Fatalf("create returned no id: %v", first)
	}

	if code, replay := teachDo(t, h, http.MethodPost, path, access, "k-0", map[string]any{"score_mode": "max"}); code != http.StatusAccepted || replay["id"] != taskID {
		t.Fatalf("same key must replay the same task: %d %v", code, replay)
	}
	if code, out := teachDo(t, h, http.MethodPost, path, access, "k-0", map[string]any{"score_mode": "random"}); code != http.StatusUnprocessableEntity || out["code"] != "idempotency_key_mismatch" {
		t.Fatalf("same key with another body = %d %v", code, out)
	}
	if code, out := teachDo(t, h, http.MethodPost, path, access, "k-2", map[string]any{}); code != http.StatusConflict || out["task_id"] != taskID {
		t.Fatalf("second active task = %d %v, want 409 with the active id", code, out)
	}
	if code, out := teachDo(t, h, http.MethodGet, path+"/active", access, "", nil); code != http.StatusOK || out["id"] != taskID {
		t.Fatalf("active = %d %v", code, out)
	}
	if code, out := teachDo(t, h, http.MethodGet, path+"/"+taskID, access, "", nil); code != http.StatusOK {
		t.Fatalf("get task = %d %v", code, out)
	}
	if code, _ := teachDo(t, h, http.MethodPost, path+"/"+taskID+"/cancel", access, "", nil); code != http.StatusAccepted {
		t.Fatalf("cancel = %d", code)
	}
	if code, _ := teachDo(t, h, http.MethodGet, path+"/no-such-task", access, "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown task = %d, want 404", code)
	}
}

func TestTeacherEvalAdminPoolAndPause(t *testing.T) {
	h := newTeachTestServer(t, successTransport(t), true, "adm-secret")
	if code, _ := teachDo(t, h, http.MethodGet, "/v1/admin/teacher-eval/pool", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("admin pool without token = %d, want 401", code)
	}
	code, out := teachDo(t, h, http.MethodGet, "/v1/admin/teacher-eval/pool", "adm-secret", "", nil)
	if code != http.StatusOK || out["Capacity"] != float64(teachereval.DefaultCapacity) {
		t.Fatalf("admin pool = %d %v", code, out)
	}
	if code, out := teachDo(t, h, http.MethodPost, "/v1/admin/teacher-eval/pause", "adm-secret", "", map[string]any{"paused": true}); code != http.StatusOK || out["Paused"] != true {
		t.Fatalf("pause = %d %v", code, out)
	}
}
