package teachereval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
)

// The school is closed in production, so the engine is verified against this
// fake. Every timing assertion runs on the manual clock.

var t0 = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)

func course(i int) jwc.TeacherEvaluationCourse {
	return jwc.TeacherEvaluationCourse{
		Name:                    fmt.Sprintf("课程%d", i),
		Teacher:                 "教师",
		EvaluatedPeopleNumber:   fmt.Sprintf("p%d", i),
		CoureSequenceNumber:     fmt.Sprintf("s%d", i),
		EvaluationContentNumber: fmt.Sprintf("e%d", i),
		QuestionnaireCode:       "q",
	}
}

func keyOf(i int) string {
	c := course(i)
	return CourseKey(&c, 0)
}

// userState is one user's fake school account.
type userState struct {
	mu          sync.Mutex
	clock       *ManualClock
	index       []*jwc.TeacherEvaluationIndexStatus
	indexCalls  int
	courses     []jwc.TeacherEvaluationCourse
	prepareFail map[string]int
	submitFail  map[string]int
	recorded    map[string]bool // failing submit that the server still recorded
	verifyErr   bool
	captcha     bool
	prepareAt   map[string][]time.Time
	submitAt    map[string][]time.Time
	modes       []jwc.TeacherEvalScoreMode
}

func (u *userState) markEvaluated(key string) {
	for i := range u.courses {
		if CourseKey(&u.courses[i], i) == key {
			u.courses[i].IsEvaluated = true
		}
	}
}

func (u *userState) preparesOf(key string) []time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]time.Time(nil), u.prepareAt[key]...)
}

func (u *userState) submitsOf(key string) []time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]time.Time(nil), u.submitAt[key]...)
}

func (u *userState) modesSeen() []jwc.TeacherEvalScoreMode {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]jwc.TeacherEvalScoreMode(nil), u.modes...)
}

type fakeSchool struct {
	mu    sync.Mutex
	clock *ManualClock
	users map[string]*userState
}

// user returns the account, creating it with the given courses on first use.
func (s *fakeSchool) user(id string, courses ...jwc.TeacherEvaluationCourse) *userState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[id]; ok {
		return u
	}
	if len(courses) == 0 {
		courses = []jwc.TeacherEvaluationCourse{course(1)}
	}
	u := &userState{
		clock:       s.clock,
		index:       []*jwc.TeacherEvaluationIndexStatus{{TokenValue: "TOK"}},
		courses:     courses,
		prepareFail: map[string]int{},
		submitFail:  map[string]int{},
		recorded:    map[string]bool{},
		prepareAt:   map[string][]time.Time{},
		submitAt:    map[string][]time.Time{},
	}
	s.users[id] = u
	return u
}

type fakeFactory struct{ s *fakeSchool }

func (f fakeFactory) For(_ context.Context, userID, _ string) (Remote, error) {
	return fakeRemote{u: f.s.user(userID)}, nil
}

type fakeRemote struct{ u *userState }

func (r fakeRemote) Index(context.Context) (*jwc.TeacherEvaluationIndexStatus, error) {
	r.u.mu.Lock()
	defer r.u.mu.Unlock()
	i := r.u.indexCalls
	if i >= len(r.u.index) {
		i = len(r.u.index) - 1
	}
	r.u.indexCalls++
	v := *r.u.index[i]
	return &v, nil
}

func (r fakeRemote) Courses(context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	r.u.mu.Lock()
	defer r.u.mu.Unlock()
	return &jwc.TeacherEvaluationCourseList{
		TokenValue: "TOK",
		Courses:    append([]jwc.TeacherEvaluationCourse(nil), r.u.courses...),
	}, nil
}

func (r fakeRemote) Prepare(_ context.Context, c *jwc.TeacherEvaluationCourse, _ int64, _ string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error) {
	u := r.u
	u.mu.Lock()
	defer u.mu.Unlock()
	key := CourseKey(c, 0)
	u.modes = append(u.modes, mode)
	u.prepareAt[key] = append(u.prepareAt[key], u.clock.Now())
	if u.captcha {
		return nil, ErrCaptchaRequired
	}
	if u.prepareFail[key] > 0 {
		u.prepareFail[key]--
		return nil, errs.NewNetwork("prepare blip")
	}
	return &jwc.TeacherEvaluationPreparedForm{Course: *c, QuestionnaireTitle: "问卷", FormData: [][2]string{{"k", key}}}, nil
}

