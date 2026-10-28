// Package config holds the source-backed endpoint catalog and client
// configuration helpers.
//
// Every constant here is transcribed from the reference clients
// (desktop/lib/services, ios/loveaceios/Service, android/.../data/service).
// If a value disagrees between platforms, the desktop value is canonical for
// reads and the iOS value is canonical for session/recovery behaviour, per the
// migration spec.
package config

// UADesktop is the Chrome-on-Windows user agent used by the Flutter desktop client.
const UADesktop = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

// UAIOS is the mobile Safari user agent used by the iOS teacher-evaluation client.
const UAIOS = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"

// VPNServer is the VPN / EasyConnect gateway origin. Authentication origin is separate.
const VPNServer = "https://vpn2.aufe.edu.cn"

// ECLoginAuth is the EC gateway auth-parameter endpoint.
const ECLoginAuth = "/por/login_auth.csp?apiversion=1"

// ECLoginPsw is the EC gateway credential-submission endpoint.
const ECLoginPsw = "/por/login_psw.csp?anti_replay=1&encrypt=1&type=cs"

// ECRandCode is the EC login CAPTCHA image endpoint (must be fetched with the same TWFID).
const ECRandCode = "/por/rand_code.csp"

// ECCheckURL is the EC login-state probe endpoint.
const ECCheckURL = "http://txzx-aufe-edu-cn-s.vpn2.aufe.edu.cn:8118/dzzy/list.htm"

// UAAPLoginURL is the UAAP (identity provider) CAS login endpoint used for the JWC ticket.
const UAAPLoginURL = "http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/cas/login?service=http%3A%2F%2Fjwcxk2.aufe.edu.cn%2Fj_spring_cas_security_check"

// UAAPCheckURL is the UAAP login-state probe endpoint (JWC root through the proxy).
const UAAPCheckURL = "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/"

// VPNHostSuffix is the common suffix of every vpn2-proxied school host.
// Redirect Locations that escape this namespace mean the gateway session is
// no longer honoured (rewriting stops), long before a request to the raw
// internal address can time out.
const VPNHostSuffix = "vpn2.aufe.edu.cn"

// UAAPBase is the UAAP/CAS identity provider proxy base.
const UAAPBase = "http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118"

// JWCBase is the JWC (academic affairs) proxy base.
const JWCBase = "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118"

// YKTBase is the YKT (campus card) proxy base.
const YKTBase = "http://ykt-aufe-edu-cn-s.vpn2.aufe.edu.cn:8118"

// AACAPIBase is the AAC (second-classroom / "爱安财") API proxy base.
const AACAPIBase = "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118"

// AACWebBase is the AAC web base.
const AACWebBase = "http://dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118"

// AACLoginService is the AAC CAS login service used to mint a ticket.
const AACLoginService = "http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/cas/login?service=http%3a%2f%2fapi.dekt.ac.acxk.net%2fUser%2fIndex%2fCoreLoginCallback%3fisCASGateway%3dtrue"

// LaborAPIBase is the labor-club API proxy base.
const LaborAPIBase = "http://api-ldjlb-ac-acxk-net.vpn2.aufe.edu.cn:8118"

// LaborLoginService is the labor-club CAS login service used to mint a ticket.
const LaborLoginService = "http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/cas/login?service=http%3a%2f%2fapi.ldjlb.ac.acxk.net%2fUser%2fIndex%2fCoreLoginCallback%3fisCASGateway%3dtrue"

// ISIMBase is the ISIM / dormitory electricity (后勤) base (no port in the desktop source).
const ISIMBase = "http://hqkd-aufe-edu-cn.vpn2.aufe.edu.cn"

// RepairBase is the repair (零星维修) base.
const RepairBase = "http://wxqd-aufe-edu-cn.vpn2.aufe.edu.cn:8118"

// CompetitionBase is the competition (创新创业) base.
const CompetitionBase = "http://211-86-241-245.vpn2.aufe.edu.cn:8118"

// ManifestURLPrimary is the manifest v2 primary URL (desktop/iOS).
const ManifestURLPrimary = "https://loveace.linota.cn/loveace/manifest_v2.json"

// ManifestURLFallback is the manifest v2 fallback URL.
const ManifestURLFallback = "https://release.loveace.top/loveace/manifest_v2.json"

// Default timeout and bounds.
const (
	// DefaultTimeoutMS is the default request timeout in milliseconds
	// (desktop uses 30s, iOS 60s).
	DefaultTimeoutMS = 60_000
	// DefaultMaxResponseBytes is the default bounded response body size (16 MiB).
	DefaultMaxResponseBytes = 16 * 1024 * 1024
	// MaxRedirects is the maximum redirect hops followed per logical request.
	MaxRedirects = 8
	// PlanThrottleSecs is the minimum interval between plan-completion requests (desktop throttle).
	PlanThrottleSecs = 3
	// PlanCacheTTLSecs is the plan-completion cache lifetime in seconds (desktop uses 5 minutes).
	PlanCacheTTLSecs = 300
	// JWCCacheTTLSecs is the JWC response cache lifetime in seconds (iOS uses 30 seconds).
	JWCCacheTTLSecs = 30
)

// JoinURL builds a full URL by joining a base and a path exactly like the
// reference `toFullUrl` helpers: absolute URLs pass through, otherwise the
// trailing slash of the base and the leading slash of the path are normalised.
func JoinURL(base, path string) string {
	if len(path) >= 7 && (path[:7] == "http://") || (len(path) >= 8 && path[:8] == "https://") {
		return path
	}
	return trimRightSlash(base) + "/" + trimLeftSlash(path)
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func trimLeftSlash(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}

// WithQuery appends query parameters to a URL, preserving any existing query string.
func WithQuery(url string, params [][2]string) string {
	var head, existing string
	for i := 0; i < len(url); i++ {
		if url[i] == '?' {
			head, existing = url[:i], url[i+1:]
			break
		}
	}
	if head == "" && existing == "" {
		head = url
	}
	parts := make([]string, 0, len(params)+1)
	if existing != "" {
		parts = append(parts, existing)
	}
	for _, kv := range params {
		parts = append(parts, URLEncode(kv[0])+"="+URLEncode(kv[1]))
	}
	if len(parts) == 0 {
		return head
	}
	return head + "?" + joinBytes(parts, '&')
}

func joinBytes(parts []string, sep byte) string {
	n := len(parts) - 1
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			buf = append(buf, sep)
		}
		buf = append(buf, p...)
	}
	return string(buf)
}

const hexDigits = "0123456789ABCDEF"

// URLEncode percent-encodes a form/query component using the same component
// set as the reference `Uri.encodeComponent`/`URLEncoder` usage.
func URLEncode(value string) string {
	n := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			n++
		} else if c == ' ' {
			n += 3
		} else {
			n += 3
		}
	}
	if n == len(value) {
		return value
	}
	out := make([]byte, 0, n)
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		case c == ' ':
			out = append(out, '%', '2', '0')
		default:
			out = append(out, '%', hexDigits[c>>4], hexDigits[c&0x0f])
		}
	}
	return string(out)
}

// FormEncode encodes an application/x-www-form-urlencoded body from ordered pairs.
func FormEncode(pairs [][2]string) string {
	if len(pairs) == 0 {
		return ""
	}
	parts := make([]string, len(pairs))
	for i, kv := range pairs {
		parts[i] = URLEncode(kv[0]) + "=" + URLEncode(kv[1])
	}
	return joinBytes(parts, '&')
}
