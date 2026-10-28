package teachereval

import (
	"context"
	"errors"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

// Client pacing, identical on the iOS, Android and desktop clients.
const (
	// LaunchInterval separates course launches inside one task.
	LaunchInterval = 6 * time.Second
	// SubmitWait is the pause between preparing a form and submitting it.
	SubmitWait = 140 * time.Second
	// MaxProtectionWaits bounds first-visit reloads.
	MaxProtectionWaits = 2
	// MaxSubmitAttempts bounds submissions per course. A second attempt only
	// happens after the server has confirmed the course is still unevaluated.
	MaxSubmitAttempts = 2
)

// Remote is the school-side surface one task needs for one user.
type Remote interface {
	Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error)
	Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error)
	Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error)
	Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error)
	Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error)
}

// RemoteFactory resolves a user's live school session for one step. Every
// step goes through it so that the session manager sees the activity and
// recovers expired sessions before any call that cannot be replayed.
type RemoteFactory interface {
	For(ctx context.Context, userID, deviceID string) (Remote, error)
}

// Sentinel errors the school adapter maps failures onto. The engine decides
// what they mean for a task; it never inspects school-side types.
var (
	// ErrCaptchaRequired: the gateway demanded a captcha. Background tasks
	// cannot wait for a human, so the task ends.
	ErrCaptchaRequired = errors.New("teachereval: captcha required")
	// ErrSessionUnavailable: no usable school session and no vault relogin.
	ErrSessionUnavailable = errors.New("teachereval: school session unavailable")
)

// isFatal reports whether an error ends the whole task rather than one course.
func isFatal(err error) bool {
	return errors.Is(err, ErrCaptchaRequired) || errors.Is(err, ErrSessionUnavailable)
}

// taskError is a terminal task failure with a stable machine kind.
type taskError struct {
	kind string
	msg  string
}

func (e *taskError) Error() string { return e.kind + ": " + e.msg }

// unavailableFactory is used when no school binding is configured. Status
// and admission then report the school as unreachable instead of panicking.
type unavailableFactory struct{}

func (unavailableFactory) For(context.Context, string, string) (Remote, error) {
	return nil, ErrSessionUnavailable
}