func (r fakeRemote) Submit(_ context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	u := r.u
	key := CourseKey(&p.Course, 0)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.submitAt[key] = append(u.submitAt[key], u.clock.Now())
	if u.submitFail[key] > 0 {
		u.submitFail[key]--
		if u.recorded[key] {
			u.markEvaluated(key)
		}
		return nil, errs.NewNetwork("submit reset")
	}
	u.markEvaluated(key)
	return &jwc.TeacherEvaluationSubmitResult{Success: true, Message: "ok"}, nil
}

func (r fakeRemote) Verify(_ context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	u := r.u
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.verifyErr {
		return false, errs.NewNetwork("verify down")
	}
	for i := range u.courses {
		if Matches(&u.courses[i], c) {
			return u.courses[i].IsEvaluated, nil
		}
	}
	return false, nil
}

// harness wires a pool to the fake school and the in-memory store.
type harness struct {
	t      *testing.T
	ctx    context.Context
	clock  *ManualClock
	school *fakeSchool
	st     *mem.Store
	pool   *Pool
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	if !cfg.Enabled && cfg.Capacity == 0 && cfg.MaxQueue == 0 {
		cfg.Enabled = true
	}
	clock := NewManualClock(t0)
	school := &fakeSchool{clock: clock, users: map[string]*userState{}}
	st := mem.New()
	p := New(cfg, st, fakeFactory{school}, clock)
	t.Cleanup(p.Shutdown)
	return &harness{t: t, ctx: context.Background(), clock: clock, school: school, st: st, pool: p}
}

func (h *harness) create(user, mode string) *store.TeacherEvalTask {
	h.t.Helper()
	task, _, err := h.pool.Create(h.ctx, user, "dev", mode)
	if err != nil {
		h.t.Fatalf("create %s: %v", user, err)
	}
	return task
}

