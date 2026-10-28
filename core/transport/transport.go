// Package transport defines the injectable HTTP transport.
//
// The core never talks to a hard-coded production endpoint from tests: all
// network access goes through the Transport interface, so fixture tests can
// substitute a local mock server or an in-memory responder. Redirects are
// always disabled at this layer and handled centrally by the session so that
// cookie and authentication semantics are preserved.
package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/config"
)

// Method is the HTTP method used by the school services.
type Method string

// HTTP methods.
const (
	Get    Method = "GET"
	Post   Method = "POST"
	Put    Method = "PUT"
	Delete Method = "DELETE"
	Head   Method = "HEAD"
)

// IsRead reports whether the method is idempotent and safe to retry after reauth.
func (m Method) IsRead() bool { return m == Get || m == Head }

// Request is a single raw HTTP request (one hop, no redirect following).
type Request struct {
	Method    Method
	URL       string
	Headers   [][2]string
	Body      []byte
	TimeoutMS int64
	MaxBytes  int
	// Operation is the logical operation name, when known (debug metadata only).
	Operation string
	// RequestID is the logical request id, when assigned (debug metadata only).
	RequestID string
}

// NewRequest constructs a request with default timeout and size bounds.
func NewRequest(method Method, url string) *Request {
	return &Request{
		Method:    method,
		URL:       url,
		TimeoutMS: config.DefaultTimeoutMS,
		MaxBytes:  config.DefaultMaxResponseBytes,
	}
}

// Header adds a header.
func (r *Request) Header(name, value string) *Request {
	r.Headers = append(r.Headers, [2]string{name, value})
	return r
}

// WithBody sets the raw body.
func (r *Request) WithBody(body []byte) *Request {
	r.Body = body
	return r
}

// WithTimeout sets the per-request deadline in milliseconds.
func (r *Request) WithTimeout(ms int64) *Request {
	r.TimeoutMS = ms
	return r
}

// WithMaxBytes sets the response size bound.
func (r *Request) WithMaxBytes(max int) *Request {
	r.MaxBytes = max
	return r
}

// GetHeader performs a case-insensitive lookup of a request header.
func (r *Request) GetHeader(name string) (string, bool) {
	for _, kv := range r.Headers {
		if strings.EqualFold(kv[0], name) {
			return kv[1], true
		}
	}
	return "", false
}

// Clone deep-copies the request.
func (r *Request) Clone() *Request {
	clone := *r
	clone.Headers = append([][2]string(nil), r.Headers...)
	clone.Body = append([]byte(nil), r.Body...)
	return &clone
}

// Response is a raw HTTP response.
type Response struct {
	Status  int
	Headers [][2]string
	Body    []byte
	// FinalURL is the URL that produced this response (after any
	// transport-level redirect).
	FinalURL string
}

// NewResponse constructs a response (primarily for mock transports).
func NewResponse(status int, body []byte) *Response {
	return &Response{Status: status, Body: body}
}

// WithHeader adds a header.
func (r *Response) WithHeader(name, value string) *Response {
	r.Headers = append(r.Headers, [2]string{name, value})
	return r
}

// Header returns the first value of a header, case-insensitive.
func (r *Response) Header(name string) (string, bool) {
	for _, kv := range r.Headers {
		if strings.EqualFold(kv[0], name) {
			return kv[1], true
		}
	}
	return "", false
}

// SetCookieHeaders returns all Set-Cookie header values in order.
func (r *Response) SetCookieHeaders() []string {
	var out []string
	for _, kv := range r.Headers {
		if strings.EqualFold(kv[0], "set-cookie") {
			out = append(out, kv[1])
		}
	}
	return out
}

// IsRedirect reports whether the status code is a redirect.
func (r *Response) IsRedirect() bool { return r.Status >= 300 && r.Status <= 399 }

// declaredCharset extracts the declared charset from Content-Type, lowercased.
func (r *Response) declaredCharset() string {
	ct, _ := r.Header("content-type")
	ct = strings.ToLower(ct)
	if i := strings.Index(ct, "charset="); i >= 0 {
		rest := ct[i+len("charset="):]
		end := strings.IndexAny(rest, "; \t")
		if end < 0 {
			end = len(rest)
		}
		return strings.Trim(rest[:end], `"' `)
	}
	return ""
}

// BodyText returns the body as text, honouring a declared GBK/GB18030 charset.
//
// Most school responses are UTF-8 JSON/HTML; some legacy HTML pages declare
// `gbk`/`gb2312`/`gb18030` in Content-Type, so those are decoded with
// golang.org/x/text rather than lossy UTF-8. Unspecified or other charsets
// use lossy UTF-8.
func (r *Response) BodyText() string {
	cs := r.declaredCharset()
	switch cs {
	case "gbk", "gb2312", "gb18030":
		decoded, _ := gbkDecoder.NewDecoder().Bytes(r.Body)
		return string(decoded)
	default:
		return string(r.Body)
	}
}

