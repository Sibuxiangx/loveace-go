// Package timeutil provides time helpers.
//
// The school operates in the Asia/Shanghai timezone; all "today"/term date
// computations use a fixed +08:00 offset so results do not depend on the host
// timezone or locale.
package timeutil

import "time"

// Shanghai is the fixed Asia/Shanghai location (+08:00).
var Shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

// NowShanghai returns the current instant in Asia/Shanghai.
func NowShanghai() time.Time { return time.Now().In(Shanghai) }

// FormatYMD renders a Shanghai date as YYYY-MM-DD.
func FormatYMD(date time.Time) string {
	t := date.In(Shanghai)
	return t.Format("2006-01-02")
}

// TodayYMD returns today's Shanghai date as YYYY-MM-DD.
func TodayYMD() string { return FormatYMD(time.Now()) }

// YMDHoursAgo returns the Shanghai date `days` in the past as YYYY-MM-DD.
func YMDHoursAgo(days int) string {
	return FormatYMD(time.Now().AddDate(0, 0, -days))
}

// NowMillis returns the millisecond Unix timestamp, used as a cache-busting
// request parameter.
func NowMillis() int64 { return time.Now().UnixMilli() }

// ChineseDateToStandard converts a Chinese date such as `2026年1月3日` to
// `2026-01-03`. Non-matching input is returned unchanged, matching the
// reference helper.
func ChineseDateToStandard(input string) string {
	var digits []byte
	var groups []string
	for i := 0; i < len(input); {
		r := rune(input[i])
		if r < 0x80 {
			if input[i] >= '0' && input[i] <= '9' {
				digits = append(digits, input[i])
			} else if len(digits) > 0 {
				groups = append(groups, string(digits))
				digits = digits[:0]
			}
			i++
			continue
		}
		// Multi-byte UTF-8: flush digits, skip the rune.
		if len(digits) > 0 {
			groups = append(groups, string(digits))
			digits = digits[:0]
		}
		_, size := decodeRune(input[i:])
		i += size
	}
	if len(digits) > 0 {
		groups = append(groups, string(digits))
	}
	if len(groups) >= 3 &&
		containsRune(input, '年') &&
		containsRune(input, '月') &&
		(containsRune(input, '日') || containsRune2(input, "号")) {
		year := groups[0]
		month := parseUintOr(groups[1], 1)
		day := parseUintOr(groups[2], 1)
		return year + "-" + pad2(month) + "-" + pad2(day)
	}
	return input
}

// ParseISO8601 parses an RFC3339 timestamp, tolerating an optional fractional part.
func ParseISO8601(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// RFC3339Millis formats an instant as RFC3339 with millisecond precision.
func RFC3339Millis(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// IsUnexpired reports whether expiresAt (ISO-8601) is still in the future
// relative to now. An absent value means "does not expire". An unparseable
// value is treated as expired, matching the iOS announcement filter.
func IsUnexpired(expiresAt *string) bool {
	if expiresAt == nil {
		return true
	}
	t, ok := ParseISO8601(*expiresAt)
	return ok && t.After(time.Now())
}

// ParseYMD parses `YYYY-MM-DD` in the Shanghai location.
func ParseYMD(value string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", value, Shanghai)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// --- small helpers (avoid pulling heavyweight deps) ---

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func containsRune2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func decodeRune(s string) (rune, int) {
	b := s[0]
	switch {
	case b&0x80 == 0:
		return rune(b), 1
	case b&0xE0 == 0xC0 && len(s) >= 2:
		return rune(b&0x1F)<<6 | rune(s[1]&0x3F), 2
	case b&0xF0 == 0xE0 && len(s) >= 3:
		return rune(b&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	case b&0xF8 == 0xF0 && len(s) >= 4:
		return rune(b&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	default:
		return rune(b), 1
	}
}

func parseUintOr(s string, fallback uint32) uint32 {
	var v uint32
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fallback
		}
		v = v*10 + uint32(s[i]-'0')
		if v > 9999 {
			return fallback
		}
	}
	if len(s) == 0 {
		return fallback
	}
	return v
}

func pad2(v uint32) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}