func (h *harness) status(id string) store.TeacherEvalTask {
	h.t.Helper()
	task, err := h.st.GetTeacherEvalTask(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return *task
}

func (h *harness) rows(id string) []store.TeacherEvalTaskCourse {
	h.t.Helper()
	rows, err := h.st.ListTeacherEvalCourses(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// settle waits until the set of pending timers stops changing. Runner
// goroutines do store writes between timers, and advancing the simulated
// clock during that gap would make a wait start late.
func (h *harness) settle() {
	prev, stable := -1, 0
	for stable < 5 {
		time.Sleep(time.Millisecond)
		n := h.clock.Waiters()
		if n == prev {
			stable++
		} else {
			prev, stable = n, 0
		}
	}
}

// drive advances the simulated clock one second at a time until done reports
// true. It only advances when a timer is pending, so goroutines are never
// starved.
func (h *harness) drive(done func() bool) {
	h.t.Helper()
	for step := 0; step < 6000; step++ {
		if done() {
			return
		}
		if h.clock.Waiters() == 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		h.settle()
		h.clock.Advance(time.Second)
	}
	h.t.Fatal("task did not finish within the simulated window")
}

func (h *harness) finished(id string) func() bool {
	return func() bool { return isTerminalTask(h.status(id).Status) }
}

func (h *harness) courseStatus(task, key string) string {
	for _, row := range h.rows(task) {
		if row.CourseKey == key {
			return row.Status
		}
	}
	return ""
}

func TestTaskFollowsClientPacing(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1", course(1), course(2), course(3))
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	if got := h.status(task.ID); got.Status != "completed" {
		t.Fatalf("status = %s (%s)", got.Status, got.ErrorMessage)
	}
	for i := 1; i <= 3; i++ {
		if st := h.courseStatus(task.ID, keyOf(i)); st != stSuccess {
			t.Fatalf("course %d status = %s", i, st)
		}
		prep := u.preparesOf(keyOf(i))
		sub := u.submitsOf(keyOf(i))
		if len(prep) != 1 || len(sub) != 1 {
			t.Fatalf("course %d: prepares=%d submits=%d", i, len(prep), len(sub))
		}
		if want := t0.Add(time.Duration(i-1) * LaunchInterval); !prep[0].Equal(want) {
			t.Fatalf("course %d prepared at %v, want %v (6s launch stagger)", i, prep[0], want)
		}
		if gap := sub[0].Sub(prep[0]); gap != SubmitWait {
			t.Fatalf("course %d submitted %v after prepare, want %v", i, gap, SubmitWait)
		}
	}
}

func TestEvaluatedCoursesAreSkipped(t *testing.T) {
	h := newHarness(t, Config{})
	done := course(1)
	done.IsEvaluated = true
	u := h.school.user("u1", done, course(2))
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	if n := len(h.rows(task.ID)); n != 1 {
		t.Fatalf("course rows = %d, want only the unevaluated course", n)
	}
	if len(u.preparesOf(keyOf(1))) != 0 {
		t.Fatal("an already evaluated course must not be prepared")
	}
}

func TestAmbiguousSubmitIsConfirmedNotResubmitted(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	u.submitFail[keyOf(1)] = 1
	u.recorded[keyOf(1)] = true // the server recorded it despite the error
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	if st := h.courseStatus(task.ID, keyOf(1)); st != stSuccess {
		t.Fatalf("course status = %s, want success confirmed by verify", st)
	}
	if n := len(u.submitsOf(keyOf(1))); n != 1 {
		t.Fatalf("submits = %d, a confirmed submission must not be repeated", n)
	}
}

func TestUnconfirmedSubmitRetriesOnceWithFreshForm(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	u.submitFail[keyOf(1)] = 1 // not recorded: the server still shows unevaluated
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	if st := h.courseStatus(task.ID, keyOf(1)); st != stSuccess {
		t.Fatalf("course status = %s, want success after the retry", st)
	}
	if n := len(u.submitsOf(keyOf(1))); n != 2 {
		t.Fatalf("submits = %d, want exactly one retry", n)
	}
	if n := len(u.preparesOf(keyOf(1))); n != 2 {
		t.Fatalf("prepares = %d, the retry must build a fresh form", n)
	}
}

func TestVerifyErrorNeverResubmits(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	u.submitFail[keyOf(1)] = 1
	u.verifyErr = true
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	rows := h.rows(task.ID)
	if rows[0].Status != stFailed || rows[0].ErrorKind != "verify_unknown" {
		t.Fatalf("row = %s/%s, want failed/verify_unknown", rows[0].Status, rows[0].ErrorKind)
	}
	if n := len(u.submitsOf(keyOf(1))); n != 1 {
		t.Fatalf("submits = %d, an unconfirmed submission must not be retried", n)
	}
}

func TestCaptchaEndsTheTask(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1", course(1), course(2))
	u.captcha = true
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	got := h.status(task.ID)
	if got.Status != "failed" || got.ErrorKind != "captcha_required" {
		t.Fatalf("task = %s/%s, want failed/captcha_required", got.Status, got.ErrorKind)
	}
	if n := len(u.submitsOf(keyOf(1))) + len(u.submitsOf(keyOf(2))); n != 0 {
		t.Fatalf("submits = %d, nothing may be submitted once a captcha is demanded", n)
	}
	if h.pool.Stats().CaptchaAborts != 1 {
		t.Fatal("captcha abort not counted")
	}
}

func TestFirstVisitProtectionWaitsThenProceeds(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	protected := &jwc.TeacherEvaluationIndexStatus{
		TokenValue: "TOK",
		Protection: jwc.TeacherEvaluationIndexProtection{
			InfoOff: "1", InfoTimeSeconds: 30, HasAhcjTimer: true,
			IsFirstVisit: "unVisit", IsFirstVisitResolved: true,
		},
	}
	// Create reads the first entry; the runner then sees the protection gate.
	u.index = []*jwc.TeacherEvaluationIndexStatus{{TokenValue: "TOK"}, protected, {TokenValue: "TOK"}}
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	if got := h.status(task.ID); got.Status != "completed" {
		t.Fatalf("status = %s (%s)", got.Status, got.ErrorMessage)
	}
	prep := u.preparesOf(keyOf(1))
	if len(prep) != 1 || !prep[0].Equal(t0.Add(35*time.Second)) {
		t.Fatalf("first prepare at %v, want after the 35s protection wait", prep)
	}
}

func TestProtectionUnresolvedAfterTwoWaitsFails(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	protected := &jwc.TeacherEvaluationIndexStatus{
		TokenValue: "TOK",
		Protection: jwc.TeacherEvaluationIndexProtection{
			InfoOff: "1", InfoTimeSeconds: 30, HasAhcjTimer: true,
			IsFirstVisit: "unVisit", IsFirstVisitResolved: true,
		},
	}
	u.index = []*jwc.TeacherEvaluationIndexStatus{{TokenValue: "TOK"}, protected}
	task := h.create("u1", ScoreRandom)
	h.drive(h.finished(task.ID))

	got := h.status(task.ID)
	if got.Status != "failed" || got.ErrorKind != "protection_unresolved" {
		t.Fatalf("task = %s/%s, want failed/protection_unresolved", got.Status, got.ErrorKind)
	}
	if n := len(u.preparesOf(keyOf(1))); n != 0 {
		t.Fatalf("prepares = %d, nothing may run behind an unresolved gate", n)
	}
}

func TestClosedEvaluationRefusesCreate(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	u.index = []*jwc.TeacherEvaluationIndexStatus{{IsClosed: true, ClosedMessage: "评估开关已关闭"}}
	_, _, err := h.pool.Create(h.ctx, "u1", "dev", ScoreRandom)
	var closed *ClosedError
	if !errors.As(err, &closed) || closed.Message != "评估开关已关闭" {
		t.Fatalf("create err = %v, want ClosedError", err)
	}
	if _, err := h.st.ActiveTeacherEvalTask(h.ctx, "u1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused create must store nothing, active err = %v", err)
	}
}

func TestFiftyActiveUsersThenQueue(t *testing.T) {
	h := newHarness(t, Config{})
	const users = 51
	ids := make([]string, 0, users)
	for i := 0; i < users; i++ {
		id := fmt.Sprintf("u%02d", i)
		h.school.user(id)
		ids = append(ids, h.create(id, ScoreRandom).ID)
	}
	stats := h.pool.Stats()
	if stats.Running != 50 || stats.Queued != 1 {
		t.Fatalf("running=%d queued=%d, want 50 and 1", stats.Running, stats.Queued)
	}
	h.drive(func() bool {
		for _, id := range ids {
			if !isTerminalTask(h.status(id).Status) {
				return false
			}
		}
		return true
	})
	if got := h.pool.Stats().Completed; got != users {
		t.Fatalf("completed = %d, want %d", got, users)
	}
}

func TestQueueFullIsRejected(t *testing.T) {
	h := newHarness(t, Config{Enabled: true, Capacity: 1, MaxQueue: 1})
	for _, id := range []string{"a", "b", "c"} {
		h.school.user(id)
	}
	h.create("a", ScoreRandom) // running
	h.create("b", ScoreRandom) // queued
	_, _, err := h.pool.Create(h.ctx, "c", "dev", ScoreRandom)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third create err = %v, want ErrQueueFull", err)
	}
}

func TestCancelWhileWaitingNeverSubmits(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	task := h.create("u1", ScoreRandom)
	h.drive(func() bool { return h.courseStatus(task.ID, keyOf(1)) == stWaiting })

	if _, err := h.pool.Cancel(h.ctx, "u1", task.ID); err != nil {
		t.Fatal(err)
	}
	h.drive(h.finished(task.ID))

	if got := h.status(task.ID); got.Status != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got.Status)
	}
	if n := len(u.submitsOf(keyOf(1))); n != 0 {
		t.Fatalf("submits = %d, a course cancelled while waiting must not submit", n)
	}
}

