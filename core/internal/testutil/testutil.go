// Package testutil provides shared test support: a successful login fixture
// and scripted transports.
package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// RSAPublicNumbers generates a throwaway RSA key and returns
// (modulus_hex, exponent_decimal).
func RSAPublicNumbers(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(key.PublicKey.N.Bytes()), itoa(key.PublicKey.E)
}

func itoa(v int) string {
	digits := make([]byte, 0, 8)
	if v == 0 {
		return "0"
	}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// LoginTransport is a mock transport preloaded with a successful EC + UAAP
// login flow.
func LoginTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	modulus, exponent := RSAPublicNumbers(t)
	authBody := "<auth><TwfID>t1</TwfID><RSA_ENCRYPT_KEY>" + modulus +
		"</RSA_ENCRYPT_KEY><RSA_ENCRYPT_EXP>" + exponent +
		"</RSA_ENCRYPT_EXP><CSRF_RAND_CODE>c1</CSRF_RAND_CODE></auth>"
	casPage := `
        <html><body>
        <form action="/cas/login" method="post">
          <input type="hidden" name="lt" value="LT-1"/>
          <input type="hidden" name="execution" value="E-1"/>
        </form>
        </body></html>`
	return transport.NewMockTransport().
		OnRequest(transport.Get, "/por/login_auth.csp", transport.NewResponse(200, []byte(authBody))).
		OnRequest(transport.Post, "/por/login_psw.csp", transport.NewResponse(200, []byte("<Result>1</Result>"))).
		// The UAAP login URL embeds j_spring_cas_security_check inside its
		// service= query, so the callback pattern carries the ?ticket=
		// suffix: MockTransport prefers the longest matching pattern, and a
		// bare callback pattern would shadow the CAS login page route.
		OnRequest(transport.Get, "j_spring_cas_security_check?ticket=", transport.NewResponse(200, []byte("<html>jwc</html>"))).
		// A CAS GET for the AAC mint service redirects to the service, and
		// a HEALTHY gateway session has the Location REWRITTEN into the
		// vpn2 namespace — the rewrite is the gateway session's liveness
		// signature (school.slotRewriteAlive probes exactly this hop; the
		// lowercase %3a keeps the UAAP login URL's %3A on the plain-page
		// route). Longer per-test overrides shadow this one.
		OnRequest(transport.Get, "cas/login?service=http%3a%2f%2fapi.dekt", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/User/Index/CoreLoginCallback?isCASGateway=true&register?ticket=ST-PROBE")).
		OnRequest(transport.Get, "cas/login", transport.NewResponse(200, []byte(casPage))).
		// CAS issues a service-ticket redirect; the JWC callback is a
		// separate best-effort hop.
		OnRequest(transport.Post, "cas/login", transport.NewResponse(302, []byte("The URL has moved")).
			WithHeader("Location", "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/j_spring_cas_security_check?ticket=ST1"))
}
