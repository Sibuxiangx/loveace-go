package session

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// The four Rust CAPTCHA fixtures, ported (AGENTS.md 缺陷 1): the RndImg
// auth page, the TWFID-bound gif fetch, the code submission, and the
// result mapping (20023/20041/20004).

// captchaFixture is a scriptable gateway recording full requests.
type captchaFixture struct {
	mu        sync.Mutex
	authBody  string
	gifBody   []byte
	loginBody string
	requests  []*transport.Request
}

func (f *captchaFixture) Execute(_ context.Context, r *transport.Request) (*transport.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Clone())
	switch {
	case strings.Contains(r.URL, "/por/rand_code.csp"):
		return transport.NewResponse(200, f.gifBody), nil
	case strings.Contains(r.URL, "/por/login_psw.csp"):
		return transport.NewResponse(200, []byte(f.loginBody)), nil
	default: // /por/login_auth.csp
		return transport.NewResponse(200, []byte(f.authBody)), nil
	}
}

func (f *captchaFixture) recorded() []*transport.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*transport.Request(nil), f.requests...)
}

func (f *captchaFixture) headerValue(r *transport.Request, name string) string {
	for _, h := range r.Headers {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

func newCaptchaFixture(t *testing.T, rndImg bool) (*captchaFixture, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	f := &captchaFixture{
		authBody: ecAuthBody(hex.EncodeToString(key.PublicKey.N.Bytes()), fmt.Sprint(key.PublicKey.E), "c1", rndImg),
		gifBody:  []byte("GIF89a-fixture"),
	}
	return f, "TWFID123"
}

// ecAuthBody mirrors the real gateway auth-parameter page.
func ecAuthBody(modulus, exponent, csrf string, rndImg bool) string {
	img := "0"
	if rndImg {
		img = "1"
	}
	return "<auth><TwfID>TWFID123</TwfID>" +
		"<RSA_ENCRYPT_KEY>" + modulus + "</RSA_ENCRYPT_KEY>" +
		"<RSA_ENCRYPT_EXP>" + exponent + "</RSA_ENCRYPT_EXP>" +
		"<CSRF_RAND_CODE>" + csrf + "</CSRF_RAND_CODE>" +
		"<RndImg>" + img + "</RndImg></auth>"
}

// Fixture 1 — RndImg page: the auth page decides whether a CAPTCHA is
// required, and the image is only fetched then.
func TestCaptchaRndImgPage(t *testing.T) {
	f, _ := newCaptchaFixture(t, true)
	s := New("20000003", "gw", "school", f)

	captcha, err := s.EcCaptcha(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !captcha.Required || len(captcha.Image) == 0 || captcha.Mime != "image/gif" {
		t.Fatalf("captcha = %+v, want required gif image", captcha)
	}

	// Without <RndImg>1</RndImg> there is no challenge.
	f2, _ := newCaptchaFixture(t, false)
	s2 := New("20000003", "gw", "school", f2)
	captcha2, err := s2.EcCaptcha(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if captcha2.Required || captcha2.Image != nil {
		t.Fatalf("captcha without RndImg = %+v, want not required", captcha2)
	}
}

// Fixture 2 — TWFID-bound gif: the image request must carry the auth
// context's TWFID explicitly (the jar has no session cookie yet).
func TestCaptchaGifBoundToTWFID(t *testing.T) {
	f, _ := newCaptchaFixture(t, true)
	s := New("20000003", "gw", "school", f)

	if _, err := s.EcCaptcha(context.Background()); err != nil {
		t.Fatal(err)
	}
	var gifReq *transport.Request
	for _, r := range f.recorded() {
		if strings.Contains(r.URL, "/por/rand_code.csp") {
			gifReq = r
		}
	}
	if gifReq == nil {
		t.Fatal("captcha image was never fetched")
	}
	if got := f.headerValue(gifReq, "Cookie"); got != "TWFID=TWFID123" {
		t.Fatalf("gif fetch Cookie header = %q, want the auth-context TWFID", got)
	}
}

// Fixture 3 — code submission: the retry posts svpn_rand_code (the typed
// code), svpn_req_randcode (the page CSRF) and the TWFID cookie.
func TestCaptchaSubmitCarriesCodeAndCsrf(t *testing.T) {
	f, _ := newCaptchaFixture(t, true)
	f.loginBody = "<Result>1</Result>"
	s := New("20000003", "gw", "school", f)

	if _, err := s.EcCaptcha(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.ECLoginWithCaptcha(context.Background(), "ab12"); st.Outcome != ECSuccess {
		t.Fatalf("submit outcome = %s", st.Outcome)
	}
	var post *transport.Request
	for _, r := range f.recorded() {
		if strings.Contains(r.URL, "/por/login_psw.csp") {
			post = r
		}
	}
	if post == nil {
		t.Fatal("credential submission never happened")
	}
	body := string(post.Body)
	for _, want := range []string{"svpn_rand_code=ab12", "svpn_req_randcode=c1", "svpn_name=20000003", "svpn_password="} {
		if !strings.Contains(body, want) {
			t.Fatalf("form missing %s: %s", want, body)
		}
	}
	if got := f.headerValue(post, "Cookie"); got != "TWFID=TWFID123" {
		t.Fatalf("submit Cookie header = %q", got)
	}
	if twfid, ok := s.TWFID(); !ok || twfid != "TWFID123" {
		t.Fatalf("success did not publish the TWFID: %q %v", twfid, ok)
	}
}

// Fixture 4 — result mapping: 20023 with a submitted code is a rejected
// captcha (and consumes the pending context), without a code it is the
// initial challenge; 20041 ("maybe attacked") is an attack-suspicion throttle
// that judges neither the code nor the credentials (ECMaybeAttacked, never a
// captcha verdict); 20026 is a rejected request shape (ECRequestRejected);
// 20004 bad credentials; anything else unknown.
func TestCaptchaResultMapping(t *testing.T) {
	mk := func(t *testing.T, loginBody string) (*captchaFixture, *Session) {
		// RndImg=false so ECLogin actually submits and reaches the result.
		f, _ := newCaptchaFixture(t, false)
		f.loginBody = loginBody
		return f, New("20000003", "gw", "school", f)
	}

	// Initial challenge (no code yet): CaptchaRequired.
	f, s := mk(t, "<Result>20023: characters are incorrect</Result>")
	if st := s.ECLogin(context.Background()); st.Outcome != ECCaptchaRequired {
		t.Fatalf("20023 without code = %s, want captcha_required", st.Outcome)
	}
	// The same answer WITH a code is a rejection, and the pending context
	// is consumed (a retry must re-fetch the auth page).
	if st := s.ECLoginWithCaptcha(context.Background(), "ab12"); st.Outcome != ECCaptchaInvalid {
		t.Fatalf("20023 with code = %s, want captcha_invalid", st.Outcome)
	}
	authPosts := 0
	for _, r := range f.recorded() {
		if strings.Contains(r.URL, "/por/login_auth.csp") {
			authPosts++
		}
	}
	if st := s.ECLoginWithCaptcha(context.Background(), "ab12"); st.Outcome != ECCaptchaInvalid {
		t.Fatalf("retry outcome = %s", st.Outcome)
	}
	authPostsAfterRetry := 0
	for _, r := range f.recorded() {
		if strings.Contains(r.URL, "/por/login_auth.csp") {
			authPostsAfterRetry++
		}
	}
	if authPostsAfterRetry == authPosts {
		t.Fatal("rejected captcha must consume the pending auth context (retry must refetch)")
	}

	// Attack suspicion (20041 / "maybe attacked"): a throttle, not a captcha.
	f, s = mk(t, "[CDATA[maybe attacked]]")
	if st := s.ECLogin(context.Background()); st.Outcome != ECMaybeAttacked {
		t.Fatalf("20041 = %s, want maybe_attacked", st.Outcome)
	}

	// Rejected request shape (20026): distinct from unknown_error.
	f, s = mk(t, "<Result>20026</Result>")
	if st := s.ECLogin(context.Background()); st.Outcome != ECRequestRejected {
		t.Fatalf("20026 = %s, want request_rejected", st.Outcome)
	}

	// Bad credentials.
	f, s = mk(t, "Invalid username or password!")
	if st := s.ECLogin(context.Background()); st.Outcome != ECInvalidCredentials {
		t.Fatalf("20004 = %s, want invalid_credentials", st.Outcome)
	}

	// Unrecognised body.
	f, s = mk(t, "<Result>0</Result>")
	if st := s.ECLogin(context.Background()); st.Outcome != ECUnknownError {
		t.Fatalf("unrecognised = %s, want unknown_error", st.Outcome)
	}
}
