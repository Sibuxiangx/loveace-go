package teachereval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

// Observer receives a batch's progress. Its methods are called from the engine's goroutines, so an
// implementation that shares state must synchronise it.
type Observer interface {
	// Init receives the rows of the batch before any course starts.
	Init(rows []Row)
	// Update receives a snapshot of one row after each change to it.
	Update(row Row)
	// Log receives one progress line, timed by the engine's clock.
	Log(at time.Time, message string)
}

// Row is the progress of one course of a batch.
type Row struct {
	CourseKey      string
	Name           string
	Teacher        string
	Position       int
	Status         string
	ErrorKind      string
	Message        string
	SubmitAttempts int
	SubmittedAt    time.Time
	UpdatedAt      time.Time
}

// Summary counts the courses of a batch that ended in success and in failure.
type Summary struct {
	Succeeded int
	Failed    int
}

// courseRun is one course the batch works on.
type courseRun struct {
	key  string
	name string
	c    *jwc.TeacherEvaluationCourse
}

// Engine runs one batch. It holds no lock across a wait or a school call.
type Engine struct {
	remote Remote
	mode   jwc.TeacherEvalScoreMode
	clock  Clock
	obs    Observer

	stopCh   chan struct{}
	stopOnce sync.Once
	stopErr  error // written once, before stopCh closes

	mu   sync.Mutex
	runs map[string]courseRun
	rows map[string]*Row
}

// New is the engine for one batch: remote is the school side, mode the answer policy, clock paces
// the batch, and obs receives its progress.
func New(remote Remote, mode jwc.TeacherEvalScoreMode, clock Clock, obs Observer) *Engine {
	return &Engine{
		remote: remote,
		mode:   mode,
		clock:  clock,
		obs:    obs,
		stopCh: make(chan struct{}),
		runs:   map[string]courseRun{},
		rows:   map[string]*Row{},
	}
}

// Abort ends the batch with reason. The first reason wins. A course that has not submitted yet
// stops without submitting; a course that has submitted is still confirmed. Abort may be called
// before Run or while it runs.
func (e *Engine) Abort(reason error) {
	e.stopOnce.Do(func() {
		e.stopErr = reason
		close(e.stopCh)
	})
}

// Run executes the batch to its end. It returns nil when every course has a terminal state,
// ErrCancelled or ErrInterrupted when the batch was stopped, or a *TaskError that ended it. The
// summary counts the courses that ended in success and in failure.
func (e *Engine) Run(ctx context.Context) (Summary, error) {
	err := e.run(ctx)
	return e.summary(), err
}

func (e *Engine) run(ctx context.Context) error {
	e.log("开始评教任务")
	list, err := e.courses(ctx)
	if err != nil {
		return e.settle(err)
	}
	if list.IsClosed {
		return e.settle(&TaskError{Kind: "closed", Msg: list.ClosedMessage})
	}
	runs := pendingRuns(list.Courses)
	if len(runs) == 0 {
		e.log("没有待评课程")
		return nil
	}
	e.initRows(runs)
	e.log(fmt.Sprintf("待评 %d 门课程：每 %d 秒启动一门，提交前等待 %d 秒",
		len(runs), int(LaunchInterval/time.Second), int(SubmitWait/time.Second)))
	if err := e.runRound(ctx, runs, list.TokenValue); err != nil {
		return e.settle(err)
	}
	if err := e.compensate(ctx); err != nil {
		return e.settle(err)
	}
	return e.settle(nil)
}

func (e *Engine) stopping() bool {
	select {
	case <-e.stopCh:
		return true
	default:
		return false
	}
}

// settle prefers the batch's stop reason over a side effect of it.
func (e *Engine) settle(err error) error {
	if e.stopping() {
		return e.stopErr
	}
	return err
}

// sleep waits on the clock, waking early for a stop and for the context ending.
func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ErrInterrupted
	case <-e.stopCh:
		return e.stopErr
	case <-e.clock.After(d):
		if e.stopping() {
			return e.stopErr
		}
		return nil
	}
}

