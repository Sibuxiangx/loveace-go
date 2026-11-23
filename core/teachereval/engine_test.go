package teachereval

import (
	"errors"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

func TestClassifyErrorEndsTheBatchOnlyForSchoolSessionFailures(t *testing.T) {
	plain := errors.New("boom")
	if got := ClassifyError(plain); got != plain {
		t.Fatalf("a course-level failure must pass through, got %v", got)
	}
	if got := ClassifyError(errs.NewAuthRequiredSimple()); !errors.Is(got, ErrSessionUnavailable) {
		t.Fatalf("an expired login must end the batch as a session failure, got %v", got)
	}
	if got := ClassifyError(ErrCaptchaRequired); !errors.Is(got, ErrCaptchaRequired) {
		t.Fatalf("a sentinel must stay itself, got %v", got)
	}
}

func TestPendingRunsSkipsEvaluatedAndDuplicateCourses(t *testing.T) {
	runs := pendingRuns([]jwc.TeacherEvaluationCourse{
		{Name: "done", IsEvaluated: true},
		{Name: "同名课"},
		{Name: "同名课"},
	})
	if len(runs) != 1 || runs[0].key != "同名课" {
		t.Fatalf("pendingRuns = %+v, want the one unevaluated course", runs)
	}
}

func TestCourseKeyFallsBackToPosition(t *testing.T) {
	if got := CourseKey(&jwc.TeacherEvaluationCourse{}, 3); got != "row-3" {
		t.Fatalf("CourseKey of an unidentifiable course = %q, want row-3", got)
	}
}
