// Package diagnostics provides redaction, structured diagnostics and
// Diagnostic Report v1 assembly.
//
// Ports NetworkLogRedactor from the iOS client and keeps verbose HTTP logs
// off by default. All emitted channels redact passwords, cookies,
// authorization, tickets, tokens, CSRF, TWFID and JSESSIONID — including
// query parameters and HTML/JSON fields. Reports are assembled locally and
// previewable; no report endpoint is invented.
package diagnostics

import (
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/timeutil"
)

// CoreVersion is embedded in diagnostics and telemetry.
const CoreVersion = "0.1.0"

// Redactor provides redaction helpers (allowlisted summaries where body
// redaction is unsure).
type Redactor struct{}

var sensitiveNames = []string{
	"authorization", "cookie", "setcookie", "password", "passwd", "token",
	"ticket", "csrf", "secret", "credential", "jessionid", "jsessionid",
	"twfid", "username", "session", "svpnname", "svpnpassword",
}

// IsSensitive reports whether a header/field name is sensitive.
func (Redactor) IsSensitive(name string) bool {
	lower := strings.ReplaceAll(strings.ToLower(name), "-", "")
	switch lower {
	case "lt", "execution", "rand":
		return true
	}
	for _, n := range sensitiveNames {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

var hasScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

// URL redacts sensitive query parameters and keeps only scheme/host/path.
func (r Redactor) URL(rawURL string) string {
	if rawURL == "" {
		return "unknown"
	}
	if !hasScheme.MatchString(rawURL) {
		return rawURL
	}
	rest := rawURL[len(""):]

	// scheme://
	schemeEnd := strings.Index(rest, "://")
	scheme := rest[:schemeEnd]
	afterScheme := rest[schemeEnd+3:]

	// authority is up to the first '/', '?' or '#'.
	authority := afterScheme
	pathAndMore := ""
	if i := strings.IndexAny(afterScheme, "/?#"); i >= 0 {
		authority = afterScheme[:i]
		pathAndMore = afterScheme[i:]
	}
	host := authority
	port := ""
	if i := strings.LastIndex(authority, ":"); i >= 0 && !strings.Contains(authority[i:], "]") {
		host = authority[:i]
		port = authority[i:]
	}
	path := pathAndMore
	query := ""
	if i := strings.IndexAny(pathAndMore, "?#"); i >= 0 {
		path = pathAndMore[:i]
		rest2 := pathAndMore[i:]
		if strings.HasPrefix(rest2, "?") {
			end := strings.IndexByte(rest2, '#')
			if end < 0 {
				query = rest2[1:]
			} else {
				query = rest2[1:end]
			}
		}
	}

	var result strings.Builder
	result.WriteString(scheme)
	result.WriteString("://")
	result.WriteString(host)
	result.WriteString(port)
	result.WriteString(path)
	if query != "" {
		result.WriteByte('?')
		for i, pair := range strings.Split(query, "&") {
			if i > 0 {
				result.WriteByte('&')
			}
			k, v, _ := strings.Cut(pair, "=")
			if r.IsSensitive(k) {
				result.WriteString(k)
				result.WriteString("=<redacted>")
			} else {
				result.WriteString(k)
				result.WriteByte('=')
				result.WriteString(v)
			}
		}
	}
	return result.String()
}

// Headers redacts header values by name.
func (r Redactor) Headers(headers [][2]string) [][2]string {
	out := make([][2]string, len(headers))
	for i, kv := range headers {
		switch {
		case r.IsSensitive(kv[0]):
			out[i] = [2]string{kv[0], "<redacted>"}
		case strings.EqualFold(kv[0], "location"):
			out[i] = [2]string{kv[0], r.URL(kv[1])}
		default:
			out[i] = [2]string{kv[0], r.Text(kv[1])}
		}
	}
	return out
}

var (
	reXMLAuth = regexp.MustCompile(`(?is)(<(?:csrf_rand_code|twfid|rsa_encrypt_key|rsa_encrypt_exp)[^>]*>)(.*?)(</(?:csrf_rand_code|twfid|rsa_encrypt_key|rsa_encrypt_exp)>)`)
	reKV      = regexp.MustCompile(`(?i)(password|passwd|token|ticket|csrf|secret|authorization|cookie|set-cookie|jessionid|jsessionid|twfid|lt|execution)(\s*[=:]\s*)([^&\s,;<>"']+)`)
	reJSON    = regexp.MustCompile(`(?i)("?(?:password|passwd|token|ticket|csrf|secret|jessionid|jsessionid|twfid|lt|execution)"?\s*:\s*"?)([^",}\s]+)`)
)

// Text redacts known secret-bearing fields inside free text.
func (Redactor) Text(text string) string {
	result := reXMLAuth.ReplaceAllString(text, "${1}<redacted>${3}")
	result = reKV.ReplaceAllString(result, "${1}${2}<redacted>")
	result = reJSON.ReplaceAllString(result, "${1}<redacted>")
	return result
}

// DiagnosticEvent is a single sanitized diagnostic event.
type DiagnosticEvent struct {
	Event             string   `json:"event"`
	Operation         string   `json:"operation"`
	RequestID         *string  `json:"request_id"`
	AuthAttemptID     *string  `json:"auth_attempt_id"`
	SessionGeneration uint64   `json:"session_generation"`
	Phase             string   `json:"phase"`
	ElapsedMS         int64    `json:"elapsed_ms"`
	RetryCount        int      `json:"retry_count"`
	Outcome           string   `json:"outcome"`
	ErrorCode         *string  `json:"error_code"`
	DetectorSignals   []string `json:"detector_signals"`
	Synthetic         bool     `json:"synthetic"`
	Timestamp         float64  `json:"timestamp"`
}

// ReportManifest is the Diagnostic Report v1 manifest.
type ReportManifest struct {
	ReportID            string   `json:"report_id"`
	CoreVersion         string   `json:"core_version"`
	SchemaVersion       int      `json:"schema_version"`
	IncidentFingerprint string   `json:"incident_fingerprint"`
	CreatedAt           string   `json:"created_at"`
	Attachments         []string `json:"attachments"`
}

// DiagnosticReport is the assembled Diagnostic Report v1.
type DiagnosticReport struct {
	Manifest    ReportManifest    `json:"manifest"`
	Summary     string            `json:"summary"`
	Timeline    []DiagnosticEvent `json:"timeline"`
	Environment map[string]string `json:"environment"`
}

// Options are the diagnostic runtime options.
type Options struct {
	// VerboseHTTP enables verbose HTTP logging (default OFF).
	VerboseHTTP bool
	// MaxBufferBytes is the bounded in-memory buffer size (default 20 MiB).
	MaxBufferBytes int
}

// DefaultOptions mirrors the Rust DiagnosticOptions::default.
func DefaultOptions() Options {
	return Options{VerboseHTTP: false, MaxBufferBytes: 20 * 1024 * 1024}
}

// Collector is a bounded diagnostic collector with rotation by byte budget.
type Collector struct {
	options Options

	mu     sync.Mutex
	buffer []DiagnosticEvent
	bytes  int
}

// NewCollector creates a collector.
func NewCollector(options Options) *Collector {
	if options.MaxBufferBytes <= 0 {
		options.MaxBufferBytes = 20 * 1024 * 1024
	}
	return &Collector{options: options}
}

// VerboseHTTP reports whether verbose HTTP logging is enabled.
func (c *Collector) VerboseHTTP() bool { return c.options.VerboseHTTP }

// Record stores an event, rotating oldest entries out of the byte budget.
func (c *Collector) Record(event DiagnosticEvent) {
	approx := 64 + len(event.Operation) + len(event.Phase) + len(event.Outcome) +
		4*len(event.DetectorSignals)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buffer = append(c.buffer, event)
	c.bytes += approx
	for c.bytes > c.options.MaxBufferBytes && len(c.buffer) > 0 {
		front := c.buffer[0]
		c.buffer = c.buffer[1:]
		c.bytes -= 64 + len(front.Operation) + len(front.Phase) + len(front.Outcome) +
			4*len(front.DetectorSignals)
		if c.bytes < 0 {
			c.bytes = 0
		}
	}
}

// Len returns the number of buffered events.
func (c *Collector) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buffer)
}

