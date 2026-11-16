package review

import "testing"

// Every free-classroom read has a synthetic answer, and none is writable.
func TestClassroomFixturesAnswerEveryReadAndNoWrite(t *testing.T) {
	ops := []string{
		"jwc.free_classroom_buildings", "jwc.free_classroom_meta", "jwc.free_classroom_search",
		"jwc.free_classroom_rooms", "jwc.free_classroom_occupancy",
	}
	for _, op := range ops {
		if out, ok := ReadOp(op); !ok || out == nil {
			t.Fatalf("%s has no fixture", op)
		}
		if _, ok := WriteOp(op); ok {
			t.Fatalf("%s must not be writable", op)
		}
	}
}