// call runs one school step and classifies its failure. A fatal failure ends the batch.
func (e *Engine) call(fn func() error) error {
	err := ClassifyError(fn())
	if isFatal(err) {
		e.Abort(fatalReason(err))
	}
	return err
}

func fatalReason(err error) *TaskError {
	if errors.Is(err, ErrCaptchaRequired) {
		return &TaskError{Kind: "captcha_required", Msg: "学校网关需要验证码；后台任务无法等待人工输入，请稍后重新发起"}
	}
	return &TaskError{Kind: "session_required", Msg: "学校会话不可用，请重新登录后再发起任务"}
}

func (e *Engine) courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	var list *jwc.TeacherEvaluationCourseList
	err := e.call(func() (err error) {
		list, err = e.remote.Courses(ctx)
		return err
	})
	return list, err
}

// pendingRuns returns the unevaluated courses in the school's order, keyed uniquely.
func pendingRuns(list []jwc.TeacherEvaluationCourse) []courseRun {
	seen := map[string]bool{}
	var out []courseRun
	for i := range list {
		c := &list[i]
		if c.IsEvaluated {
			continue
		}
		key := CourseKey(c, i)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, courseRun{key: key, name: displayName(c), c: c})
	}
	return out
}

// initRows records the run set so the progress is visible from the start.
func (e *Engine) initRows(runs []courseRun) {
	now := e.clock.Now()
	rows := make([]Row, 0, len(runs))
	for i, cr := range runs {
		rows = append(rows, Row{
			CourseKey: cr.key,
			Name:      cr.c.Name,
			Teacher:   cr.c.Teacher,
			Position:  i,
			Status:    StatusPending,
			UpdatedAt: now,
		})
	}
	e.mu.Lock()
	for i, cr := range runs {
		e.runs[cr.key] = cr
		e.rows[cr.key] = &rows[i]
	}
	e.mu.Unlock()
	// The observer gets a copy: the engine keeps mutating its own rows.
	initial := make([]Row, len(rows))
	copy(initial, rows)
	e.obs.Init(initial)
}

// runRound launches courses in order, LaunchInterval apart, and waits for every launched course
// before it returns.
func (e *Engine) runRound(ctx context.Context, runs []courseRun, token string) error {
	var wg sync.WaitGroup
	var stopErr error
	for i, cr := range runs {
		if i > 0 {
			if err := e.sleep(ctx, LaunchInterval); err != nil {
				for _, rest := range runs[i:] {
					e.courseStopped(rest.key, err)
				}
				stopErr = err
				break
			}
		}
		wg.Add(1)
		go func(cr courseRun) {
			defer wg.Done()
			e.runCourse(ctx, cr, int64(len(runs)), token)
		}(cr)
	}
	wg.Wait()
	// A stop that landed while courses ran must end the batch, even when no stagger sleep
	// observed it (a single course, for example).
	return e.settle(stopErr)
}

