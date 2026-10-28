package teachereval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// Score modes a user can choose at submission time.
const (
	// ScoreRandom: 80% a full-weight option, otherwise the second weight group.
	ScoreRandom = "random"
	// ScoreMax: every radio answer is a top-weight option.
	ScoreMax = "max"
)

// Defaults for Config zero values.
const (
	DefaultCapacity  = 50
	DefaultMaxQueue  = 200
	DefaultStatusTTL = 60 * time.Second
	// staleStatusWindow bounds how old a cached index may be when the school
	// cannot be reached and the status endpoint falls back to it.
	staleStatusWindow = 30 * time.Minute
	logFetchLimit     = 50
)

// Errors the API maps onto problem responses.
var (
	ErrDisabled         = errors.New("teachereval: feature disabled")
	ErrPaused           = errors.New("teachereval: paused by operator")
	ErrInvalidScoreMode = errors.New("teachereval: invalid score mode")
	ErrQueueFull        = errors.New("teachereval: queue full")
	ErrTaskActive       = errors.New("teachereval: task already active")
)

// ClosedError means the school has the evaluation switched off.
type ClosedError struct{ Message string }

func (e *ClosedError) Error() string { return "teachereval: evaluation closed: " + e.Message }

// Config controls the pool. Zero values take the defaults above.
type Config struct {
	Enabled   bool
	Capacity  int
	MaxQueue  int
	StatusTTL time.Duration
}

// Pool owns background tasks: admission, the queue, the running set, and
// restart recovery. All school work happens in per-task runners.
type Pool struct {
	cfg    Config
	st     store.Store
	remote RemoteFactory
	clock  Clock

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	paused  bool
	queue   []string
	running map[string]*runner

	counters counters

	statusMu sync.Mutex
	statuses map[string]cachedIndex
}

type counters struct {
	started, completed, failed, cancelled, interrupted atomic.Int64
	coursesSucceeded, coursesFailed, captchaAborts     atomic.Int64
}

type cachedIndex struct {
	at    time.Time
	open  bool
	msg   string
	waitS int
}

// New builds a pool. Call Start once to recover tasks from the previous run.
func New(cfg Config, st store.Store, remote RemoteFactory, clock Clock) *Pool {
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = DefaultMaxQueue
	}
	if cfg.StatusTTL <= 0 {
		cfg.StatusTTL = DefaultStatusTTL
	}
	if clock == nil {
		clock = RealClock{}
	}
	if remote == nil {
		remote = unavailableFactory{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		cfg:      cfg,
		st:       st,
		remote:   remote,
		clock:    clock,
		ctx:      ctx,
		cancel:   cancel,
		running:  map[string]*runner{},
		statuses: map[string]cachedIndex{},
	}
}

// Start recovers tasks left by the previous process. Running tasks are marked
// interrupted and never resumed: their submissions may have reached the
// server, so a fresh task must be started deliberately. Queued tasks never
// started, so they re-enter the queue.
func (p *Pool) Start(ctx context.Context) error {
	recoverable, err := p.st.ListRecoverableTeacherEvalTasks(ctx)
	if err != nil {
		return fmt.Errorf("teachereval: recover: %w", err)
	}
	sort.Slice(recoverable, func(i, j int) bool {
		return recoverable[i].CreatedAt.Before(recoverable[j].CreatedAt)
	})
	p.mu.Lock()
	for _, t := range recoverable {
		if t.Status == "queued" {
			p.queue = append(p.queue, t.ID)
		}
	}
	p.mu.Unlock()
	for _, t := range recoverable {
		if t.Status == "running" {
			p.interruptStored(ctx, t)
		}
	}
	p.mu.Lock()
	p.dispatchLocked()
	p.mu.Unlock()
	return nil
}

// Shutdown stops every runner and waits for them. Runners record their own
// interruption on the way out.
func (p *Pool) Shutdown() {
	p.cancel()
	p.wg.Wait()
}

func (p *Pool) interruptStored(ctx context.Context, t store.TeacherEvalTask) {
	const reason = "服务重启，任务已中断"
	now := p.clock.Now()
	if rows, err := p.st.ListTeacherEvalCourses(ctx, t.ID); err == nil {
		for _, row := range rows {
			if isTerminalCourse(row.Status) {
				continue
			}
			row.Status = stCancelled
			row.Message = reason
			row.UpdatedAt = now
			_ = p.st.UpdateTeacherEvalCourse(ctx, row)
		}
	}
	t.Status = "interrupted"
	t.ErrorKind = "interrupted"
	t.ErrorMessage = reason
	t.FinishedAt = now
	if err := p.st.UpdateTeacherEvalTask(ctx, t); err != nil {
		log.Printf("[teachereval] mark interrupted %s: %v", t.ID, err)
	}
	p.counters.interrupted.Add(1)
	_ = p.st.AppendTeacherEvalLog(ctx, t.ID, now, reason)
}

