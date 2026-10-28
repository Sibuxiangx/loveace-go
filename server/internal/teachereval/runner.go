package teachereval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// Course statuses stored per task course.
const (
	stPending    = "pending"
	stPreparing  = "preparing"
	stWaiting    = "waiting"
	stSubmitting = "submitting"
	stVerifying  = "verifying"
	stSuccess    = "success"
	stFailed     = "failed"
	stCancelled  = "cancelled"
)

// Control reasons that end a task without a failure kind.
var (
	errUserCancelled = errors.New("teachereval: cancelled by user")
	errShutdown      = errors.New("teachereval: service shutting down")
)

// courseRun is one course the task will work on.
type courseRun struct {
	key  string
	name string
	c    *jwc.TeacherEvaluationCourse
}

// runner executes one task. It holds no lock across a wait or a network call.
type runner struct {
	p    *Pool
	task store.TeacherEvalTask
	runs map[string]courseRun

	stopCh   chan struct{}
	stopOnce sync.Once
	stopErr  error // written once, before stopCh closes

	mu    sync.Mutex
	rows  map[string]*store.TeacherEvalTaskCourse
	order []string
}

func newRunner(p *Pool, task store.TeacherEvalTask) *runner {
	return &runner{
		p:      p,
		task:   task,
		runs:   map[string]courseRun{},
		stopCh: make(chan struct{}),
		rows:   map[string]*store.TeacherEvalTaskCourse{},
	}
}

// abort ends the task. The first reason wins; sleeping courses wake up.
func (r *runner) abort(reason error) {
	r.stopOnce.Do(func() {
		r.stopErr = reason
		close(r.stopCh)
	})
}

func (r *runner) stopping() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

// settle prefers the task's stop reason over a side effect of it.
func (r *runner) settle(err error) error {
	if r.stopping() {
		return r.stopErr
	}
	return err
}

// sleep waits on the clock, waking early for a stop and for shutdown.
func (r *runner) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return errShutdown
	case <-r.stopCh:
		return r.stopErr
	case <-r.p.clock.After(d):
		if r.stopping() {
			return r.stopErr
		}
		return nil
	}
}

// call resolves a fresh session for one step and runs fn against it. A fatal
// school failure ends the whole task.
func (r *runner) call(ctx context.Context, fn func(Remote) error) error {
	rm, err := r.p.remote.For(ctx, r.task.UserID, r.task.DeviceID)
	if err == nil {
		err = fn(rm)
	}
	if isFatal(err) {
		r.abort(fatalReason(err))
	}
	return err
}

func fatalReason(err error) *taskError {
	if errors.Is(err, ErrCaptchaRequired) {
		return &taskError{kind: "captcha_required", msg: "学校网关需要验证码；后台任务无法等待人工输入，请稍后重新发起"}
	}
	return &taskError{kind: "session_required", msg: "学校会话不可用，请重新登录后再发起任务"}
}

// execute runs the whole task. It returns nil on completion, errUserCancelled,
// errShutdown, or a *taskError with the failure kind.
func (r *runner) execute(ctx context.Context) error {
	r.log("开始评教任务")
	if err := r.gate(ctx); err != nil {
		return r.settle(err)
	}
	list, err := r.courses(ctx)
	if err != nil {
		return r.settle(err)
	}
	runs := pendingRuns(list.Courses)
	if len(runs) == 0 {
		r.log("没有待评课程")
		return nil
	}
	r.initRows(runs)
	r.log(fmt.Sprintf("待评 %d 门课程：每 %d 秒启动一门，提交前等待 %d 秒",
		len(runs), int(LaunchInterval/time.Second), int(SubmitWait/time.Second)))
	if err := r.runRound(ctx, runs, list.TokenValue); err != nil {
		return r.settle(err)
	}
	if err := r.compensate(ctx); err != nil {
		return r.settle(err)
	}
	return r.settle(nil)
}

