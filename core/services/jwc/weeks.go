// Week matching for schedule class_week strings ("1-16周", "5-8(双)周",
// "1,3,5周"). The merged timetable view (server calendar × schedule)
// needs to decide "does this slot occur in week N".
package jwc

import "strings"

// WeekMatches reports whether classWeek covers week. Only explicit week
// evidence matches: an empty/absent spec never matches. Two real formats:
//   - "1-8(单),10-16周" — ranges/lists with optional per-segment parity;
//   - "111111111000000000000000" — a 0/1 digit string, one digit per week
//     (the studentSchedule endpoint's format): digit at index week-1.
func WeekMatches(classWeek string, week int) bool {
	text := strings.TrimSpace(classWeek)
	text = strings.TrimSuffix(text, "周")
	text = strings.ReplaceAll(text, " ", "")
	if text == "" || week <= 0 {
		return false
	}
	// 0/1 bitmask form.
	if bitmask := text; isWeekBitmask(bitmask) {
		return week <= len(bitmask) && bitmask[week-1] == '1'
	}
	for _, part := range strings.Split(text, ",") {
		parity := 0 // 1 = odd only, 2 = even only
		if strings.HasSuffix(part, "(单)") {
			parity, part = 1, strings.TrimSuffix(part, "(单)")
		} else if strings.HasSuffix(part, "(双)") {
			parity, part = 2, strings.TrimSuffix(part, "(双)")
		}
		lo, hi, ok := splitRange(part)
		if !ok || week < lo || week > hi {
			continue
		}
		if parity == 1 && week%2 == 0 {
			continue
		}
		if parity == 2 && week%2 == 1 {
			continue
		}
		return true
	}
	return false
}

// isWeekBitmask reports whether the spec is the 0/1 digit-string form.
func isWeekBitmask(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] != '0' && text[i] != '1' {
			return false
		}
	}
	return true
}

func splitRange(part string) (int, int, bool) {
	if part == "" {
		return 0, 0, false
	}
	if before, after, hasDash := strings.Cut(part, "-"); hasDash {
		lo, ok1 := atoi(before)
		hi, ok2 := atoi(after)
		if ok1 && ok2 && lo <= hi {
			return lo, hi, true
		}
		return 0, 0, false
	}
	n, ok := atoi(part)
	if !ok {
		return 0, 0, false
	}
	return n, n, true
}

func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
