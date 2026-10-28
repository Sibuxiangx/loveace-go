// Package detector implements the session-expiry page detector.
//
// Faithful port of SessionPageDetector.inspect from
// ios/loveaceios/Network/NetworkLogStore.swift, plus the legacy login-page
// heuristic from HTTPClient.isVpnLoginPage. The detector intentionally uses
// combinations of structural signals so that an EC login success response
// (<TwfID>) or an ordinary JSON payload is never mistaken for an expired
// session.
package detector

import (
	"net/url"
	"strings"
)

// Detection is the result of inspecting a response for session-expiry signals.
type Detection struct {
	IsExpired bool     `json:"is_expired"`
	Kind      string   `json:"kind"`
	Signals   []string `json:"signals"`
}

// Normal returns a neutral detection.
func Normal() Detection {
	return Detection{Kind: "normal", Signals: []string{}}
}

// Detector is the stateless session-page detector.
type Detector struct{}

// Inspect examines a response body (bounded to the first 64 KiB, as iOS does).
func (Detector) Inspect(responseURL string, body []byte, location string) Detection {
	if len(body) > 64*1024 {
		body = body[:64*1024]
	}
	text := string(body)
	lower := strings.ToLower(text)

	var host, path, responseQuery, locationQuery string
	if parsed, err := url.Parse(responseURL); err == nil && responseURL != "" {
		host = strings.ToLower(parsed.Hostname())
		path = strings.ToLower(parsed.EscapedPath())
		responseQuery = parsed.RawQuery
	}
	if parsed, err := url.Parse(location); err == nil && location != "" {
		locationQuery = parsed.RawQuery
	}

	signals := make([]string, 0, 8)
	push := func(name string, cond bool) {
		if cond {
			signals = append(signals, name)
		}
	}

	push("vpn_portal_url", host == "vpn.aufe.edu.cn" && path == "/portal")
	push("vpn_portal_url", host == "vpn.aufe.edu.cn" && strings.HasPrefix(path, "/portal/"))
	push("vpn_route_selector_url", host == "vpn2.aufe.edu.cn" && path == "/" && strings.Contains(locationQuery, "redirect_uri="))
	push("vpn_route_selector_url", host == "vpn2.aufe.edu.cn" && path == "/" && strings.Contains(responseQuery, "redirect_uri="))
	push("portal_app_root", strings.Contains(lower, `id="app"`) && strings.Contains(lower, `ms-controller="app"`))
	push("sangfor_body", strings.Contains(lower, "sangfor-body"))
	push("sangfor_main", strings.Contains(lower, "sangfor-main"))
	push("portal_session_script", strings.Contains(lower, "jssdk/business/session.js"))
	push("portal_auth_script", strings.Contains(lower, "jssdk/business/auth.js"))
	push("portal_jsdata", strings.Contains(lower, "../por/jsdata.csp") || strings.Contains(lower, "/por/jsdata.csp"))
	push("portal_ec_redirect", strings.Contains(lower, "window.ecredirect"))
	push("portal_login_route", strings.Contains(lower, "#!/login"))
	push("vpn_route_selector_script", strings.Contains(lower, "g_lines"))
	push("vpn_route_selector_goto", strings.Contains(lower, "gotolines"))
	push("vpn_route_selector_probe", strings.Contains(lower, "win_location"))
	push("vpn_route_selector_marker", strings.Contains(lower, "sf_ssl_ms_"))
	push("vpn_phone_entry", strings.Contains(lower, "/por/phone_index.csp"))
	push("vpn_portal_fallback", strings.Contains(lower, "getorigin() + '/portal'"))
	push("vpn_auth_response", strings.Contains(lower, "<twfid>"))

	// CAS unified-login page (UAAP). A 302 from a service to
	// .../cas/login?service=... followed to a 200 login page means the
	// gateway session is gone. Use combinations, not a single marker.
	htmlLike := strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<html") || strings.Contains(lower, "<form")
	casLoginURL := strings.Contains(path, "/cas/login")
	casLoginForm := htmlLike &&
		(strings.Contains(lower, "<title>统一身份认证") ||
			(containsNameAttr(lower, "execution") &&
				(containsNameAttr(lower, "_eventid") ||
					strings.Contains(lower, "_eventid=submit") ||
					containsNameAttr(lower, "lt"))))
	push("cas_login_url", casLoginURL)
	push("cas_login_form", casLoginForm)

	portalSignals := 0
	routeSelectorSignals := 0
	for _, s := range signals {
		switch {
		case strings.HasPrefix(s, "vpn_portal") || strings.HasPrefix(s, "portal_"):
			portalSignals++
		case strings.HasPrefix(s, "vpn_route_selector") ||
			strings.HasPrefix(s, "vpn_phone_") ||
			strings.HasPrefix(s, "vpn_portal_fallback"):
			routeSelectorSignals++
		}
	}

	casLoginSignal := false
	for _, s := range signals {
		if s == "cas_login_url" || s == "cas_login_form" {
			casLoginSignal = true
			break
		}
	}
	hasPortalURL := false
	for _, s := range signals {
		if s == "vpn_portal_url" {
			hasPortalURL = true
			break
		}
	}
	hasRouteSelectorURL := false
	for _, s := range signals {
		if s == "vpn_route_selector_url" {
			hasRouteSelectorURL = true
			break
		}
	}
	isPortal := hasPortalURL || portalSignals >= 3 || hasRouteSelectorURL ||
		routeSelectorSignals >= 3 || casLoginSignal

	kind := "normal"
	if isPortal {
		kind = "vpn_session_expired"
	} else {
		for _, s := range signals {
			if s == "vpn_auth_response" {
				kind = "vpn_auth"
				break
			}
		}
	}

	return Detection{
		IsExpired: isPortal,
		Kind:      kind,
		Signals:   signals,
	}
}

// IsLegacyLoginPage implements the legacy VPN login-page heuristic from
// HTTPClient.isVpnLoginPage. Never fires for the /por/login_* endpoints
// themselves.
func (Detector) IsLegacyLoginPage(requestURL string, body []byte) bool {
	if strings.Contains(requestURL, "/por/login_auth.csp") ||
		strings.Contains(requestURL, "/por/login_psw.csp") {
		return false
	}
	if len(body) > 4096 {
		body = body[:4096]
	}
	text := string(body)
	trimmed := strings.TrimLeft(text, " \t\r\n")
	htmlRoot := strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html")
	if !htmlRoot {
		return false
	}
	return strings.Contains(text, "login_auth.csp") ||
		strings.Contains(text, "TWFID") ||
		strings.Contains(text, "svpn_name")
}

// containsNameAttr is a case-insensitive match for an HTML name="x" attribute
// (haystack is expected to already be lowercased).
func containsNameAttr(haystack, name string) bool {
	return strings.Contains(haystack, `name="`+name+`"`) ||
		strings.Contains(haystack, `name='`+name+`'`)
}