func TestCancelQueuedTaskNeverStarts(t *testing.T) {
	h := newHarness(t, Config{Enabled: true, Capacity: 1})
	for _, id := range []string{"a", "b"} {
		h.school.user(id)
	}
	first := h.create("a", ScoreRandom)
	queued := h.create("b", ScoreRandom)
	if _, err := h.pool.Cancel(h.ctx, "b", queued.ID); err != nil {
		t.Fatal(err)
	}
	h.drive(h.finished(first.ID))

	if got := h.status(queued.ID); got.Status != "cancelled" {
		t.Fatalf("queued task status = %s, want cancelled", got.Status)
	}
	if n := len(h.school.user("b").preparesOf(keyOf(1))); n != 0 {
		t.Fatalf("cancelled queued task prepared %d forms", n)
	}
}

func TestRestartInterruptsRunningAndRequeuesQueued(t *testing.T) {
	h := newHarness(t, Config{})
	h.school.user("u6")
	running := store.TeacherEvalTask{ID: "running-1", UserID: "u5", Status: "running", ScoreMode: ScoreRandom, CreatedAt: t0}
	inFlight := store.TeacherEvalTaskCourse{
		TaskID: running.ID, CourseKey: keyOf(9), Name: "课程9", Position: 0,
		Status: stSubmitting, SubmitAttempts: 1, SubmittedAt: t0, UpdatedAt: t0,
	}
	if err := h.st.CreateTeacherEvalTask(h.ctx, running, []store.TeacherEvalTaskCourse{inFlight}); err != nil {
		t.Fatal(err)
	}
	queued := store.TeacherEvalTask{ID: "queued-1", UserID: "u6", Status: "queued", ScoreMode: ScoreRandom, CreatedAt: t0.Add(time.Second)}
	if err := h.st.CreateTeacherEvalTask(h.ctx, queued, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.drive(h.finished(queued.ID))

	if got := h.status(running.ID); got.Status != "interrupted" {
		t.Fatalf("running task after restart = %s, want interrupted", got.Status)
	}
	if rows := h.rows(running.ID); rows[0].Status != stCancelled {
		t.Fatalf("in-flight course = %s, must not be resubmitted", rows[0].Status)
	}
	if n := len(h.school.user("u5").submitsOf(keyOf(9))); n != 0 {
		t.Fatalf("restart resubmitted an in-flight course %d times", n)
	}
	if got := h.status(queued.ID); got.Status != "completed" {
		t.Fatalf("requeued task = %s, want completed", got.Status)
	}
}

func TestDuplicateCreateReturnsTheActiveTask(t *testing.T) {
	h := newHarness(t, Config{})
	h.school.user("u1")
	first := h.create("u1", ScoreRandom)
	again, _, err := h.pool.Create(h.ctx, "u1", "dev", ScoreRandom)
	if !errors.Is(err, ErrTaskActive) || again == nil || again.ID != first.ID {
		t.Fatalf("second create = (%v, %v), want ErrTaskActive pointing at %s", again, err, first.ID)
	}
}

func TestPauseStopsRunningTasksAndBlocksCreate(t *testing.T) {
	h := newHarness(t, Config{})
	h.school.user("u1")
	task := h.create("u1", ScoreRandom)
	h.drive(func() bool { return h.courseStatus(task.ID, keyOf(1)) == stWaiting })

	h.pool.Pause(true)
	h.drive(h.finished(task.ID))
	if got := h.status(task.ID); got.Status != "failed" || got.ErrorKind != "paused" {
		t.Fatalf("task = %s/%s, want failed/paused", got.Status, got.ErrorKind)
	}
	if _, _, err := h.pool.Create(h.ctx, "u2", "dev", ScoreRandom); !errors.Is(err, ErrPaused) {
		t.Fatalf("create while paused = %v, want ErrPaused", err)
	}
}

func TestDisabledGateRefusesCreate(t *testing.T) {
	off := newHarness(t, Config{Enabled: false, Capacity: 1})
	if _, _, err := off.pool.Create(off.ctx, "u1", "dev", ScoreRandom); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled create = %v, want ErrDisabled", err)
	}
}

func TestScoreModeReachesTheCoreAndIsValidated(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.school.user("u1")
	task := h.create("u1", ScoreMax)
	h.drive(h.finished(task.ID))

	modes := u.modesSeen()
	if len(modes) != 1 || modes[0] != jwc.TeacherEvalScoreMax {
		t.Fatalf("prepare modes = %v, want [max]", modes)
	}
	if _, _, err := h.pool.Create(h.ctx, "u1", "dev", "best-effort"); !errors.Is(err, ErrInvalidScoreMode) {
		t.Fatalf("invalid mode = %v, want ErrInvalidScoreMode", err)
	}
}