// gate reproduces the first-visit protection flow: refuse a closed evaluation,
// and wait out the reading countdown up to MaxProtectionWaits times.
func (r *runner) gate(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		var idx *jwc.TeacherEvaluationIndexStatus
		err := r.call(ctx, func(rm Remote) (err error) {
			idx, err = rm.Index(ctx)
			return err
		})
		if err != nil {
			return err
		}
		if idx.IsClosed {
			return &taskError{kind: "closed", msg: idx.ClosedMessage}
		}
		if !idx.Protection.RequiresInitialWait() {
			return nil
		}
		if attempt >= MaxProtectionWaits {
			return &taskError{kind: "protection_unresolved", msg: "首次访问保护未解除，请稍后再发起任务"}
		}
		secs := idx.Protection.WaitSeconds()
		r.log(fmt.Sprintf("首次评教需阅读须知，等待 %d 秒后继续", secs))
		if err := r.sleep(ctx, time.Duration(secs)*time.Second); err != nil {
			return err
		}
	}
}

func (r *runner) courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	var list *jwc.TeacherEvaluationCourseList
	err := r.call(ctx, func(rm Remote) (err error) {
		list, err = rm.Courses(ctx)
		return err
	})
	return list, err
}

// pendingRuns returns the unevaluated courses in server order, keyed uniquely.
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

// initRows records the run set so progress is visible and recoverable.
func (r *runner) initRows(runs []courseRun) {
	now := r.p.clock.Now()
	rows := make([]store.TeacherEvalTaskCourse, 0, len(runs))
	for i, cr := range runs {
		r.runs[cr.key] = cr
		rows = append(rows, store.TeacherEvalTaskCourse{
			TaskID:    r.task.ID,
			CourseKey: cr.key,
			Name:      cr.c.Name,
			Teacher:   cr.c.Teacher,
			Position:  i,
			Status:    stPending,
			UpdatedAt: now,
		})
	}
	r.mu.Lock()
	for i := range rows {
		r.rows[rows[i].CourseKey] = &rows[i]
		r.order = append(r.order, rows[i].CourseKey)
	}
	r.mu.Unlock()
	r.p.persist(func(ctx context.Context) error {
		return r.p.st.SetTeacherEvalCourses(ctx, r.task.ID, rows)
	})
	r.task.CourseTotal = len(runs)
	r.p.persist(func(ctx context.Context) error {
		return r.p.st.UpdateTeacherEvalTask(ctx, r.task)
	})
}

// runRound launches courses in order, LaunchInterval apart, and waits for
// every launched course before returning.
func (r *runner) runRound(ctx context.Context, runs []courseRun, token string) error {
	var wg sync.WaitGroup
	var stopErr error
	for i, cr := range runs {
		if i > 0 {
			if err := r.sleep(ctx, LaunchInterval); err != nil {
				for _, rest := range runs[i:] {
					r.courseStopped(rest.key, err)
				}
				stopErr = err
				break
			}
		}
		wg.Add(1)
		go func(cr courseRun) {
			defer wg.Done()
			r.runCourse(ctx, cr, int64(len(runs)), token)
		}(cr)
	}
	wg.Wait()
	// A stop that landed while courses ran must end the task, even when
	// no stagger sleep observed it (a single course, for example).
	return r.settle(stopErr)
}