// QueuePosition is the task's 1-based place in the queue, or 0 when it is not
// waiting (running, finished, or unknown). Read paths use it so that every
// task view reports the same position.
func (p *Pool) QueuePosition(taskID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.positionLocked(taskID)
}

// Precheck runs the admission gates that do not touch the school: the feature
// flag, operator pause, and score mode. The API runs it before it reserves an
// idempotency key, so a refused request leaves no stored failure behind.
func (p *Pool) Precheck(ctx context.Context, userID, scoreMode string) error {
	if !p.cfg.Enabled {
		return ErrDisabled
	}
	if _, err := normalizeMode(scoreMode); err != nil {
		return err
	}
	if p.isPaused() {
		return ErrPaused
	}
	return nil
}

// Create admits a task for the user. On ErrTaskActive the returned task is the
// user's existing active task.
func (p *Pool) Create(ctx context.Context, userID, deviceID, scoreMode string) (*store.TeacherEvalTask, int, error) {
	if err := p.Precheck(ctx, userID, scoreMode); err != nil {
		return nil, 0, err
	}
	mode, _ := normalizeMode(scoreMode)
	if active, err := p.st.ActiveTeacherEvalTask(ctx, userID); err == nil {
		return active, 0, ErrTaskActive
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, 0, err
	}
	idx, err := p.indexFor(ctx, userID, deviceID)
	if err != nil {
		return nil, 0, err
	}
	if idx.IsClosed {
		return nil, 0, &ClosedError{Message: idx.ClosedMessage}
	}
	p.mu.Lock()
	full := len(p.running) >= p.cfg.Capacity && len(p.queue) >= p.cfg.MaxQueue
	p.mu.Unlock()
	if full {
		return nil, 0, ErrQueueFull
	}
	task := store.TeacherEvalTask{
		ID:        newID(),
		UserID:    userID,
		DeviceID:  deviceID,
		Status:    "queued",
		ScoreMode: mode,
		CreatedAt: p.clock.Now(),
	}
	if err := p.st.CreateTeacherEvalTask(ctx, task, nil); err != nil {
		if errors.Is(err, store.ErrTeacherEvalTaskActive) {
			if active, aerr := p.st.ActiveTeacherEvalTask(ctx, userID); aerr == nil {
				return active, 0, ErrTaskActive
			}
			return nil, 0, ErrTaskActive
		}
		return nil, 0, err
	}
	p.mu.Lock()
	p.queue = append(p.queue, task.ID)
	p.dispatchLocked()
	position := p.positionLocked(task.ID)
	p.mu.Unlock()
	return &task, position, nil
}

// dispatchLocked starts queued tasks while slots are free. Caller holds mu.
func (p *Pool) dispatchLocked() {
	for !p.paused && len(p.running) < p.cfg.Capacity && len(p.queue) > 0 {
		id := p.queue[0]
		p.queue = p.queue[1:]
		r := newRunner(p, store.TeacherEvalTask{ID: id})
		p.running[id] = r
		p.counters.started.Add(1)
		p.wg.Add(1)
		go p.execute(r)
	}
}

// positionLocked is the 1-based queue position, or 0 when not queued. Caller holds mu.
func (p *Pool) positionLocked(taskID string) int {
	for i, id := range p.queue {
		if id == taskID {
			return i + 1
		}
	}
	return 0
}

// execute runs one task to a terminal state, then frees its slot.
func (p *Pool) execute(r *runner) {
	defer p.wg.Done()
	defer p.release(r.task.ID)
	t, err := p.st.GetTeacherEvalTask(p.storeContext(), r.task.ID)
	if err != nil {
		log.Printf("[teachereval] load %s: %v", r.task.ID, err)
		return
	}
	r.task = *t
	r.task.Status = "running"
	r.task.StartedAt = p.clock.Now()
	p.persist(func(ctx context.Context) error { return p.st.UpdateTeacherEvalTask(ctx, r.task) })
	p.log(r.task.ID, "任务已开始")
	err = r.execute(p.ctx)
	p.finish(r, err)
}

