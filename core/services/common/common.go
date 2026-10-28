// Package common ports services/mod.rs helpers: response guards, HTML
// cleaning, JSON accessors and CAS ticket extraction.
package common

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// RequireSuccess rejects a non-200 response before attempting JSON/envelope
// parsing. School gateways return HTML (often a login page) with a non-200
// status when a request is malformed or unauthenticated; surfacing http_error
// with the status is far more diagnosable than feeding that HTML into a JSON
// parser.
func RequireSuccess(response *transport.Response) error {
	if response.Status != 200 {
		return errs.NewHTTP(response.Status, "unexpected non-200 response")
	}
	return nil
}

var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	nbspRe    = regexp.MustCompile(`&nbsp;`)
	ampRe     = regexp.MustCompile(`&amp;`)
	ltRe      = regexp.MustCompile(`&lt;`)
	gtRe      = regexp.MustCompile(`&gt;`)
	quotRe    = regexp.MustCompile(`&quot;`)
	apos39Re  = regexp.MustCompile(`&#39;`)
	aposLitRe = regexp.MustCompile(`&apos;`)
)

// CleanHTMLText removes HTML tags and decodes common entities, then collapses
// whitespace.
func CleanHTMLText(input string) string {
	out := StripTags(input)
	out = UnescapeEntities(out)
	return strings.TrimSpace(out)
}

// StripTags removes HTML tags.
func StripTags(input string) string {
	return tagRe.ReplaceAllString(input, "")
}

// UnescapeEntities decodes the small set of entities the school pages use.
func UnescapeEntities(input string) string {
	out := nbspRe.ReplaceAllString(input, " ")
	out = ampRe.ReplaceAllString(out, "&")
	out = ltRe.ReplaceAllString(out, "<")
	out = gtRe.ReplaceAllString(out, ">")
	out = quotRe.ReplaceAllString(out, "\"")
	out = apos39Re.ReplaceAllString(out, "'")
	out = aposLitRe.ReplaceAllString(out, "'")
	return out
}

// ParseJSON parses a response body as JSON, mapping failures to Parse.
func ParseJSON(body string) (json.RawMessage, error) {
	if !json.Valid([]byte(body)) {
		return nil, errs.NewParse("JSON parse failed: invalid JSON")
	}
	return json.RawMessage(body), nil
}

