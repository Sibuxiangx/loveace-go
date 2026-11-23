package teachereval

import (
	"context"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

// NewJWCRemote runs a batch's school steps through loveace-go's JWC service for one session. Errors
// are returned as the school gave them; the engine classifies them with ClassifyError.
func NewJWCRemote(svc *jwc.Service) Remote {
	return jwcRemote{svc: svc}
}

type jwcRemote struct {
	svc *jwc.Service
}

func (r jwcRemote) Index(ctx context.Context) (*jwc.TeacherEvaluationIndexStatus, error) {
	return r.svc.TeacherEvalIndex(ctx)
}

func (r jwcRemote) Courses(ctx context.Context) (*jwc.TeacherEvaluationCourseList, error) {
	return r.svc.TeacherEvalCourses(ctx)
}

func (r jwcRemote) Prepare(ctx context.Context, c *jwc.TeacherEvaluationCourse, pending int64, token string, mode jwc.TeacherEvalScoreMode) (*jwc.TeacherEvaluationPreparedForm, error) {
	// Each form gets its own seed, so two courses never receive the same answers.
	return r.svc.TeacherEvalPrepareWithMode(ctx, c, pending, token, uint64(time.Now().UnixNano()), mode)
}

func (r jwcRemote) Submit(ctx context.Context, p *jwc.TeacherEvaluationPreparedForm) (*jwc.TeacherEvaluationSubmitResult, error) {
	return r.svc.TeacherEvalSubmit(ctx, p)
}

func (r jwcRemote) Verify(ctx context.Context, c *jwc.TeacherEvaluationCourse) (bool, error) {
	return r.svc.TeacherEvalVerify(ctx, c)
}