// finish records the terminal state of a task.
func (p *Pool) finish(r *runner, err error) {
	t := r.task
	t.FinishedAt = p.clock.Now()
	switch {
	case err == nil:
		t.Status = "completed"
		p.counters.completed.Add(1)
		ok, failed := r.summary()
		p.log(t.ID, fmt.Sprintf("评教结束：成功 %d 门，失败 %d 门", ok, failed))
	case errors.Is(err, errUserCancelled):
		t.Status = "cancelled"
		t.CancelRequested = true
		t.ErrorMessage = "已取消；已提交的评价无法撤回"
		p.counters.cancelled.Add(1)
	case errors.Is(err, errShutdown):
		t.Status = "interrupted"
		t.ErrorKind = "interrupted"
		t.ErrorMessage = "服务关闭，任务已中断"
		p.counters.interrupted.Add(1)
	default:
		t.Status = "failed"
		var te *taskError
		if errors.As(err, &te) {
			t.ErrorKind = te.kind
			t.ErrorMessage = te.msg
			if te.kind == "captcha_required" {
				p.counters.captchaAborts.Add(1)
			}
		} else {
			t.ErrorKind = "unknown"
			t.ErrorMessage = "评教任务异常中断"
		}
		p.counters.failed.Add(1)
	}
	p.persist(func(ctx context.Context) error { return p.st.UpdateTeacherEvalTask(ctx, t) })
	if t.Status != "completed" {
		p.log(t.ID, "任务结束："+t.ErrorMessage)
	}
}

// release frees a running slot and lets the next queued task start.
func (p *Pool) release(id string) {
	p.mu.Lock()
	delete(p.running, id)
	p.dispatchLocked()
	p.mu.Unlock()
}

// Cancel stops a task. A queued task is removed and never starts. A running
// task stops before its next submission; a submission already in flight
// completes and is reported honestly.
func (p *Pool) Cancel(ctx context.Context, userID, taskID string) (*store.TeacherEvalTask, error) {
	t, err := p.st.GetTeacherEvalTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.UserID != userID {
		return nil, store.ErrNotFound
	}
	if isTerminalTask(t.Status) {
		return t, nil
	}
	p.mu.Lock()
	if r, ok := p.running[taskID]; ok {
		p.mu.Unlock()
		r.abort(errUserCancelled)
		t.CancelRequested = true
		return t, nil
	}
	removed := false
	for i, id := range p.queue {
		if id == taskID {
			p.queue = append(p.queue[:i:i], p.queue[i+1:]...)
			removed = true
			break
		}
	}
	p.mu.Unlock()
	if removed || t.Status == "queued" {
		t.Status = "cancelled"
		t.CancelRequested = true
		t.FinishedAt = p.clock.Now()
		t.ErrorMessage = "已取消"
		p.counters.cancelled.Add(1)
		p.persist(func(ctx context.Context) error { return p.st.UpdateTeacherEvalTask(ctx, *t) })
		p.log(t.ID, "排队中的任务已取消")
	}
	return t, nil
}

// TaskView is the status payload for one task.
type TaskView struct {
	Task          store.TeacherEvalTask
	Courses       []store.TeacherEvalTaskCourse
	Logs          []store.TeacherEvalLog
	QueuePosition int
}

// Get returns one of the user's tasks with its courses and recent log.
func (p *Pool) Get(ctx context.Context, userID, taskID string) (*TaskView, error) {
	t, err := p.st.GetTeacherEvalTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.UserID != userID {
		return nil, store.ErrNotFound
	}
	courses, err := p.st.ListTeacherEvalCourses(ctx, taskID)
	if err != nil {
		return nil, err
	}
	logs, err := p.st.ListTeacherEvalLogs(ctx, taskID, logFetchLimit)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	position := p.positionLocked(taskID)
	p.mu.Unlock()
	return &TaskView{Task: *t, Courses: courses, Logs: logs, QueuePosition: position}, nil
}

// Active returns the user's queued or running task.
func (p *Pool) Active(ctx context.Context, userID string) (*store.TeacherEvalTask, error) {
	return p.st.ActiveTeacherEvalTask(ctx, userID)
}

// List returns the user's recent tasks, newest first.
func (p *Pool) List(ctx context.Context, userID string, limit int) ([]store.TeacherEvalTask, error) {
	return p.st.ListTeacherEvalTasks(ctx, userID, limit)
}

// Pause stops new dispatch and ends every running task at its next step.
// Resume restarts dispatch; ended tasks must be started again by their users.
func (p *Pool) Pause(paused bool) {
	p.mu.Lock()
	p.paused = paused
	var running []*runner
	if paused {
		for _, r := range p.running {
			running = append(running, r)
		}
	} else {
		p.dispatchLocked()
	}
	p.mu.Unlock()
	for _, r := range running {
		r.abort(&taskError{kind: "paused", msg: "运营已暂停自动评教，任务已停止"})
	}
}