// Envelope parses the {code, data, msg} envelope used by AAC/labor APIs.
func Envelope(body string) (code int64, data json.RawMessage, err error) {
	var root struct {
		Code *int64          `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return -1, nil, errs.NewParse("JSON parse failed: " + err.Error())
	}
	code = -1
	if root.Code != nil {
		code = *root.Code
	}
	if root.Data == nil {
		data = json.RawMessage("null")
	} else {
		data = root.Data
	}
	return code, data, nil
}

// JSONString is a string field accessor with null/missing tolerance.
func JSONString(value json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	trimmed := strings.TrimSpace(string(raw))
	trimmed = strings.TrimPrefix(trimmed, "\"")
	trimmed = strings.TrimSuffix(trimmed, "\"")
	return trimmed
}

// JSONOptString is an optional string field accessor.
func JSONOptString(value json.RawMessage, key string) *string {
	s := JSONString(value, key)
	if s == "" {
		return nil
	}
	return &s
}

// JSONI64 is an integer field accessor with tolerance for numeric strings and
// floats.
func JSONI64(value json.RawMessage, key string) int64 {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return 0
	}
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if n, err := parseI64(s); err == nil {
			return n
		}
		if f, err := parseF64(s); err == nil {
			return int64(f)
		}
	}
	return 0
}

// JSONF64 is a float field accessor with tolerance for numeric strings.
func JSONF64(value json.RawMessage, key string) float64 {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return 0
	}
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := parseF64(strings.TrimSpace(s)); err == nil {
			return f
		}
	}
	return 0
}

// JSONBool is a boolean field accessor accepting 0/1/true/false.
func JSONBool(value json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return false
	}
	raw, ok := m[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n != 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "1" || strings.EqualFold(s, "true")
	}
	return false
}

// RawField returns the raw JSON value for a key, or nil.
func RawField(value json.RawMessage, key string) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return nil
	}
	return m[key]
}

// ArrayItems decodes a JSON array into raw items; non-arrays yield nil.
func ArrayItems(value json.RawMessage) []json.RawMessage {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return nil
	}
	return items
}

// ExtractRecords locates the first non-empty records array in a service
// response.
func ExtractRecords(value json.RawMessage) []json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return nil
	}
	if records, ok := m["records"]; ok {
		return ArrayItems(records)
	}
	if list, ok := m["list"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(list, &nested); err == nil {
			if records, ok := nested["records"]; ok {
				return ArrayItems(records)
			}
		}
	}
	return nil
}

var amountRe = regexp.MustCompile(`[^0-9.\-]`)

// ParseAmount parses a decimal amount, stripping currency symbols and separators.
func ParseAmount(input string) *float64 {
	cleaned := amountRe.ReplaceAllString(input, "")
	if cleaned == "" || cleaned == "-" || cleaned == "." {
		return nil
	}
	f, err := parseF64(cleaned)
	if err != nil {
		return nil
	}
	return &f
}

var ticketStart = regexp.MustCompile(`ticket=`)

// ExtractTicketFromURL extracts a CAS ticket value from a callback URL
// containing ticket=. Matches the reference _extractTicket: the value stops
// at &/# and is percent-decoded.
func ExtractTicketFromURL(rawURL string) (string, bool) {
	idx := strings.Index(rawURL, "ticket=")
	if idx < 0 {
		return "", false
	}
	tail := rawURL[idx+len("ticket="):]
	if end := strings.IndexAny(tail, "&#"); end >= 0 {
		tail = tail[:end]
	}
	if tail == "" {
		return "", false
	}
	return PercentDecode(tail), true
}

// PercentDecode percent-decodes a URL component (+ left literal, as the
// source does).
func PercentDecode(input string) string {
	if !strings.Contains(input, "%") {
		return input
	}
	var out []byte
	for i := 0; i < len(input); i++ {
		if input[i] == '%' && i+2 < len(input) {
			if v, ok := parseHexByte(input[i+1], input[i+2]); ok {
				out = append(out, v)
				i += 2
				continue
			}
		}
		out = append(out, input[i])
	}
	return string(out)
}

func parseHexByte(hi, lo byte) (byte, bool) {
	h, ok1 := hexVal(hi)
	l, ok2 := hexVal(lo)
	if !ok1 || !ok2 {
		return 0, false
	}
	return h<<4 | l, true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// SimpleRng is a deterministic small RNG (xorshift64*) used for reproducible
// teacher-eval answer selection without pulling in a heavyweight RNG.
type SimpleRng struct{ state uint64 }

// NewSimpleRng seeds the RNG. A zero seed is remapped to a non-zero constant.
func NewSimpleRng(seed uint64) *SimpleRng {
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15
	}
	return &SimpleRng{state: seed}
}

// NextU64 returns the next raw 64-bit value.
func (r *SimpleRng) NextU64() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545F4914F6CDD1D
}

// NextF64 returns a uniform float in [0, 1).
func (r *SimpleRng) NextF64() float64 {
	return float64(r.NextU64()>>11) / float64(uint64(1)<<53)
}

// Below returns a uniform index in [0, bound).
func (r *SimpleRng) Below(bound int) int {
	if bound <= 0 {
		return 0
	}
	return int(r.NextU64() % uint64(bound))
}

func parseI64(s string) (int64, error) {
	var v int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			if i == 0 && (s[i] == '-' || s[i] == '+') {
				continue
			}
			return 0, errBadNumber
		}
		v = v*10 + int64(s[i]-'0')
	}
	return v, nil
}

func parseF64(s string) (float64, error) {
	var f float64
	var frac float64
	var scale float64 = 1
	intPart := true
	neg := false
	start := 0
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		start = 1
	}
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if !intPart {
				return 0, errBadNumber
			}
			intPart = false
			continue
		}
		if c < '0' || c > '9' {
			return 0, errBadNumber
		}
		if intPart {
			f = f*10 + float64(c-'0')
		} else {
			scale *= 10
			frac = frac*10 + float64(c-'0')
		}
	}
	f += frac / scale
	if neg {
		f = -f
	}
	return f, nil
}

var errBadNumber = &parseError{}

type parseError struct{}

func (*parseError) Error() string { return "bad number" }

// HTMLEscapeUnescape re-exports html.UnescapeString for callers needing
// broader entity coverage than UnescapeEntities.
func HTMLEscapeUnescape(s string) string { return html.UnescapeString(s) }
