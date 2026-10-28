package detector

import "testing"

var d Detector

func TestECAuthResponseIsNotExpiry(t *testing.T) {
	detection := d.Inspect("https://vpn2.aufe.edu.cn/por/login_auth.csp",
		[]byte("<auth><TwfID>abc</TwfID></auth>"), "")
	if detection.IsExpired {
		t.Fatalf("detection = %+v", detection)
	}
	if detection.Kind != "vpn_auth" {
		t.Fatalf("kind = %q", detection.Kind)
	}
	found := false
	for _, s := range detection.Signals {
		if s == "vpn_auth_response" {
			found = true
		}
	}
	if !found {
		t.Fatalf("signals = %v", detection.Signals)
	}
}

func TestSangforPortalIsExpiry(t *testing.T) {
	body := []byte(`<html><body class="sangfor-body"><div class="sangfor-main"></div>
	<script src="/jssdk/business/session.js"></script>
	<script>var g_lines=[];var gotoLines=[];win_location=1;var x='sf_ssl_ms_1';</script>
	<a href="#!/login">login</a></body></html>`)
	detection := d.Inspect("https://vpn2.aufe.edu.cn/", body,
		"https://vpn2.aufe.edu.cn/?redirect_uri=https%3A%2F%2Fexample")
	if !detection.IsExpired {
		t.Fatalf("detection = %+v", detection)
	}
	if detection.Kind != "vpn_session_expired" {
		t.Fatalf("kind = %q", detection.Kind)
	}
}

func TestOrdinaryJSONIsNormal(t *testing.T) {
	detection := d.Inspect("http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/main/academicInfo",
		[]byte(`{"code":0,"data":[]}`), "")
	if detection.IsExpired || detection.Kind != "normal" {
		t.Fatalf("detection = %+v", detection)
	}
}

func TestCASLoginPageIsExpiry(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html><head><title>统一身份认证</title></head>
	<body><form action="/cas/login" method="post">
	<input type="hidden" name="lt" value="LT-1"/>
	<input type="hidden" name="execution" value="E-1"/>
	</form></body></html>`)
	detection := d.Inspect("http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/cas/login?service=x", body, "")
	if !detection.IsExpired {
		t.Fatalf("detection = %+v", detection)
	}
}

func TestLegacyLoginPage(t *testing.T) {
	body := []byte("<!DOCTYPE html><html><body>login_auth.csp please</body></html>")
	if !d.IsLegacyLoginPage("http://jwc/jwc/data", body) {
		t.Fatal("legacy login page must be detected")
	}
	// Never for the /por endpoints themselves.
	if d.IsLegacyLoginPage("https://vpn2.aufe.edu.cn/por/login_auth.csp", body) {
		t.Fatal("/por endpoints are excluded")
	}
	// Plain JSON is not a login page.
	if d.IsLegacyLoginPage("http://jwc/jwc/data", []byte(`{"ok":true}`)) {
		t.Fatal("json must not be a login page")
	}
	// TWFID mention inside HTML triggers.
	body2 := []byte("<html><body>TWFID missing</body></html>")
	if !d.IsLegacyLoginPage("http://jwc/jwc/data", body2) {
		t.Fatal("TWFID mention must trigger")
	}
}
