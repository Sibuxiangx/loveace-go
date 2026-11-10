package review

import (
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/labor"
)

// Only the two canned writes are answered; every other write is refused.
func TestWriteAllowlistIsExactlyTheCannedOnes(t *testing.T) {
	if _, ok := WriteOp("labor.apply_activity"); !ok {
		t.Fatal("apply must keep its canned answer")
	}
	for _, op := range []string{"ykt.utility_pay", "labor.scan_sign_in", "jwc.teacher_eval_submit", "repair.submit"} {
		if _, ok := WriteOp(op); ok {
			t.Fatalf("%s must be refused", op)
		}
	}
}

// An operation without a fixture is not served, so gaps show up as 404s
// instead of quietly returning someone else's data.
func TestUnknownOpsAreNotServed(t *testing.T) {
	for _, op := range []string{"auth.login", "ykt.utility_pay", "labor.scan_sign_in"} {
		if _, ok := ReadOp(op); ok {
			t.Fatalf("%s served as a read", op)
		}
	}
}

// The club list must show one activity in each sign-up state right now, or the
// mini program's 可报名 path is never exercised by review.
func TestClubActivitiesCoverSignUpStates(t *testing.T) {
	out, ok := ReadOp("labor.club_activities")
	if !ok {
		t.Fatal("no club activities fixture")
	}
	now := time.Now().In(beijing)
	byID := map[string]labor.Activity{}
	for _, a := range out.([]labor.Activity) {
		byID[a.ID] = a
	}

	open := byID["review-open"]
	if !open.CanApply() || !inWindow(open, now) {
		t.Fatalf("open activity must be signable now: %+v", open)
	}
	if byID["review-full"].CanApply() {
		t.Fatal("full activity must not be applicable")
	}
	later := byID["review-later"]
	if later.SignUpStartTime == "" || !now.Before(parseStamp(t, later.SignUpStartTime)) {
		t.Fatalf("later activity must open in the future: %+v", later)
	}
}

func inWindow(a labor.Activity, now time.Time) bool {
	start := parseStampQuiet(a.SignUpStartTime)
	end := parseStampQuiet(a.SignUpEndTime)
	return !now.Before(start) && now.Before(end)
}

func parseStampQuiet(s string) time.Time {
	t, _ := time.ParseInLocation(stampLayout, s, beijing)
	return t
}

func parseStamp(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(stampLayout, s, beijing)
	if err != nil {
		t.Fatalf("stamp %q: %v", s, err)
	}
	return parsed
}
