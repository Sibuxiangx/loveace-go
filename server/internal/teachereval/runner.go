package teachereval

import (
	"context"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	tecore "github.com/Sibuxiangx/loveace-go/core/teachereval"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// runner is one task on the server. The batch itself runs in core/teachereval; the runner resolves
// a school session for every step, and keeps the task and its course rows in the store as the engine
// reports progress.
type runner struct {
	p    *Pool
	task store.TeacherEvalTask

	mu    sync.Mutex
	eng   *tecore.Engine
	early error // an abort that arrived before the engine started
	sum   tecore.Summary
}

func newRunner(p *Pool, task store.TeacherEvalTask) *runner {
	return &runner{p: p, task: task}
}

// abort ends the task with reason. An abort that arrives before the engine starts is kept, and
// applied as the engine starts.
func (r *runner) abort(reason error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eng != nil {
		r.eng.Abort(reason)
		return
	}
	if r.early == nil {
		r.early = reason
	}
}

// execute runs the task to its end. The task is loaded before it is called.
func (r *runner) execute(ctx context.Context) error {
	eng := tecore.New(stepRemote{r: r}, r.mode(), r.p.clock, storeObserver{r: r})
	r.mu.Lock()
	r.eng = eng
	if r.early != nil {
		eng.Abort(r.early)
	}
	r.mu.Unlock()
	sum, err := eng.Run(ctx)
	r.sum = sum
	r.p.counters.coursesSucceeded.Add(int64(sum.Succeeded))
	r.p.counters.coursesFailed.Add(int64(sum.Failed))
	return err
}

// summary is how many courses ended in success and in failure.
func (r *runner) summary() (succeeded, failed int) {
	return r.sum.Succeeded, r.sum.Failed
}

// mode maps the stored score mode onto the core answer policy.
func (r *runner) mode() jwc.TeacherEvalScoreMode {
	if r.task.ScoreMode == ScoreMax {
		return jwc.TeacherEvalScoreMax
	}
	return jwc.TeacherEvalScoreRandom
}

// stepRemote resolves the task's school session for each step the engine takes. A step that runs
// long after the previous one still uses a live session, and a session that expired is recovered by
// the next step.
type stepRemote struct {
	r *runner
}

func (s stepRemote) step(ctx context.Context) (tecore.Remote, error) {
	return s.r.p.remote.For(ctx, s.r.task.UserID, s.r.task.DeviceID)
}

func (s stepRemote) Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error) {
	rm, err := s.step(ctx)
	if err != nil {
		return nil, err
	}
	return rm.Index(ctx)
}

func (s stepRemote) Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	rm, err := s.step(ctx)
	if err != nil {
		return nil, err
	}
	return rm.Courses(ctx)
}

func (s stepRemote) Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error) {
	rm, err := s.step(ctx)
	if err != nil {
		return nil, err
	}
	return rm.Prepare(ctx, c, pending, token, mode)
}

func (s stepRemote) Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	rm, err := s.step(ctx)
	if err != nil {
		return nil, err
	}
	return rm.Submit(ctx, p)
}

func (s stepRemote) Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	rm, err := s.step(ctx)
	if err != nil {
		return false, err
	}
	return rm.Verify(ctx, c)
}

// storeObserver persists the engine's progress: the course rows, the task's course total, and the
// log lines.
type storeObserver struct {
	r *runner
}

func (o storeObserver) Init(rows []tecore.Row) {
	p, id := o.r.p, o.r.task.ID
	stored := make([]store.TeacherEvalTaskCourse, 0, len(rows))
	for _, row := range rows {
		stored = append(stored, storedRow(id, row))
	}
	p.persist(func(ctx context.Context) error { return p.st.SetTeacherEvalCourses(ctx, id, stored) })
	o.r.task.CourseTotal = len(rows)
	task := o.r.task
	p.persist(func(ctx context.Context) error { return p.st.UpdateTeacherEvalTask(ctx, task) })
}

func (o storeObserver) Update(row tecore.Row) {
	p := o.r.p
	stored := storedRow(o.r.task.ID, row)
	p.persist(func(ctx context.Context) error { return p.st.UpdateTeacherEvalCourse(ctx, stored) })
}

func (o storeObserver) Log(at time.Time, message string) {
	p, id := o.r.p, o.r.task.ID
	p.persist(func(ctx context.Context) error { return p.st.AppendTeacherEvalLog(ctx, id, at, message) })
}

func storedRow(taskID string, row tecore.Row) store.TeacherEvalTaskCourse {
	return store.TeacherEvalTaskCourse{
		TaskID:         taskID,
		CourseKey:      row.CourseKey,
		Name:           row.Name,
		Teacher:        row.Teacher,
		Position:       row.Position,
		Status:         row.Status,
		ErrorKind:      row.ErrorKind,
		Message:        row.Message,
		SubmitAttempts: row.SubmitAttempts,
		SubmittedAt:    row.SubmittedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