// runCourse drives one course through prepare, wait, submit and, when the submit result is
// ambiguous, verification. A second submission is only made after the server has confirmed the
// course is still unevaluated.
func (e *Engine) runCourse(ctx context.Context, cr courseRun, pending int64, token string) {
	e.setState(cr.key, StatusPreparing, "正在访问评价页并生成表单")
	form, err := e.prepare(ctx, cr, pending, token)
	if err != nil {
		e.courseFailed(cr, err, "prepare_failed", "准备评价表单失败")
		return
	}
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			// Refresh the index token and rebuild the form. The old form is never reused.
			token = e.refreshToken(ctx, token)
			e.setState(cr.key, StatusPreparing, "提交失败，重新准备表单")
			form, err = e.prepareOnce(ctx, cr, pending, token)
			if err != nil {
				e.courseFailed(cr, err, "submit_failed", "重试准备评价表单失败")
				return
			}
		}
		e.setState(cr.key, StatusWaiting, "等待提交")
		if err := e.sleep(ctx, SubmitWait); err != nil {
			e.courseStopped(cr.key, err)
			return
		}
		if e.stopping() {
			e.courseStopped(cr.key, e.stopErr)
			return
		}
		e.markSubmitting(cr.key, attempt)
		res, err := e.submit(ctx, form)
		if err == nil && res != nil && res.Success {
			e.courseDone(cr, StatusSuccess, "", "提交成功")
			return
		}
		if isFatal(err) {
			e.courseStopped(cr.key, e.stopErr)
			return
		}
		// Ambiguous: the server may have recorded it. Confirm before anything else.
		e.setState(cr.key, StatusVerifying, "正在核实提交结果")
		done, verr := e.verify(ctx, cr.c)
		if verr != nil {
			if e.stopping() {
				e.courseStopped(cr.key, e.stopErr)
				return
			}
			// Unconfirmed: never resubmit blindly.
			e.courseDone(cr, StatusFailed, "verify_unknown", "提交结果无法核实，未重新提交")
			return
		}
		if done {
			e.courseDone(cr, StatusSuccess, "", "提交响应异常，服务器已确认")
			return
		}
		if e.stopping() {
			e.courseStopped(cr.key, e.stopErr)
			return
		}
		if attempt >= MaxSubmitAttempts {
			e.courseDone(cr, StatusFailed, "submit_failed", submitMessage(res))
			return
		}
	}
}

func (e *Engine) prepare(ctx context.Context, cr courseRun, pending int64, token string) (*jwc.TeacherEvaluationPreparedForm, error) {
	form, err := e.prepareOnce(ctx, cr, pending, token)
	if err == nil || isFatal(err) || e.stopping() {
		return form, err
	}
	// One immediate retry for a transient preparation failure, as the clients do.
	e.log(fmt.Sprintf("%s 准备失败，立即重试一次", cr.name))
	e.setState(cr.key, StatusPreparing, "准备失败，立即重试")
	return e.prepareOnce(ctx, cr, pending, token)
}

func (e *Engine) prepareOnce(ctx context.Context, cr courseRun, pending int64, token string) (*jwc.TeacherEvaluationPreparedForm, error) {
	var form *jwc.TeacherEvaluationPreparedForm
	err := e.call(func() (err error) {
		form, err = e.remote.Prepare(ctx, cr.c, pending, token, e.mode)
		return err
	})
	return form, err
}

func (e *Engine) submit(ctx context.Context, form *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	var res *jwc.TeacherEvaluationSubmitResult
	err := e.call(func() (err error) {
		res, err = e.remote.Submit(ctx, form)
		return err
	})
	return res, err
}

func (e *Engine) verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	var done bool
	err := e.call(func() (err error) {
		done, err = e.remote.Verify(ctx, c)
		return err
	})
	return done, err
}

// refreshToken fetches the current index token, keeping the old one when the fetch fails.
func (e *Engine) refreshToken(ctx context.Context, old string) string {
	var idx *jwc.TeacherEvaluationIndexStatus
	err := e.call(func() (err error) {
		idx, err = e.remote.Index(ctx)
		return err
	})
	if err != nil || idx == nil || idx.TokenValue == "" {
		return old
	}
	return idx.TokenValue
}

// compensate is the single post-batch round. Only failures that cannot have reached the server are
// retried: prepare failures, and submit failures the server confirmed as unevaluated. An
// unconfirmed submission is never retried.
func (e *Engine) compensate(ctx context.Context) error {
	retryable := e.retryableKeys()
	if len(retryable) == 0 {
		return nil
	}
	var list *jwc.TeacherEvaluationCourseList
	err := e.call(func() (err error) {
		list, err = e.remote.Courses(ctx)
		return err
	})
	if err != nil {
		if isFatal(err) || e.stopping() {
			return e.settle(err)
		}
		e.log("批后刷新失败，跳过补偿")
		return nil
	}
	var retry []courseRun
	for i := range list.Courses {
		fresh := &list.Courses[i]
		key := CourseKey(fresh, i)
		if _, ok := retryable[key]; !ok {
			continue
		}
		if fresh.IsEvaluated {
			e.courseDone(e.runs[key], StatusSuccess, "", "服务器已确认")
			continue
		}
		retry = append(retry, e.runs[key])
	}
	if len(retry) == 0 {
		e.log("批后补偿：失败课程均已确认已评，无需重试")
		return nil
	}
	e.log(fmt.Sprintf("批后补偿：%d 门课程将重试一轮", len(retry)))
	for _, cr := range retry {
		e.setState(cr.key, StatusPending, "批后重试等待中")
	}
	return e.runRound(ctx, retry, list.TokenValue)
}

