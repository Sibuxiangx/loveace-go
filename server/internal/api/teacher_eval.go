package api

// Auto teacher-evaluation endpoints. The school's evaluation switch is read
// through the user's own session; writes never run inside a request. They go
// to the background pool, which owns pacing, retries and recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/teachereval"
)

// teachCreateOp names the idempotency scope of task creation.
const teachCreateOp = "teacher_eval.create_task"

func (s *Server) registerTeacherEval(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-status",
		Summary:     "Whether the school has teacher evaluation open",
		Method:      http.MethodGet,
		Path:        "/v1/me/teacher-eval/status",
		Tags:        []string{"teacher-eval"},
	}, s.teachStatus)
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-create-task",
		Summary:     "Start a background auto-evaluation task (Idempotency-Key required)",
		Method:      http.MethodPost,
		Path:        "/v1/me/teacher-eval/tasks",
		Tags:        []string{"teacher-eval"},
	}, s.teachCreate)
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-active-task",
		Summary:     "The user's queued or running task",
		Method:      http.MethodGet,
		Path:        "/v1/me/teacher-eval/tasks/active",
		Tags:        []string{"teacher-eval"},
	}, s.teachActive)
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-list-tasks",
		Summary:     "Recent auto-evaluation tasks, newest first",
		Method:      http.MethodGet,
		Path:        "/v1/me/teacher-eval/tasks",
		Tags:        []string{"teacher-eval"},
	}, s.teachList)
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-get-task",
		Summary:     "One task with per-course progress and recent log",
		Method:      http.MethodGet,
		Path:        "/v1/me/teacher-eval/tasks/{id}",
		Tags:        []string{"teacher-eval"},
	}, s.teachGet)
	huma.Register(api, huma.Operation{
		OperationID: "teacher-eval-cancel-task",
		Summary:     "Cancel a task; submissions already sent cannot be withdrawn",
		Method:      http.MethodPost,
		Path:        "/v1/me/teacher-eval/tasks/{id}/cancel",
		Tags:        []string{"teacher-eval"},
	}, s.teachCancel)
}

type teachStatusInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Fresh            bool   `query:"fresh" required:"false"`
}

type teachStatusBody struct {
	FeatureEnabled        bool      `json:"feature_enabled"`
	Open                  bool      `json:"open"`
	ClosedMessage         string    `json:"closed_message,omitempty"`
	FirstVisitWaitSeconds int       `json:"first_visit_wait_seconds,omitempty"`
	CheckedAt             time.Time `json:"checked_at"`
	Stale                 bool      `json:"stale,omitempty"`
}

func (s *Server) teachStatus(ctx context.Context, in *teachStatusInput) (*Output[teachStatusBody], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	st, err := s.deps.Teach.Status(ctx, claims.UserID, claims.DeviceID, in.Fresh)
	if err != nil {
		return nil, teachProblem(err, nil)
	}
	return &Output[teachStatusBody]{Status: http.StatusOK, Body: teachStatusBody{
		FeatureEnabled:        st.FeatureEnabled,
		Open:                  st.Open,
		ClosedMessage:         st.ClosedMessage,
		FirstVisitWaitSeconds: st.FirstVisitWaitSeconds,
		CheckedAt:             st.CheckedAt,
		Stale:                 st.Stale,
	}}, nil
}

type teachCreateInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	IdempotencyKey   string `header:"Idempotency-Key" required:"false"`
	Body             struct {
		// ScoreMode is "random" (default) or "max". Courses are always all
		// unevaluated courses.
		ScoreMode string `json:"score_mode" required:"false"`
	}
}

