// Package cookies implements the single authoritative cookie jar per account
// session.
//
// It matches the observable behaviour of HTTPClient._storeCookie /
// _getCookiesForRequest while following RFC 6265 domain/path/expiry/secure
// matching rules. The jar is shared by every transport mode of one session
// (normal, simple and no-redirect) so clearing cookies always affects all of
// them.
package cookies

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/timeutil"
)

// Cookie is a single stored cookie.
type Cookie struct {
	Name       string     `json:"name"`
	Value      string     `json:"value"`
	Domain     string     `json:"domain"`
	Path       string     `json:"path"`
	OriginHost string     `json:"origin_host"`
	Expires    *time.Time `json:"expires"`
	Secure     bool       `json:"secure"`
	HTTPOnly   bool       `json:"http_only"`
}

// IsExpired reports whether this cookie is expired at now.
func (c *Cookie) IsExpired(now time.Time) bool {
	return c.Expires != nil && !c.Expires.After(now)
}

// DomainMatches implements RFC 6265 domain match against a request host.
func (c *Cookie) DomainMatches(host string) bool {
	host = strings.ToLower(host)
	domain := strings.ToLower(c.Domain)
	if suffix, ok := strings.CutPrefix(domain, "."); ok {
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	return host == domain
}

// PathMatches implements RFC 6265 path match against a request path.
func (c *Cookie) PathMatches(requestPath string) bool {
	if c.Path == "" {
		return true
	}
	if requestPath == c.Path {
		return true
	}
	if !strings.HasPrefix(requestPath, c.Path) {
		return false
	}
	return strings.HasSuffix(c.Path, "/") ||
		strings.HasPrefix(requestPath[len(c.Path):], "/")
}

// Matches reports whether this cookie applies to a request.
func (c *Cookie) Matches(host, requestPath string, secureRequest bool, now time.Time) bool {
	if c.IsExpired(now) {
		return false
	}
	if c.Secure && !secureRequest {
		return false
	}
	return c.DomainMatches(host) && c.PathMatches(requestPath)
}

// Scope selects which cookie origin scope to clear.
type Scope int

// Cookie scopes.
const (
	// Gateway is the EasyConnect gateway origin (vpn2.aufe.edu.cn).
	Gateway Scope = iota
	// Identity is the identity provider (UAAP) origin.
	Identity
	// Service is school service origins.
	Service
	// All is every stored cookie.
	All
)

// Jar is the per-session cookie jar.
type Jar struct {
	mu      sync.Mutex
	cookies []*Cookie
}

// New creates an empty jar.
func New() *Jar { return &Jar{} }

// StoreSetCookie parses a Set-Cookie header and stores (or deletes) the cookie.
// requestHost/requestPath supply domain/path defaults when the header omits them.
func (j *Jar) StoreSetCookie(header, requestHost, requestPath string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.storeSetCookieLocked(header, requestHost, requestPath)
}

func (j *Jar) storeSetCookieLocked(header, requestHost, requestPath string) {
	parts := strings.Split(header, ";")
	nameValue := strings.TrimSpace(parts[0])
	name, value, ok := strings.Cut(nameValue, "=")
	if !ok {
		return
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" {
		return
	}

	var domain, path, expiresRaw, maxAgeRaw string
	secure, httpOnly := false, false
	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		key, raw, hasVal := strings.Cut(attr, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		raw = strings.TrimSpace(raw)
		switch key {
		case "domain":
			if hasVal {
				domain = strings.TrimPrefix(raw, ".")
			}
		case "path":
			if hasVal {
				path = raw
			}
		case "expires":
			if hasVal {
				expiresRaw = raw
			}
		case "max-age":
			if hasVal {
				maxAgeRaw = raw
			}
		case "secure":
			secure = true
		case "httponly":
			httpOnly = true
		}
	}

	now := time.Now()
	var expires *time.Time

	// Max-Age has precedence over Expires; non-positive means delete.
	if maxAgeRaw != "" {
		if age, ok := parseI64(maxAgeRaw); ok {
			if age <= 0 {
				j.removeLocked(name, domain, path)
				return
			}
			t := now.Add(time.Duration(age) * time.Second)
			expires = &t
		}
	} else if expiresRaw != "" {
		if t, ok := parseCookieDate(expiresRaw); ok {
			expires = &t
		}
	}

	domain = lowerOr(domain, defaultDomain(requestHost))
	if path == "" {
		path = defaultPath(requestPath)
	}

	// Replace an existing cookie with the same name/domain/path.
	kept := j.cookies[:0]
	for _, c := range j.cookies {
		if !(c.Name == name && c.Domain == domain && c.Path == path) {
			kept = append(kept, c)
		}
	}
	j.cookies = append(kept, &Cookie{
		Name:       name,
		Value:      value,
		Domain:     domain,
		Path:       path,
		OriginHost: strings.ToLower(requestHost),
		Expires:    expires,
		Secure:     secure,
		HTTPOnly:   httpOnly,
	})
	j.pruneExpiredLocked(now)
}

// Set inserts a cookie explicitly (mirrors HTTPClient.setCookie).
func (j *Jar) Set(name, value, domain, path string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	domain = strings.ToLower(domain)
	kept := j.cookies[:0]
	for _, c := range j.cookies {
		if !(c.Name == name && c.Domain == domain && c.Path == path) {
			kept = append(kept, c)
		}
	}
	j.cookies = append(kept, &Cookie{
		Name:       name,
		Value:      value,
		Domain:     domain,
		Path:       path,
		OriginHost: strings.TrimPrefix(domain, "."),
	})
}

// Get returns the first non-expired value for name across all domains.
func (j *Jar) Get(name string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	for _, c := range j.cookies {
		if c.Name == name && !c.IsExpired(now) {
			return c.Value, true
		}
	}
	return "", false
}

// GetForDomain returns the value for name scoped to a domain and path.
func (j *Jar) GetForDomain(name, domain, path string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	for _, c := range j.cookies {
		if c.Name == name && c.DomainMatches(domain) && c.PathMatches(path) && !c.IsExpired(now) {
			return c.Value, true
		}
	}
	return "", false
}

// HeaderFor builds a Cookie: request header value for a request, or "".
func (j *Jar) HeaderFor(host, path string, secure bool) string {
	matched := j.Matching(host, path, secure)
	if len(matched) == 0 {
		return ""
	}
	pairs := make([]string, len(matched))
	for i, kv := range matched {
		pairs[i] = kv[0] + "=" + kv[1]
	}
	return strings.Join(pairs, "; ")
}

// Matching returns all cookies matching a request (name/value pairs), in jar order.
func (j *Jar) Matching(host, path string, secure bool) [][2]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	var out [][2]string
	for _, c := range j.cookies {
		if c.Matches(host, path, secure, now) {
			out = append(out, [2]string{c.Name, c.Value})
		}
	}
	return out
}

