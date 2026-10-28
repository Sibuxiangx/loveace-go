// Package gate implements the server-side admission checks that run before
// any school-bound credential is accepted. See server/DESIGN.md §5.1.
//
// The school gateway does not accept full-width characters and rejects
// passwords containing them as credential errors, burning the gateway's
// per-IP failure budget (3 failures → CAPTCHA). Every bad-format password
// must therefore be stopped locally, before any network request and without
// counting as a failure.
package gate

import (
	"strings"
	"unicode"
)

// Category classifies one password-format violation. The values are part of
// the API contract (see §5.1) and stable.
type Category string

// Violation categories.
const (
	CatFullwidthSymbol Category = "fullwidth_symbol"
	CatFullwidthAlnum  Category = "fullwidth_alnum"
	CatNonASCII        Category = "non_ascii"
	CatWhitespaceEdge  Category = "whitespace_edge"
	CatControl         Category = "control"
	CatZeroWidth       Category = "zero_width"
	CatTooLong         Category = "too_long"
	CatEmpty           Category = "empty"
)

// Violation points at one offending rune. Index is the rune offset in the
// submitted string. The value itself is never echoed back.
type Violation struct {
	Index    int      `json:"index"`
	Category Category `json:"category"`
}

// Result is the outcome of the local pre-validation.
type Result struct {
	OK         bool        `json:"ok"`
	Violations []Violation `json:"violations,omitempty"`
	Hint       string      `json:"hint,omitempty"`
}

// MaxPasswordLength mirrors the school-side cap; the exact limit is not
// documented anywhere, so 64 is a conservative default (§12 E5 will pin it).
const MaxPasswordLength = 64

// ValidatePasswordFormat runs the local pre-validation on a submitted
// password (or gateway password). It never touches the network and never
// records the value.
func ValidatePasswordFormat(pw string, maxLen int) Result {
	if len(pw) == 0 {
		return Result{OK: false, Violations: []Violation{{Index: 0, Category: CatEmpty}},
			Hint: "密码不能为空"}
	}

	runes := []rune(pw)
	var violations []Violation
	appendviolation := func(i int, c Category) {
		violations = append(violations, Violation{Index: i, Category: c})
	}

	last := len(runes) - 1
	for i, r := range runes {
		switch {
		case isZeroWidth(r):
			appendviolation(i, CatZeroWidth)
		case unicode.IsControl(r):
			appendviolation(i, CatControl)
		case (i == 0 || i == last) && unicode.IsSpace(r):
			appendviolation(i, CatWhitespaceEdge)
		case isFullwidthAlnum(r):
			appendviolation(i, CatFullwidthAlnum)
		case isFullwidthSymbol(r):
			appendviolation(i, CatFullwidthSymbol)
		case r > 0x7F:
			appendviolation(i, CatNonASCII)
		}
	}

	if len(runes) > maxLen {
		appendviolation(maxLen, CatTooLong)
	}

	if len(violations) == 0 {
		return Result{OK: true}
	}
	return Result{OK: false, Violations: violations, Hint: hintFor(violations)}
}

// MaskedAccount renders an account id for logs and API views: short ids
// reveal only the first and last character, long ones the first and last
// two. Used by the server, never by the core.
func MaskedAccount(account string) string {
	runes := []rune(account)
	switch {
	case len(runes) <= 2:
		return "***"
	case len(runes) <= 4:
		return string(runes[:1]) + "***" + string(runes[len(runes)-1:])
	default:
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
}

func isZeroWidth(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || r == 0x2060 || r == 0xFEFF
}

func isFullwidthAlnum(r rune) bool {
	return (r >= '０' && r <= '９') || // FF10-FF19
		(r >= 'Ａ' && r <= 'Ｚ') || // FF21-FF3A
		(r >= 'ａ' && r <= 'ｚ') // FF41-FF5A
}

func isFullwidthSymbol(r rune) bool {
	// Remainder of the Halfwidth and Fullwidth Forms block plus the
	// ideographic space (U+3000), which school forms also reject.
	return (r >= 0xFF01 && r <= 0xFF60) || r == 0x3000
}

func hintFor(violations []Violation) string {
	var hints []string
	seen := map[Category]bool{}
	for _, v := range violations {
		if seen[v.Category] {
			continue
		}
		seen[v.Category] = true
		switch v.Category {
		case CatFullwidthSymbol, CatFullwidthAlnum:
			hints = append(hints, "包含全角字符，请切换到英文半角输入")
		case CatNonASCII:
			hints = append(hints, "包含中文或非英文字符")
		case CatWhitespaceEdge:
			hints = append(hints, "首尾含有空白（粘贴时常带入），请删除")
		case CatControl:
			hints = append(hints, "包含换行或控制字符")
		case CatZeroWidth:
			hints = append(hints, "包含不可见的零宽字符，请手动重新输入")
		case CatTooLong:
			hints = append(hints, "超出长度上限")
		case CatEmpty:
			hints = append(hints, "密码不能为空")
		}
	}
	return strings.Join(hints, "；")
}
