package jwc

import "testing"

// TestWeekMatches pins the class_week matcher used by the merged
// timetable: ranges, comma lists, single weeks, and 单/双 parity.
func TestWeekMatches(t *testing.T) {
	cases := []struct {
		spec  string
		week  int
		match bool
	}{
		{"1-16周", 1, true},
		{"1-16周", 16, true},
		{"1-16周", 17, false},
		{"1-16周", 0, false},
		{"5-8(双)周", 6, true},
		{"5-8(双)周", 7, false},
		{"1,3,5周", 3, true},
		{"1,3,5周", 4, false},
		{"1-8,10-16周", 9, false},
		{"1-8,10-16周", 10, true},
		// Per-part parity: (单) binds to its own range only.
		{"1-8(单),10-16周", 7, true},
		{"1-8(单),10-16周", 8, false},
		{"1-8(单),10-16周", 9, false},
		{"1-8(单),10-16周", 11, true},
		{"1-8(单),10-16(双)周", 12, true},
		{"1-8(单),10-16(双)周", 13, false},
		// The studentSchedule endpoint's real 0/1 bitmask form
		// ("企业形象策划" 2026-2027-1: weeks 1-9).
		{"111111111000000000000000", 1, true},
		{"111111111000000000000000", 9, true},
		{"111111111000000000000000", 10, false},
		{"111111111000000000000000", 18, false},
		// 18 weeks total but a gap: 直播短视频 week 9 only.
		{"000000001000000000000000", 9, true},
		{"000000001000000000000000", 8, false},
		// Not a bitmask (parity/range chars) → falls to the range path.
		{"1-16周", 16, true},
		{"7", 7, true},
		{"", 3, false},
		{"乱码", 3, false},
	}
	for _, c := range cases {
		if got := WeekMatches(c.spec, c.week); got != c.match {
			t.Fatalf("WeekMatches(%q, %d) = %v, want %v", c.spec, c.week, got, c.match)
		}
	}
}