// BuildReport builds a report from the buffered timeline.
func (c *Collector) BuildReport(reportID, incidentFingerprint, summary string) DiagnosticReport {
	c.mu.Lock()
	timeline := make([]DiagnosticEvent, len(c.buffer))
	copy(timeline, c.buffer)
	c.mu.Unlock()

	environment := map[string]string{
		"core_version": CoreVersion,
		"os":           goos,
		"arch":         goarch,
	}
	return DiagnosticReport{
		Manifest: ReportManifest{
			ReportID:            reportID,
			CoreVersion:         CoreVersion,
			SchemaVersion:       1,
			IncidentFingerprint: incidentFingerprint,
			CreatedAt:           timeutil.RFC3339Millis(time.Now()),
			Attachments:         []string{},
		},
		Summary:     summary,
		Timeline:    timeline,
		Environment: environment,
	}
}

// IncidentFingerprint builds a stable incident fingerprint (no account or
// body content).
func IncidentFingerprint(service, phase, errorCode, detector, parser string) string {
	return service + "/" + phase + "/" + errorCode + "/" + detector + "/" + parser
}

// goos/goarch describe the build platform (resolved once at init).
var (
	goos   = runtime.GOOS
	goarch = runtime.GOARCH
)

// SortedKeys returns map keys sorted (deterministic JSON output).
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
