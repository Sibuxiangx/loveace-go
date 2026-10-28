package cookies

import "testing"

const (
	host = "jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn"
	path = "/student/integratedQuery/scoreQuery/allTermScores/index"
)

func TestStoresAndMatchesSubdomainCookie(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("JSESSIONID=abc; Path=/; HttpOnly", host, path)
	if got := jar.HeaderFor(host, "/x", true); !contains(got, "JSESSIONID=abc") {
		t.Fatalf("header = %q", got)
	}
	if jar.HeaderFor("other.vpn2.aufe.edu.cn", "/x", true) == "" {
		t.Fatal("subdomain must match the default .vpn2.aufe.edu.cn domain")
	}
	if jar.HeaderFor("example.com", "/x", true) != "" {
		t.Fatal("unrelated host must not match")
	}
}

func TestMaxAgeZeroDeletes(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("TWFID=v1; Path=/", host, path)
	if v, _ := jar.Get("TWFID"); v != "v1" {
		t.Fatalf("get = %q", v)
	}
	jar.StoreSetCookie("TWFID=gone; Path=/; Max-Age=0", host, path)
	if _, ok := jar.Get("TWFID"); ok {
		t.Fatal("Max-Age=0 must delete")
	}
}

func TestSecureCookiesAreHTTPSOnly(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("S=1; Path=/; Secure", host, path)
	if jar.HeaderFor(host, "/", false) != "" {
		t.Fatal("secure cookie must not match http")
	}
	if jar.HeaderFor(host, "/", true) == "" {
		t.Fatal("secure cookie must match https")
	}
}

func TestScopedClearPreservesOtherOrigins(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("TWFID=gw; Path=/", "vpn2.aufe.edu.cn", "/por/login_psw.csp")
	jar.StoreSetCookie("JSESSIONID=svc; Path=/; Domain=jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn", host, path)
	jar.Clear(Gateway)
	if _, ok := jar.Get("TWFID"); ok {
		t.Fatal("gateway clear must remove TWFID")
	}
	if v, _ := jar.Get("JSESSIONID"); v != "svc" {
		t.Fatal("gateway clear must preserve service cookies")
	}
	jar.Clear(All)
	if !jar.IsEmpty() {
		t.Fatal("clear all must empty the jar")
	}
}

func TestPathDefaultUsesDirectory(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("A=1", host, path)
	if jar.HeaderFor(host, path, true) == "" {
		t.Fatal("exact request path must match")
	}
	dir := path[:lastSlash(path)]
	if jar.HeaderFor(host, dir+"/sibling", true) == "" {
		t.Fatal("sibling path in the same directory must match")
	}
	if jar.HeaderFor(host, "/student/other", true) != "" {
		t.Fatal("unrelated directory must not match")
	}
}

func TestRepeatedSetCookieReplacesSameName(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("A=1; Path=/", host, path)
	jar.StoreSetCookie("A=2; Path=/", host, path)
	if v, _ := jar.Get("A"); v != "2" {
		t.Fatalf("get = %q", v)
	}
	if jar.Len() != 1 {
		t.Fatalf("len = %d", jar.Len())
	}
}

func TestExpiresParsing(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("E=1; Expires=Wed, 21 Oct 2099 07:28:00 GMT; Path=/", host, path)
	if _, ok := jar.Get("E"); !ok {
		t.Fatal("future expiry must be stored")
	}
	jar.StoreSetCookie("E=1; Expires=Wed, 21 Oct 2015 07:28:00 GMT; Path=/", host, path)
	if _, ok := jar.Get("E"); ok {
		t.Fatal("past expiry must prune")
	}
}

func TestSetExplicitCookie(t *testing.T) {
	jar := New()
	jar.Set("TWFID", "tw1", ".vpn2.aufe.edu.cn", "/")
	if v, _ := jar.GetForDomain("TWFID", "vpn2.aufe.edu.cn", "/x"); v != "tw1" {
		t.Fatalf("get = %q", v)
	}
	// Inspect never exposes values.
	for _, s := range jar.Inspect() {
		if s.Name != "TWFID" || s.Domain != ".vpn2.aufe.edu.cn" {
			t.Fatalf("summary = %+v", s)
		}
	}
}

func TestReplaceAndAll(t *testing.T) {
	jar := New()
	jar.StoreSetCookie("A=1; Path=/", host, path)
	snap := jar.All()
	other := New()
	other.Replace(snap)
	if v, _ := other.Get("A"); v != "1" {
		t.Fatal("restore failed")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