func (p *Pool) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// PoolStats is the operator view.
type PoolStats struct {
	Enabled          bool
	Paused           bool
	Capacity         int
	MaxQueue         int
	Running          int
	Queued           int
	Started          int64
	Completed        int64
	Failed           int64
	Cancelled        int64
	Interrupted      int64
	CoursesSucceeded int64
	CoursesFailed    int64
	CaptchaAborts    int64
}

// Stats snapshots the pool.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	running, queued, paused := len(p.running), len(p.queue), p.paused
	p.mu.Unlock()
	return PoolStats{
		Enabled:          p.cfg.Enabled,
		Paused:           paused,
		Capacity:         p.cfg.Capacity,
		MaxQueue:         p.cfg.MaxQueue,
		Running:          running,
		Queued:           queued,
		Started:          p.counters.started.Load(),
		Completed:        p.counters.completed.Load(),
		Failed:           p.counters.failed.Load(),
		Cancelled:        p.counters.cancelled.Load(),
		Interrupted:      p.counters.interrupted.Load(),
		CoursesSucceeded: p.counters.coursesSucceeded.Load(),
		CoursesFailed:    p.counters.coursesFailed.Load(),
		CaptchaAborts:    p.counters.captchaAborts.Load(),
	}
}

// Status is the evaluation-open check the frontend uses to show "not open".
type Status struct {
	FeatureEnabled        bool
	Open                  bool
	ClosedMessage         string
	FirstVisitWaitSeconds int
	CheckedAt             time.Time
	Stale                 bool
}

// Status reports whether the school has evaluation open. The school's index
// is cached per user for StatusTTL. When the school cannot be reached, a
// cached answer up to staleStatusWindow old is returned and marked stale.
func (p *Pool) Status(ctx context.Context, userID, deviceID string, fresh bool) (*Status, error) {
	out := &Status{FeatureEnabled: p.cfg.Enabled && !p.isPaused()}
	now := p.clock.Now()
	p.statusMu.Lock()
	cached, hit := p.statuses[userID]
	p.statusMu.Unlock()
	if hit && !fresh && now.Sub(cached.at) < p.cfg.StatusTTL {
		fillFromCache(out, cached)
		return out, nil
	}
	idx, err := p.indexFor(ctx, userID, deviceID)
	if err != nil {
		if hit && now.Sub(cached.at) < staleStatusWindow {
			fillFromCache(out, cached)
			out.Stale = true
			return out, nil
		}
		return nil, err
	}
	entry := cachedIndex{
		at:    now,
		open:  !idx.IsClosed,
		msg:   idx.ClosedMessage,
		waitS: 0,
	}
	if idx.Protection.RequiresInitialWait() {
		entry.waitS = idx.Protection.WaitSeconds()
	}
	p.statusMu.Lock()
	p.statuses[userID] = entry
	p.statusMu.Unlock()
	fillFromCache(out, entry)
	return out, nil
}

func fillFromCache(out *Status, c cachedIndex) {
	out.Open = c.open
	out.ClosedMessage = c.msg
	out.FirstVisitWaitSeconds = c.waitS
	out.CheckedAt = c.at
}

// indexFor reads the school index with the user's session.
func (p *Pool) indexFor(ctx context.Context, userID, deviceID string) (*jwc.TeacherEvaluationIndexStatus, error) {
	rm, err := p.remote.For(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	return rm.Index(ctx)
}

// persist runs a store write with its own deadline, so writes still land while
// the pool is shutting down.
func (p *Pool) persist(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fn(ctx); err != nil {
		log.Printf("[teachereval] persist: %v", err)
	}
}

func (p *Pool) storeContext() context.Context {
	return context.Background()
}

func (p *Pool) log(taskID, message string) {
	at := p.clock.Now()
	p.persist(func(ctx context.Context) error {
		return p.st.AppendTeacherEvalLog(ctx, taskID, at, message)
	})
}

func normalizeMode(mode string) (string, error) {
	switch mode {
	case "", ScoreRandom:
		return ScoreRandom, nil
	case ScoreMax:
		return ScoreMax, nil
	default:
		return "", ErrInvalidScoreMode
	}
}

func isTerminalTask(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
		return true
	}
	return false
}

func isTerminalCourse(status string) bool {
	switch status {
	case stSuccess, stFailed, stCancelled, "skipped":
		return true
	}
	return false
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("teachereval: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
