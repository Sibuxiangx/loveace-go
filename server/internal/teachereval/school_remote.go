package teachereval

import (
	"context"
	"errors"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/core/session"
	tecore "github.com/Sibuxiangx/loveace-go/core/teachereval"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
)

// SchoolFactory binds the engine to live school sessions.
type SchoolFactory struct {
	School *school.Manager
}

// For resolves the user's session for one step. SessionFor refreshes the user's activity, so
// long-running tasks are not cooled out from under themselves, and it falls back to the sealed
// snapshot or a vault relogin.
func (f SchoolFactory) For(ctx context.Context, userID, deviceID string) (tecore.Remote, error) {
	sess, err := f.School.SessionFor(ctx, userID, deviceID)
	if err != nil {
		if errors.Is(err, school.ErrNoSession) {
			return nil, tecore.ErrSessionUnavailable
		}
		return nil, err
	}
	return &schoolRemote{
		inner:  tecore.NewJWCRemote(jwc.New(sess)),
		mgr:    f.School,
		userID: userID,
		sess:   sess,
	}, nil
}

// schoolRemote is one step's school side. Each failure goes to the manager, as the ops path does, so
// an expired session is dropped and the next step recovers it.
type schoolRemote struct {
	inner  tecore.Remote
	mgr    *school.Manager
	userID string
	sess   *session.Session
}

func (r *schoolRemote) Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error) {
	idx, err := r.inner.Index(ctx)
	return idx, r.observe(err)
}

func (r *schoolRemote) Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	list, err := r.inner.Courses(ctx)
	return list, r.observe(err)
}

func (r *schoolRemote) Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error) {
	form, err := r.inner.Prepare(ctx, c, pending, token, mode)
	return form, r.observe(err)
}

func (r *schoolRemote) Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	res, err := r.inner.Submit(ctx, p)
	return res, r.observe(err)
}

func (r *schoolRemote) Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	done, err := r.inner.Verify(ctx, c)
	return done, r.observe(err)
}

// observe reports a failure to the manager, and maps a gateway challenge onto the engine's captcha
// sentinel. Other errors go to the engine unchanged, which classifies them.
func (r *schoolRemote) observe(err error) error {
	if err == nil {
		return nil
	}
	r.mgr.MarkAuthFailed(r.userID, r.sess, err)
	var ce *school.ChallengeError
	if errors.As(err, &ce) {
		return tecore.ErrCaptchaRequired
	}
	if errors.Is(err, school.ErrNoSession) {
		return tecore.ErrSessionUnavailable
	}
	return err
}