// Transport executes exactly one HTTP hop.
type Transport interface {
	Execute(ctx context.Context, request *Request) (*Response, error)
}

// HTTPTransport is the production transport backed by net/http.
//
// Redirects are disabled so the session can preserve method semantics and
// cookie scoping; TLS verification stays enabled.
type HTTPTransport struct {
	client *http.Client
	base   *http.Transport
	mu     sync.Mutex // guards proxy swap (the live sessions may rebind mid-flight)
}

// NewHTTPTransport builds a transport. TLS certificate verification remains
// enabled. maxConnsPerHost caps per-host connections (0 = unlimited).
func NewHTTPTransport(maxConnsPerHost int) *HTTPTransport {
	return NewHTTPTransportWithProxy(maxConnsPerHost, "")
}

// NewHTTPTransportWithProxy builds a transport whose requests egress through
// the given HTTP(S) proxy URL (e.g. http://127.0.0.1:18080 tunneled over SSH).
// nil proxy means direct dialing.
func NewHTTPTransportWithProxy(maxConnsPerHost int, proxyURL string) *HTTPTransport {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&netDialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4096,
		MaxIdleConnsPerHost:   1024,
		MaxConnsPerHost:       maxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	ht := &HTTPTransport{base: t}
	if proxyURL != "" {
		ht.setProxyLocked(proxyURL)
	}
	ht.client = &http.Client{
		Transport: t,
		// Redirects handled centrally by the session.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		// Cookies handled by the session-owned jar.
		Jar: nil,
	}
	return ht
}

// SetProxy re-points a LIVE transport at a new proxy exit (or clears it with
// an empty string). The EC gateway binds a TWFID to its birth egress IP
// (2026-10-17 verified): a session reborn through a different proxy port
// must switch its egress atomically without touching the session above.
// Idle connections to the old exit are closed so nothing leaks past the
// swap.
func (t *HTTPTransport) SetProxy(proxyURL string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if proxyURL == "" {
		t.base.Proxy = http.ProxyFromEnvironment
	} else {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Scheme == "" {
			return // keep the current egress; the caller logs the bad URL
		}
		t.base.Proxy = http.ProxyURL(u)
		t.base.ForceAttemptHTTP2 = false
	}
	t.base.CloseIdleConnections()
}

// WithProxy is the chainable constructor form of SetProxy.
func (t *HTTPTransport) WithProxy(proxyURL string) *HTTPTransport {
	t.SetProxy(proxyURL)
	return t
}

func (t *HTTPTransport) setProxyLocked(proxyURL string) {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme == "" {
		return
	}
	t.base.Proxy = http.ProxyURL(u)
	t.base.ForceAttemptHTTP2 = false
}

// ProxyURL reports the current egress proxy endpoint ("" = direct). For
// diagnostics and the borrow paths (a validation clone must ride the
// host's exit).
func (t *HTTPTransport) ProxyURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "example.com"}}
	u, err := t.base.Proxy(req)
	if err != nil || u == nil {
		return ""
	}
	return u.String()
}

// Execute implements Transport. It performs exactly one hop: non-empty bodies
// and explicit Content-Length: 0 for empty form POSTs (the school gateway
// replies 411 Length Required to a length-less empty form POST), bounded
// response reads, per-request timeout.
func (t *HTTPTransport) Execute(ctx context.Context, request *Request) (*Response, error) {
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	carriesBody := request.Method != Get && request.Method != Head
	if len(request.Body) > 0 {
		body = bytes.NewReader(request.Body)
	} else if carriesBody {
		body = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, string(request.Method), request.URL, body)
	if err != nil {
		return nil, errs.NewNetwork(err.Error())
	}
	var hasHost, hasUA, hasContentLength bool
	for _, kv := range request.Headers {
		name, value := kv[0], kv[1]
		if strings.EqualFold(name, "host") {
			req.Host = value
			hasHost = true
			continue
		}
		if strings.EqualFold(name, "content-length") {
			// Set manually below to guarantee the wire header.
			hasContentLength = true
			req.ContentLength, _ = parseI64(value)
			// Also record it as a header so it is definitely sent.
			req.Header["Content-Length"] = []string{value}
			continue
		}
		// Preserve non-canonical spellings (ticket, sdp-app-session).
		if isCanonical(name) {
			req.Header.Set(name, value)
		} else {
			req.Header[name] = []string{value}
		}
		if strings.EqualFold(name, "user-agent") {
			hasUA = true
		}
	}
	if !hasUA {
		req.Header.Set("User-Agent", config.UADesktop)
	}
	if carriesBody && len(request.Body) == 0 && !hasContentLength {
		req.ContentLength = 0
		req.Header["Content-Length"] = []string{"0"}
	}
	if !hasHost {
		if host := hostOf(request.URL); host != "" {
			req.Host = host
		}
	}

	started := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errs.NewTimeout(request.TimeoutMS)
		}
		if ctx.Err() == context.Canceled {
			return nil, errs.NewCancelled()
		}
		return nil, errs.NewNetwork(err.Error())
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, int64(request.MaxBytes)+1)
	bodyBytes, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, errs.NewNetwork(readErr.Error())
	}
	if len(bodyBytes) > request.MaxBytes {
		return nil, errs.NewHTTP(resp.StatusCode, "response body exceeded size bound")
	}
	_ = started

	headers := make([][2]string, 0, len(resp.Header))
	for name, values := range resp.Header {
		for _, v := range values {
			headers = append(headers, [2]string{name, v})
		}
	}
	return &Response{
		Status:   resp.StatusCode,
		Headers:  headers,
		Body:     bodyBytes,
		FinalURL: resp.Request.URL.String(),
	}, nil
}

