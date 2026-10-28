package teachereval

import (
	"context"
	"errors"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
)

// SchoolFactory binds the engine to live school sessions.
type SchoolFactory struct {
	School *school.Manager
}

// For resolves the user's session for one step. SessionFor refreshes the
// user's activity, so long-running tasks are not cooled out from under
// themselves, and it falls back to the sealed snapshot or a vault relogin.
func (f SchoolFactory) For(ctx context.Context, userID, deviceID string) (Remote, error) {
	sess, err := f.School.SessionFor(ctx, userID, deviceID)
	if err != nil {
		if errors.Is(err, school.ErrNoSession) {
			return nil, ErrSessionUnavailable
		}
		return nil, err
	}
	return &schoolRemote{mgr: f.School, userID: userID, sess: sess, svc: jwc.New(sess)}, nil
}

type schoolRemote struct {
	mgr    *school.Manager
	userID string
	sess   *session.Session
	svc    *jwc.Service
}

func (r *schoolRemote) Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error) {
	idx, err := r.svc.TeacherEvalIndex(ctx)
	return idx, r.classify(err)
}

func (r *schoolRemote) Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	list, err := r.svc.TeacherEvalCourses(ctx)
	return list, r.classify(err)
}

func (r *schoolRemote) Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error) {
	form, err := r.svc.TeacherEvalPrepareWithMode(ctx, c, pending, token, uint64(time.Now().UnixNano()), mode)
	return form, r.classify(err)
}

func (r *schoolRemote) Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	res, err := r.svc.TeacherEvalSubmit(ctx, p)
	return res, r.classify(err)
}

func (r *schoolRemote) Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	done, err := r.svc.TeacherEvalVerify(ctx, c)
	return done, r.classify(err)
}

// classify maps school failures onto the engine's sentinels. Every failure
// also goes to the manager, as the ops path does, so an expired session is
// dropped and the next step recovers it.
func (r *schoolRemote) classify(err error) error {
	if err == nil {
		return nil
	}
	r.mgr.MarkAuthFailed(r.userID, r.sess, err)
	var ce *school.ChallengeError
	if errors.As(err, &ce) || errs.CodeOf(err) == errs.InteractionRequired {
		return ErrCaptchaRequired
	}
	if errors.Is(err, school.ErrNoSession) || errs.CodeOf(err) == errs.AuthRequired {
		return ErrSessionUnavailable
	}
	return err
}