// runCourse drives one course through prepare, wait, submit and, when the
// submit result is ambiguous, verification. A second submission is only ever
// made after the server has confirmed the course is still unevaluated.
func (r *runner) runCourse(ctx context.Context, cr courseRun, pending int64, token string) {
	r.setState(cr.key, stPreparing, "正在访问评价页并生成表单")
	form, err := r.prepare(ctx, cr, pending, token)
	if err != nil {
		r.courseFailed(cr, err, "prepare_failed", "准备评价表单失败")
		return
	}
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			// Refresh the index token and rebuild the form. The old form is
			// never reused, matching the clients.
			token = r.refreshToken(ctx, token)
			r.setState(cr.key, stPreparing, "提交失败，重新准备表单")
			form, err = r.prepareOnce(ctx, cr, pending, token)
			if err != nil {
				r.courseFailed(cr, err, "submit_failed", "重试准备评价表单失败")
				return
			}
		}
		r.setState(cr.key, stWaiting, "等待提交")
		if err := r.sleep(ctx, SubmitWait); err != nil {
			r.courseStopped(cr.key, err)
			return
		}
		if r.stopping() {
			r.courseStopped(cr.key, r.stopErr)
			return
		}
		r.markSubmitting(cr.key, attempt)
		res, err := r.submit(ctx, form)
		if err == nil && res != nil && res.Success {
			r.courseDone(cr, stSuccess, "", "提交成功")
			return
		}
		if isFatal(err) {
			r.courseStopped(cr.key, r.stopErr)
			return
		}
		// Ambiguous: the server may have recorded it. Confirm first.
		r.setState(cr.key, stVerifying, "正在核实提交结果")
		done, verr := r.verify(ctx, cr.c)
		if verr != nil {
			if r.stopping() {
				r.courseStopped(cr.key, r.stopErr)
				return
			}
			// Unconfirmed: never resubmit blindly.
			r.courseDone(cr, stFailed, "verify_unknown", "提交结果无法核实，未重新提交")
			return
		}
		if done {
			r.courseDone(cr, stSuccess, "", "提交响应异常，服务器已确认")
			return
		}
		if r.stopping() {
			r.courseStopped(cr.key, r.stopErr)
			return
		}
		if attempt >= MaxSubmitAttempts {
			r.courseDone(cr, stFailed, "submit_failed", submitMessage(res))
			return
		}
	}
}

func (r *runner) prepare(ctx context.Context, cr courseRun, pending int64, token string) (*jwc.TeacherEvaluationPreparedForm, error) {
	form, err := r.prepareOnce(ctx, cr, pending, token)
	if err == nil || isFatal(err) || r.stopping() {
		return form, err
	}
	// One immediate retry for a transient preparation failure, as the clients do.
	r.log(fmt.Sprintf("%s 准备失败，立即重试一次", cr.name))
	r.setState(cr.key, stPreparing, "准备失败，立即重试")
	return r.prepareOnce(ctx, cr, pending, token)
}

func (r *runner) prepareOnce(ctx context.Context, cr courseRun, pending int64, token string) (*jwc.TeacherEvaluationPreparedForm, error) {
	var form *jwc.TeacherEvaluationPreparedForm
	err := r.call(ctx, func(rm Remote) (err error) {
		form, err = rm.Prepare(ctx, cr.c, pending, token, r.mode())
		return err
	})
	return form, err
}

func (r *runner) submit(ctx context.Context, form *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	var res *jwc.TeacherEvaluationSubmitResult
	err := r.call(ctx, func(rm Remote) (err error) {
		res, err = rm.Submit(ctx, form)
		return err
	})
	return res, err
}

func (r *runner) verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	var done bool
	err := r.call(ctx, func(rm Remote) (err error) {
		done, err = rm.Verify(ctx, c)
		return err
	})
	return done, err
}

// refreshToken fetches the current index token, keeping the old one on failure.
func (r *runner) refreshToken(ctx context.Context, old string) string {
	var idx *jwc.TeacherEvaluationIndexStatus
	err := r.call(ctx, func(rm Remote) (err error) {
		idx, err = rm.Index(ctx)
		return err
	})
	if err != nil || idx == nil || idx.TokenValue == "" {
		return old
	}
	return idx.TokenValue
}

// compensate is the single post-batch round. Only failures that cannot have
// reached the server are retried: prepare failures and submit failures the
// server confirmed as unevaluated. An unconfirmed submission is never retried.
func (r *runner) compensate(ctx context.Context) error {
	retryable := r.retryableKeys()
	if len(retryable) == 0 {
		return nil
	}
	var list *jwc.TeacherEvaluationCourseList
	err := r.call(ctx, func(rm Remote) (err error) {
		list, err = rm.Courses(ctx)
		return err
	})
	if err != nil {
		if isFatal(err) || r.stopping() {
			return r.settle(err)
		}
		r.log("批后刷新失败，跳过补偿")
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
			r.courseDone(r.runs[key], stSuccess, "", "服务器已确认")
			continue
		}
		retry = append(retry, r.runs[key])
	}
	if len(retry) == 0 {
		r.log("批后补偿：失败课程均已确认已评，无需重试")
		return nil
	}
	r.log(fmt.Sprintf("批后补偿：%d 门课程将重试一轮", len(retry)))
	for _, cr := range retry {
		r.setState(cr.key, stPending, "批后重试等待中")
	}
	return r.runRound(ctx, retry, list.TokenValue)
}