// retryableKeys lists the failed courses a compensation round may retry.
func (e *Engine) retryableKeys() map[string]struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]struct{}{}
	for key, row := range e.rows {
		if row.Status != StatusFailed {
			continue
		}
		if row.ErrorKind == "prepare_failed" || row.ErrorKind == "submit_failed" {
			out[key] = struct{}{}
		}
	}
	return out
}

// --- course row helpers ---------------------------------------------------

func (e *Engine) setState(key, status, message string) {
	e.update(key, func(row *Row) {
		row.Status = status
		row.Message = message
	})
	e.log(fmt.Sprintf("%s：%s", e.runs[key].name, message))
}

func (e *Engine) markSubmitting(key string, attempt int) {
	now := e.clock.Now()
	e.update(key, func(row *Row) {
		row.Status = StatusSubmitting
		row.Message = "正在提交评价"
		row.SubmitAttempts = attempt
		row.SubmittedAt = now
	})
	e.log(fmt.Sprintf("%s：正在提交（第 %d 次）", e.runs[key].name, attempt))
}

func (e *Engine) courseDone(cr courseRun, status, kind, message string) {
	e.update(cr.key, func(row *Row) {
		row.Status = status
		row.ErrorKind = kind
		row.Message = message
	})
	if status == StatusSuccess {
		e.log(fmt.Sprintf("%s：%s", cr.name, message))
		return
	}
	e.log(fmt.Sprintf("%s 失败[%s]：%s", cr.name, kind, message))
}

func (e *Engine) courseFailed(cr courseRun, err error, kind, message string) {
	if isFatal(err) || e.stopping() {
		e.courseStopped(cr.key, e.settle(err))
		return
	}
	e.courseDone(cr, StatusFailed, kind, message)
}

// courseStopped marks a course the batch stopped before it finished.
func (e *Engine) courseStopped(key string, reason error) {
	message := "已取消"
	if !errors.Is(reason, ErrCancelled) {
		message = "任务中断，未完成"
		var te *TaskError
		if errors.As(reason, &te) {
			message = "任务中断：" + te.Msg
		}
	}
	e.update(key, func(row *Row) {
		if row.Status == StatusSuccess || row.Status == StatusFailed {
			return
		}
		row.Status = StatusCancelled
		row.Message = message
	})
}

// update mutates a row under the lock, then hands the observer a snapshot.
func (e *Engine) update(key string, mutate func(*Row)) {
	e.mu.Lock()
	row, ok := e.rows[key]
	if !ok {
		e.mu.Unlock()
		return
	}
	mutate(row)
	row.UpdatedAt = e.clock.Now()
	snapshot := *row
	e.mu.Unlock()
	e.obs.Update(snapshot)
}

func (e *Engine) summary() Summary {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s Summary
	for _, row := range e.rows {
		switch row.Status {
		case StatusSuccess:
			s.Succeeded++
		case StatusFailed:
			s.Failed++
		}
	}
	return s
}

// log hands the observer one progress line.
func (e *Engine) log(message string) {
	e.obs.Log(e.clock.Now(), message)
}

// submitMessage shows the school's own words when it gave any, trimmed to 120 runes.
func submitMessage(res *jwc.TeacherEvaluationSubmitResult) string {
	if res == nil || strings.TrimSpace(res.Message) == "" {
		return "提交失败"
	}
	msg := strings.TrimSpace(res.Message)
	if len([]rune(msg)) > 120 {
		msg = string([]rune(msg)[:120])
	}
	return msg
}
