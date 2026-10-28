package teachereval

import (
	"fmt"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
)

// CourseKey identifies a course across refreshes. It follows the clients'
// displayId: the four-part stable id when complete, otherwise the best
// available fallback. A course with no identifying field gets its row
// position so the key stays unique inside a task.
func CourseKey(c *jwc.TeacherEvaluationCourse, position int) string {
	if stable := stableID(c); stable != "" {
		return stable
	}
	if parts := nonEmpty(c.EvaluatedPeopleNumber, c.EvaluationContentNumber, c.QuestionnaireCode, c.Name, c.Teacher); len(parts) > 0 {
		return strings.Join(parts, "_")
	}
	return fmt.Sprintf("row-%d", position)
}

// stableID is the four-part identity the clients use when every part exists.
func stableID(c *jwc.TeacherEvaluationCourse) string {
	parts := nonEmpty(c.EvaluatedPeopleNumber, c.CoureSequenceNumber, c.EvaluationContentNumber, c.QuestionnaireCode)
	if len(parts) != 4 {
		return ""
	}
	return strings.Join(parts, "_")
}

// Matches mirrors the client rule. Two courses match on the stable id when
// both have one; otherwise the evaluated-people and content numbers must both
// be present and equal.
func Matches(a, b *jwc.TeacherEvaluationCourse) bool {
	if sa, sb := stableID(a), stableID(b); sa != "" && sb != "" {
		return sa == sb
	}
	return a.EvaluatedPeopleNumber != "" &&
		a.EvaluatedPeopleNumber == b.EvaluatedPeopleNumber &&
		a.EvaluationContentNumber != "" &&
		a.EvaluationContentNumber == b.EvaluationContentNumber
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// displayName is what users and logs see: course name and teacher.
func displayName(c *jwc.TeacherEvaluationCourse) string {
	name := strings.Join(nonEmpty(c.Name, c.Teacher), " / ")
	if name == "" {
		return CourseKey(c, 0)
	}
	return name
}