// teachCreate follows the ops write semantics: the key is mandatory, a retry
// with the same key replays the stored outcome, and a different body under the
// same key is refused.
func (s *Server) teachCreate(ctx context.Context, in *teachCreateInput) (*Output[any], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	if in.IdempotencyKey == "" {
		return nil, newProblem(http.StatusPreconditionRequired, "idempotency_key_required", "创建任务必须携带 Idempotency-Key 头")
	}
	// Admission gates that do not touch the school run before the key is
	// reserved: a refused request must not leave a stored failure behind,
	// or a retry with the same key after the feature is re-enabled would replay the refusal.
	if err := s.deps.Teach.Precheck(ctx, claims.UserID, in.Body.ScoreMode); err != nil {
		return nil, teachProblem(err, nil)
	}
	args, _ := json.Marshal(map[string]string{"score_mode": in.Body.ScoreMode})
	hash := idempotencyHash(teachCreateOp, args)
	prev, existed, err := s.deps.Store.ReserveIdempotency(ctx, store.IdempotencyRecord{
		Key: in.IdempotencyKey, UserID: claims.UserID, Op: teachCreateOp, RequestHash: hash, Status: "pending",
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if existed {
		status, body, rerr := s.replayOrRefuse(ctx, claims.UserID, in.IdempotencyKey, teachCreateOp, prev, hash)
		if rerr != nil {
			return nil, rerr
		}
		return &Output[any]{Status: status, Body: body}, nil
	}

	task, position, cerr := s.deps.Teach.Create(ctx, claims.UserID, claims.DeviceID, in.Body.ScoreMode)
	if cerr != nil {
		p := teachProblem(cerr, task)
		s.finalizeIdem(ctx, claims.UserID, in.IdempotencyKey, "failed", idemEnvelope{Status: problemStatus(p), Body: marshalProblem(p)})
		return nil, p
	}
	body := teachTaskBodyOf(task, position)
	raw, _ := json.Marshal(body)
	s.finalizeIdem(ctx, claims.UserID, in.IdempotencyKey, "done", idemEnvelope{Status: http.StatusAccepted, Body: raw})
	return &Output[any]{Status: http.StatusAccepted, Body: body}, nil
}

func (s *Server) teachActive(ctx context.Context, in *teachAuthInput) (*Output[teachTaskBody], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	task, err := s.deps.Teach.Active(ctx, claims.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newProblem(http.StatusNotFound, "not_found", "没有进行中的评教任务")
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return &Output[teachTaskBody]{Status: http.StatusOK, Body: teachTaskBodyOf(task, s.deps.Teach.QueuePosition(task.ID))}, nil
}

type teachAuthInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
}

type teachListInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Limit            int    `query:"limit" required:"false"`
}

func (s *Server) teachList(ctx context.Context, in *teachListInput) (*Output[[]teachTaskBody], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	tasks, err := s.deps.Teach.List(ctx, claims.UserID, in.Limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]teachTaskBody, 0, len(tasks))
	for i := range tasks {
		out = append(out, teachTaskBodyOf(&tasks[i], s.deps.Teach.QueuePosition(tasks[i].ID)))
	}
	return &Output[[]teachTaskBody]{Status: http.StatusOK, Body: out}, nil
}

type teachTaskIDInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	ID               string `path:"id" minLength:"1" required:"false"`
}

type teachDetailBody struct {
	Task    teachTaskBody     `json:"task"`
	Courses []teachCourseBody `json:"courses"`
	Logs    []teachLogBody    `json:"logs"`
}

type teachTaskBody struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	ScoreMode       string     `json:"score_mode"`
	CourseTotal     int        `json:"course_total"`
	QueuePosition   int        `json:"queue_position,omitempty"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
	ErrorKind       string     `json:"error_kind,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