// Clear removes cookies selected by scope, preserving the rest of the session.
//
// Scope is decided by the host that *set* each cookie, not by the (possibly
// shared) cookie domain, so a gateway clear does not wipe service cookies on
// other school hosts.
func (j *Jar) Clear(scope Scope) {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch scope {
	case All:
		j.cookies = nil
	case Gateway:
		j.cookies = retain(j.cookies, func(c *Cookie) bool { return !isGateway(c.OriginHost) })
	case Identity:
		j.cookies = retain(j.cookies, func(c *Cookie) bool { return !isIdentity(c.OriginHost) })
	case Service:
		j.cookies = retain(j.cookies, func(c *Cookie) bool { return !isService(c.OriginHost) })
	}
}

// ClearNamed removes every cookie with the given name.
func (j *Jar) ClearNamed(name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = retain(j.cookies, func(c *Cookie) bool { return c.Name != name })
}

// ClearDomain removes every cookie stored for the given domain.
func (j *Jar) ClearDomain(domain string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = retain(j.cookies, func(c *Cookie) bool { return c.Domain != domain })
}

// Replace replaces the entire jar contents (used for account/session isolation).
func (j *Jar) Replace(cookies []*Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = append(j.cookies[:0], cookies...)
}

// All returns a snapshot of all cookies, including values (used for copying
// between modes and for session snapshot/restore).
func (j *Jar) All() []*Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*Cookie, len(j.cookies))
	copy(out, j.cookies)
	return out
}