// MockTransport is an in-memory responder for fixture tests.
//
// Patterns are matched against the URL by substring; the longest matching
// pattern wins so that a more specific callback route is not shadowed by a
// shorter one. Matching can be restricted to a method (Rust `on_request`) or
// a status line (`on_status`).
type MockTransport struct {
	mu       sync.Mutex
	routes   []mockRoute
	recorded []*Request
}

type mockRoute struct {
	method    Method // empty = any
	urlSubstr string
	status    int // 0 = any
	response  *Response
}

// NewMockTransport creates an empty mock.
func NewMockTransport() *MockTransport { return &MockTransport{} }

// On registers a response for any method whose URL contains urlSubstr.
func (m *MockTransport) On(urlSubstr string, response *Response) *MockTransport {
	return m.onRoute(mockRoute{urlSubstr: urlSubstr, response: response})
}

// OnRequest registers a response for a specific method + URL substring.
func (m *MockTransport) OnRequest(method Method, urlSubstr string, response *Response) *MockTransport {
	return m.onRoute(mockRoute{method: method, urlSubstr: urlSubstr, response: response})
}

// OnStatus registers a response for a URL substring + expected response status.
func (m *MockTransport) OnStatus(urlSubstr string, status int, response *Response) *MockTransport {
	return m.onRoute(mockRoute{urlSubstr: urlSubstr, status: status, response: response})
}

func (m *MockTransport) onRoute(route mockRoute) *MockTransport {
	m.mu.Lock()
	m.routes = append(m.routes, route)
	m.mu.Unlock()
	return m
}

// Execute implements Transport.
func (m *MockTransport) Execute(ctx context.Context, request *Request) (*Response, error) {
	m.mu.Lock()
	m.recorded = append(m.recorded, request.Clone())
	var best *mockRoute
	for i := range m.routes {
		route := &m.routes[i]
		if route.method != "" && route.method != request.Method {
			continue
		}
		if route.status != 0 && route.status != request.URLStatusHint() {
			continue
		}
		if !strings.Contains(request.URL, route.urlSubstr) {
			continue
		}
		if best == nil || len(route.urlSubstr) > len(best.urlSubstr) {
			best = route
		}
	}
	m.mu.Unlock()

	if best == nil {
		// Unmatched requests return a synthetic 404 so tests fail loudly
		// rather than touching the network (Rust MockTransport parity).
		return &Response{
			Status:   404,
			Body:     []byte("mock: no route matches " + request.URL),
			FinalURL: request.URL,
		}, nil
	}
	resp := &Response{
		Status:   best.response.Status,
		Headers:  append([][2]string(nil), best.response.Headers...),
		Body:     append([]byte(nil), best.response.Body...),
		FinalURL: request.URL,
	}
	return resp, nil
}

// Recorded returns the recorded requests (copies).
func (m *MockTransport) Recorded() []*Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Request, len(m.recorded))
	for i, r := range m.recorded {
		out[i] = r.Clone()
	}
	return out
}

// LastRequest returns the most recent recorded request, if any.
func (m *MockTransport) LastRequest() *Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recorded) == 0 {
		return nil
	}
	return m.recorded[len(m.recorded)-1].Clone()
}

// URLStatusHint exists for OnStatus parity with the Rust mock; in practice
// Go routes never need it because OnStatus routes are matched before any
// response exists. It always returns 0 (= any).
func (r *Request) URLStatusHint() int { return 0 }

// NowMillis re-exported for session metadata.
var NowMillis = timeutil.NowMillis
