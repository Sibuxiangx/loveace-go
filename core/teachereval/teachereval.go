// Package teachereval runs a batch of teacher evaluations for one school session, following the
// clients' pacing: courses start LaunchInterval apart, each waits SubmitWait before it submits, an
// ambiguous submission is confirmed before anything is sent again, and one compensation round
// retries only what cannot have reached the server.
//
// The engine holds no storage. Its progress goes to an Observer, so the server's background pool
// and the desktop app run the same logic.
package teachereval

import (
	"context"
	"errors"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

// Pacing of a batch, matching the clients.
const (
	// LaunchInterval separates course launches inside one batch.
	LaunchInterval = 6 * time.Second
	// SubmitWait is the pause between preparing a form and submitting it.
	SubmitWait = 140 * time.Second
	// MaxSubmitAttempts bounds submissions per course. A second submission only follows a
	// check that the server does not have the course recorded.
	MaxSubmitAttempts = 2
)

// Course statuses reported to an Observer.
const (
	StatusPending    = "pending"
	StatusPreparing  = "preparing"
	StatusWaiting    = "waiting"
	StatusSubmitting = "submitting"
	StatusVerifying  = "verifying"
	StatusSuccess    = "success"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// Sentinel errors. A Remote returns them, directly or wrapped, when the school side cannot carry
// on: the engine ends the batch on them.
var (
	// ErrCaptchaRequired: the gateway demanded a captcha. A batch cannot wait for a human.
	ErrCaptchaRequired = errors.New("teachereval: captcha required")
	// ErrSessionUnavailable: no usable school session.
	ErrSessionUnavailable = errors.New("teachereval: school session unavailable")
	// ErrCancelled ends a batch the user stopped. Submitted evaluations stay submitted.
	ErrCancelled = errors.New("teachereval: cancelled by user")
	// ErrInterrupted ends a batch whose context ended, such as at shutdown.
	ErrInterrupted = errors.New("teachereval: interrupted")
)

// TaskError is a terminal failure of the whole batch, with a stable machine kind.
type TaskError struct {
	Kind string
	Msg  string
}

func (e *TaskError) Error() string { return e.Kind + ": " + e.Msg }

// Remote is the school side one batch needs for one user. Errors may be the school's own; the
// engine classifies them with ClassifyError.
type Remote interface {
	Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error)
	Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error)
	Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error)
	Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error)
	Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error)
}

// ClassifyError maps a school failure onto the engine's sentinels. A gateway challenge or an
// interaction the batch cannot answer ends the batch as a captcha requirement; an expired login
// ends it as a session failure. Any other error fails only the course it came from.
func ClassifyError(err error) error {
	if err == nil || errors.Is(err, ErrCaptchaRequired) || errors.Is(err, ErrSessionUnavailable) {
		return err
	}
	switch errs.CodeOf(err) {
	case errs.InteractionRequired:
		return ErrCaptchaRequired
	case errs.AuthRequired:
		return ErrSessionUnavailable
	}
	return err
}

// isFatal is whether an error ends the whole batch rather than one course.
func isFatal(err error) bool {
	return errors.Is(err, ErrCaptchaRequired) || errors.Is(err, ErrSessionUnavailable)
}