// retryableKeys lists failed courses that a compensation round may retry.
func (r *runner) retryableKeys() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]struct{}{}
	for key, row := range r.rows {
		if row.Status != stFailed {
			continue
		}
		if row.ErrorKind == "prepare_failed" || row.ErrorKind == "submit_failed" {
			out[key] = struct{}{}
		}
	}
	return out
}

// mode maps the stored score mode onto the core option policy.
func (r *runner) mode() jwc.TeacherEvalScoreMode {
	if r.task.ScoreMode == ScoreMax {
		return jwc.TeacherEvalScoreMax
	}
	return jwc.TeacherEvalScoreRandom
}

// --- course row helpers --------------------------------------------------

func (r *runner) setState(key, status, message string) {
	r.update(key, func(row *store.TeacherEvalTaskCourse) {
		row.Status = status
		row.Message = message
	})
	r.log(fmt.Sprintf("%s：%s", r.runs[key].name, message))
}

func (r *runner) markSubmitting(key string, attempt int) {
	now := r.p.clock.Now()
	r.update(key, func(row *store.TeacherEvalTaskCourse) {
		row.Status = stSubmitting
		row.Message = "正在提交评价"
		row.SubmitAttempts = attempt
		row.SubmittedAt = now
	})
	r.log(fmt.Sprintf("%s：正在提交（第 %d 次）", r.runs[key].name, attempt))
}

func (r *runner) courseDone(cr courseRun, status, kind, message string) {
	r.update(cr.key, func(row *store.TeacherEvalTaskCourse) {
		row.Status = status
		row.ErrorKind = kind
		row.Message = message
	})
	if status == stSuccess {
		r.p.counters.coursesSucceeded.Add(1)
		r.log(fmt.Sprintf("%s：%s", cr.name, message))
		return
	}
	r.p.counters.coursesFailed.Add(1)
	r.log(fmt.Sprintf("%s 失败[%s]：%s", cr.name, kind, message))
}

func (r *runner) courseFailed(cr courseRun, err error, kind, message string) {
	if isFatal(err) || r.stopping() {
		r.courseStopped(cr.key, r.settle(err))
		return
	}
	r.courseDone(cr, stFailed, kind, message)
}

// courseStopped marks a course that the task stopped before it finished.
func (r *runner) courseStopped(key string, reason error) {
	message := "已取消"
	if !errors.Is(reason, errUserCancelled) {
		message = "任务中断，未完成"
		var te *taskError
		if errors.As(reason, &te) {
			message = "任务中断：" + te.msg
		}
	}
	r.update(key, func(row *store.TeacherEvalTaskCourse) {
		if row.Status == stSuccess || row.Status == stFailed {
			return
		}
		row.Status = stCancelled
		row.Message = message
	})
}

// markRemaining cancels courses that never launched.
func (r *runner) markRemaining(runs []courseRun, reason error) {
	for _, cr := range runs {
		r.courseStopped(cr.key, reason)
	}
}

// update mutates a row under the lock, then persists the snapshot.
func (r *runner) update(key string, mutate func(*store.TeacherEvalTaskCourse)) {
	r.mu.Lock()
	row, ok := r.rows[key]
	if !ok {
		r.mu.Unlock()
		return
	}
	mutate(row)
	row.UpdatedAt = r.p.clock.Now()
	snapshot := *row
	r.mu.Unlock()
	r.p.persist(func(ctx context.Context) error {
		return r.p.st.UpdateTeacherEvalCourse(ctx, snapshot)
	})
}

// retryableRows reports how many courses reached a terminal state.
func (r *runner) summary() (succeeded, failed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		switch row.Status {
		case stSuccess:
			succeeded++
		case stFailed:
			failed++
		}
	}
	return succeeded, failed
}

// log appends one sanitized progress line for the task.
func (r *runner) log(message string) {
	at := r.p.clock.Now()
	r.p.persist(func(ctx context.Context) error {
		return r.p.st.AppendTeacherEvalLog(ctx, r.task.ID, at, message)
	})
}

// submitMessage shows the server's own words when it gave any, trimmed.
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