// Summary is a redacted cookie description for diagnostics.
type Summary struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  string `json:"expires"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"http_only"`
	Expired  bool   `json:"expired"`
}

// Inspect returns a redacted view: names/domains/expiry only.
func (j *Jar) Inspect() []Summary {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	out := make([]Summary, 0, len(j.cookies))
	for _, c := range j.cookies {
		s := Summary{
			Name:     c.Name,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			Expired:  c.IsExpired(now),
		}
		if c.Expires != nil {
			s.Expires = c.Expires.UTC().Format(time.RFC3339)
		}
		out = append(out, s)
	}
	return out
}

// Len returns the number of stored cookies.
func (j *Jar) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.cookies)
}

// IsEmpty reports whether the jar is empty.
func (j *Jar) IsEmpty() bool { return j.Len() == 0 }

// URLParts splits a URL into (host, path, secure) for jar lookups.
func URLParts(rawURL string) (host, path string, secure bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "/", false
	}
	return strings.ToLower(u.Hostname()), u.EscapedPath(), u.Scheme == "https"
}

// --- internals ---

func (j *Jar) removeLocked(name, domain, path string) {
	kept := j.cookies[:0]
	for _, c := range j.cookies {
		nameMatch := c.Name == name
		domainMatch := domain == "" || c.DomainMatches(domain)
		pathMatch := path == "" || c.PathMatches(path)
		if nameMatch && domainMatch && pathMatch {
			continue
		}
		kept = append(kept, c)
	}
	j.cookies = kept
}

func (j *Jar) pruneExpiredLocked(now time.Time) {
	j.cookies = retain(j.cookies, func(c *Cookie) bool { return !c.IsExpired(now) })
}

func retain(cookies []*Cookie, keep func(*Cookie) bool) []*Cookie {
	kept := cookies[:0]
	for _, c := range cookies {
		if keep(c) {
			kept = append(kept, c)
		}
	}
	return kept
}

func lowerOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return strings.ToLower(v)
}

func defaultDomain(host string) string {
	host = strings.ToLower(host)
	// The reference client lets gateway cookies span all vpn2.aufe.edu.cn
	// subdomains, and otherwise scopes host-only.
	if strings.HasSuffix(host, ".vpn2.aufe.edu.cn") || host == "vpn2.aufe.edu.cn" {
		return ".vpn2.aufe.edu.cn"
	}
	return host
}

func defaultPath(requestPath string) string {
	if requestPath == "" || !strings.HasPrefix(requestPath, "/") {
		return "/"
	}
	idx := strings.LastIndex(requestPath, "/")
	if idx <= 0 {
		return "/"
	}
	return requestPath[:idx]
}

func isGateway(domain string) bool {
	d := strings.TrimPrefix(domain, ".")
	return d == "vpn2.aufe.edu.cn" || d == "vpn.aufe.edu.cn"
}

func isIdentity(domain string) bool { return strings.Contains(domain, "uaap") }

func isService(domain string) bool { return !isGateway(domain) && !isIdentity(domain) }

func parseI64(s string) (int64, bool) {
	var v int64
	neg := false
	s = strings.TrimSpace(s)
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, len(s) > 0
}

var cookieDateFormats = []string{
	"Mon, 02 Jan 2006 15:04:05 GMT",
	"Monday, 02-Jan-06 15:04:05 GMT",
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan  2 15:04:05 MST 2006",
}

func parseCookieDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range cookieDateFormats {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	// RFC3339 as a lenient fallback for tests.
	if t, ok := timeutil.ParseISO8601(raw); ok {
		return t, true
	}
	return time.Time{}, false
}