type teachCourseBody struct {
	Key            string     `json:"key"`
	Name           string     `json:"name"`
	Teacher        string     `json:"teacher"`
	Position       int        `json:"position"`
	Status         string     `json:"status"`
	ErrorKind      string     `json:"error_kind,omitempty"`
	Message        string     `json:"message,omitempty"`
	SubmitAttempts int        `json:"submit_attempts"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type teachLogBody struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (s *Server) teachGet(ctx context.Context, in *teachTaskIDInput) (*Output[teachDetailBody], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	view, err := s.deps.Teach.Get(ctx, claims.UserID, in.ID)
	if err != nil {
		return nil, teachLookupErr(err)
	}
	body := teachDetailBody{
		Task:    teachTaskBodyOf(&view.Task, view.QueuePosition),
		Courses: make([]teachCourseBody, 0, len(view.Courses)),
		Logs:    make([]teachLogBody, 0, len(view.Logs)),
	}
	for _, c := range view.Courses {
		body.Courses = append(body.Courses, teachCourseBody{
			Key: c.CourseKey, Name: c.Name, Teacher: c.Teacher, Position: c.Position,
			Status: c.Status, ErrorKind: c.ErrorKind, Message: c.Message,
			SubmitAttempts: c.SubmitAttempts, SubmittedAt: teachTimePtr(c.SubmittedAt), UpdatedAt: c.UpdatedAt,
		})
	}
	for _, l := range view.Logs {
		body.Logs = append(body.Logs, teachLogBody{At: l.At, Message: l.Message})
	}
	return &Output[teachDetailBody]{Status: http.StatusOK, Body: body}, nil
}

func (s *Server) teachCancel(ctx context.Context, in *teachTaskIDInput) (*Output[teachTaskBody], error) {
	claims, err := s.authenticate(ctx, in.RawAuthorization)
	if err != nil {
		return nil, err
	}
	task, err := s.deps.Teach.Cancel(ctx, claims.UserID, in.ID)
	if err != nil {
		return nil, teachLookupErr(err)
	}
	return &Output[teachTaskBody]{Status: http.StatusAccepted, Body: teachTaskBodyOf(task, 0)}, nil
}

// teachLookupErr maps a missing or foreign task to 404 and anything else to
// the shared mapping.
func teachLookupErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return newProblem(http.StatusNotFound, "not_found", "评教任务不存在")
	}
	return teachProblem(err, nil)
}

// teachProblem maps teachereval errors onto stable problem codes. The task is
// set only for ErrTaskActive, so the client can jump to it.
func teachProblem(err error, task *store.TeacherEvalTask) error {
	switch {
	case errors.Is(err, teachereval.ErrDisabled):
		return newProblem(http.StatusServiceUnavailable, "teacher_eval.disabled", "自动评教暂未开放")
	case errors.Is(err, teachereval.ErrPaused):
		return newProblem(http.StatusServiceUnavailable, "teacher_eval.paused", "自动评教已暂停，请稍后再试")
	case errors.Is(err, teachereval.ErrInvalidScoreMode):
		return newProblem(http.StatusUnprocessableEntity, "invalid_argument", "评分模式只能是 random 或 max")
	case errors.Is(err, teachereval.ErrTaskActive):
		p := newProblem(http.StatusConflict, "teacher_eval.task_active", "已有进行中的自动评教任务")
		if task != nil {
			p.TaskID = task.ID
		}
		return p
	case errors.Is(err, teachereval.ErrQueueFull):
		return newProblem(http.StatusTooManyRequests, "teacher_eval.queue_full", "自动评教排队已满，请稍后再试")
	case errors.Is(err, teachereval.ErrSessionUnavailable):
		return newProblem(http.StatusUnauthorized, "auth.school_login_required", "没有可用的学校会话，请重新登录")
	case errors.Is(err, teachereval.ErrCaptchaRequired):
		return newProblem(http.StatusConflict, "school.captcha_required", "学校网关需要验证码，请稍后重试")
	}
	var closed *teachereval.ClosedError
	if errors.As(err, &closed) {
		msg := closed.Message
		if msg == "" {
			msg = "学校尚未开放教师评价"
		}
		return newProblem(http.StatusConflict, "teacher_eval.closed", msg)
	}
	return mapErr(err)
}

func teachTaskBodyOf(t *store.TeacherEvalTask, position int) teachTaskBody {
	return teachTaskBody{
		ID:              t.ID,
		Status:          t.Status,
		ScoreMode:       t.ScoreMode,
		CourseTotal:     t.CourseTotal,
		QueuePosition:   position,
		CancelRequested: t.CancelRequested,
		ErrorKind:       t.ErrorKind,
		ErrorMessage:    t.ErrorMessage,
		CreatedAt:       t.CreatedAt,
		StartedAt:       teachTimePtr(t.StartedAt),
		FinishedAt:      teachTimePtr(t.FinishedAt),
	}
}

// teachTimePtr renders the store's zero time as absent.
func teachTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// adminTeachPool is the operator view of the pool.
func (s *Server) adminTeachPool(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.deps.Teach.Stats())
}

// adminTeachPause is the global switch. Pausing refuses new tasks and ends
// running ones at their next step; resuming restarts dispatch.
func (s *Server) adminTeachPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(newProblem(http.StatusUnprocessableEntity, "invalid_argument", "需要 paused 字段"))
		return
	}
	s.deps.Teach.Pause(in.Paused)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.deps.Teach.Stats())
}
