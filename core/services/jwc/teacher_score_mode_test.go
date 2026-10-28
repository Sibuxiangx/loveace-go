package jwc

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// scoreModePage has two radio questions: q1 has full-weight (1.0) options,
// q2 has none, so its top group is 0.6.
const scoreModePage = `
        <html><body>
          <input name="tokenValue" value="FORMTOKEN">
          <input name="wjdm" value="Q1"><input name="bprdm" value="P1"><input name="pgnr" value="E1">
          <table><tr><td>授课态度是否认真</td><td>
            <input type="radio" name="q1" value="5_1.0"><label>很认真</label>
            <input type="radio" name="q1" value="4_1.0"><label>认真</label>
            <input type="radio" name="q1" value="3_0.6"><label>一般</label>
          </td></tr></table>
          <table><tr><td>课堂互动是否充分</td><td>
            <input type="radio" name="q2" value="3_0.6"><label>一般</label>
            <input type="radio" name="q2" value="2_0.4"><label>较差</label>
          </td></tr></table>
          <textarea name="zgpj"></textarea>
        </body></html>`

func scoreModeService(t *testing.T) *Service {
	t.Helper()
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Post, "teachingEvaluation/evaluationPage", transport.NewResponse(200, []byte(scoreModePage)))
	return New(session.New("20000002", "gw", "school", tr))
}

func scoreModeCourse() *TeacherEvaluationCourse {
	return &TeacherEvaluationCourse{CoureSequenceNumber: "S1", EvaluationContentNumber: "E1"}
}

// optionWeight parses the weight encoded in a "<score>_<weight>" option value.
func optionWeight(t *testing.T, value string) float64 {
	t.Helper()
	i := strings.LastIndex(value, "_")
	w, err := strconv.ParseFloat(value[i+1:], 64)
	if err != nil {
		t.Fatalf("bad option value %q: %v", value, err)
	}
	return w
}

// radioMaxWeight returns the highest weight among a question's options.
func radioMaxWeight(t *testing.T, values ...string) float64 {
	t.Helper()
	max := optionWeight(t, values[0])
	for _, v := range values[1:] {
		if w := optionWeight(t, v); w > max {
			max = w
		}
	}
	return max
}

func TestTeacherEvalMaxModeChoosesTopWeightForEveryRadio(t *testing.T) {
	questionOptions := map[string][]string{
		"q1": {"5_1.0", "4_1.0", "3_0.6"},
		"q2": {"3_0.6", "2_0.4"},
	}
	for seed := uint64(1); seed <= 25; seed++ {
		prepared, err := scoreModeService(t).TeacherEvalPrepareWithMode(
			context.Background(), scoreModeCourse(), 1, "TOK", seed, TeacherEvalScoreMax)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		answered := map[string]string{}
		for _, kv := range prepared.FormData {
			if _, ok := questionOptions[kv[0]]; ok {
				answered[kv[0]] = kv[1]
			}
		}
		if len(answered) != len(questionOptions) {
			t.Fatalf("seed %d: answered = %v", seed, answered)
		}
		for key, options := range questionOptions {
			chosen := answered[key]
			if optionWeight(t, chosen) != radioMaxWeight(t, options...) {
				t.Fatalf("seed %d: %s chose %q, not a top-weight option", seed, key, chosen)
			}
		}
	}
}

func TestTeacherEvalRandomModeIsUnchangedForSeed7(t *testing.T) {
	legacy, err := scoreModeService(t).TeacherEvalPrepareSeeded(context.Background(), scoreModeCourse(), 1, "TOK", 7)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := scoreModeService(t).TeacherEvalPrepareWithMode(
		context.Background(), scoreModeCourse(), 1, "TOK", 7, TeacherEvalScoreRandom)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := scoreModeService(t).TeacherEvalPrepareWithMode(
		context.Background(), scoreModeCourse(), 1, "TOK", 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.FormData) == 0 {
		t.Fatal("form data empty")
	}
	if !reflect.DeepEqual(legacy.FormData, explicit.FormData) {
		t.Fatalf("random mode differs from TeacherEvalPrepareSeeded:\n%v\n%v", legacy.FormData, explicit.FormData)
	}
	if !reflect.DeepEqual(legacy.FormData, empty.FormData) {
		t.Fatalf("empty mode differs from random:\n%v\n%v", legacy.FormData, empty.FormData)
	}
}

func TestTeacherEvalUnknownScoreModeIsInvalidArgument(t *testing.T) {
	_, err := scoreModeService(t).TeacherEvalPrepareWithMode(
		context.Background(), scoreModeCourse(), 1, "TOK", 7, TeacherEvalScoreMode("bogus"))
	if errs.CodeOf(err) != errs.InvalidArgument {
		t.Fatalf("err = %v, want invalid argument", err)
	}
}
