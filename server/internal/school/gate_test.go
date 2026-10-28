package school

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"golang.org/x/time/rate"
)

// chaosGateway is a scriptable school for hardening scenarios: it counts
// gateway touches (auth page GETs, credential POSTs) and can switch into
// captcha-required or down modes.
type chaosGateway struct {
	mu             sync.Mutex
	cap            bool   // auth page demands a CAPTCHA (RndImg=1)
	captchaCode    string // 非空时:cap 模式下 login_psw 携带该码 → 成功,否则 20023
	down           bool   // the whole gateway 502s
	noRewrite      bool   // aged-TWFID mode: proxying continues, rewriting stops
	passwordChange bool   // UAAP intercepts the login with the password-change flow
	badCreds       bool   // EC credential POST returns 20004 (invalid credentials)
	badCAS         bool   // CAS identity POST returns the invalid-credentials page
	throttle       bool   // a submitted CAPTCHA code draws 20041 (attack-suspicion throttle)
	auths          int    // GET /por/login_auth.csp (login attempts + heartbeats)
	posts          int    // POST /por/login_psw.csp (credential submissions)
	casPosts       int64  // total CAS identity POSTs (atomic; probe accounting)
	aacMints       int64  // CAS GETs of the aac mint service (atomic; walk accounting)

	modulus  string
	exponent string

	casDelay time.Duration // CAS POST latency (derive-herd scenarios)
	casNow   int64         // in-flight CAS POSTs (atomic)
	casMax   int64         // observed max in-flight CAS POSTs (atomic)
}

func newChaosGateway(t *testing.T) *chaosGateway {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return &chaosGateway{
		modulus:  hex.EncodeToString(key.PublicKey.N.Bytes()),
		exponent: fmt.Sprint(key.PublicKey.E),
	}
}

func (g *chaosGateway) Execute(_ context.Context, r *transport.Request) (*transport.Response, error) {
	g.mu.Lock()
	switch {
	case strings.Contains(r.URL, "/por/login_auth.csp"):
		g.auths++
		down, captcha := g.down, g.cap
		g.mu.Unlock()
		if down {
			return transport.NewResponse(http.StatusBadGateway, []byte("bad gateway")), nil
		}
		img := "0"
		if captcha {
			img = "1"
		}
		body := "<auth><TwfID>t1</TwfID><RSA_ENCRYPT_KEY>" + g.modulus +
			"</RSA_ENCRYPT_KEY><RSA_ENCRYPT_EXP>" + g.exponent +
			"</RSA_ENCRYPT_EXP><CSRF_RAND_CODE>c1</CSRF_RAND_CODE>" +
			"<RndImg>" + img + "</RndImg></auth>"
		return transport.NewResponse(200, []byte(body)), nil
	case strings.Contains(r.URL, "/por/login_psw.csp"):
		g.posts++
		down, bad, cap, want, thr := g.down, g.badCreds, g.cap, g.captchaCode, g.throttle
		g.mu.Unlock()
		if down {
			return transport.NewResponse(http.StatusBadGateway, []byte("bad gateway")), nil
		}
		if thr && cap && !strings.Contains(string(r.Body), "svpn_rand_code=&") {
			// 20041 on a submitted code: the gateway refuses to judge it.
			return transport.NewResponse(200, []byte("<Result>20041</Result>")), nil
		}
		if bad {
			return transport.NewResponse(200, []byte("<Result>20004</Result>")), nil
		}
		if cap && want != "" {
			// 验证码模式:先验码后验凭据(2026-10-20 实测的网关顺序);
			// 码错 → 20023,码对 → 放行凭据检查(此处凭据恒对)。
			if strings.Contains(string(r.Body), "svpn_rand_code="+want) {
				return transport.NewResponse(200, []byte("<Result>1</Result>")), nil
			}
			return transport.NewResponse(200, []byte("<Result>20023</Result>")), nil
		}
		return transport.NewResponse(200, []byte("<Result>1</Result>")), nil
	case strings.Contains(r.URL, "/por/rand_code.csp"):
		g.mu.Unlock()
		return transport.NewResponse(200, []byte("GIF89a-fake-captcha-image")), nil
	case strings.Contains(r.URL, "j_spring_cas_security_check?ticket="):
		g.mu.Unlock()
		return transport.NewResponse(200, []byte("<html>jwc</html>")), nil
	case strings.Contains(r.URL, "cas/login"):
		g.mu.Unlock()
		if r.Method == transport.Post {
			atomic.AddInt64(&g.casPosts, 1)
			cur := atomic.AddInt64(&g.casNow, 1)
			for {
				max := atomic.LoadInt64(&g.casMax)
				if cur <= max || atomic.CompareAndSwapInt64(&g.casMax, max, cur) {
					break
				}
			}
			time.Sleep(g.casDelay)
			atomic.AddInt64(&g.casNow, -1)
			g.mu.Lock()
			pwChange, badCAS := g.passwordChange, g.badCAS
			g.mu.Unlock()
			if pwChange {
				// The credentials were accepted (a TGT exists) but the CAS
				// issues no ticket until the password is changed.
				return transport.NewResponse(302, []byte("moved")).
					WithHeader("Location", "http://uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/mng/acp/completeInfo/password/view"+
						"?service=http%3A%2F%2Fjwcxk2.aufe.edu.cn%2Fj_spring_cas_security_check&CTgtId=TGT-506-x-http%3A%2F%2F192.0.2.10%3A8080%2Fcas"), nil
			}
			if badCAS {
				return transport.NewResponse(200, []byte(
					"<html>Invalid username or password!</html>")), nil
			}
			return transport.NewResponse(302, []byte("moved")).
				WithHeader("Location", "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/j_spring_cas_security_check?ticket=ST1"), nil
		}
		// A CAS GET for the AAC mint service redirects to the service; the
		// gateway REWRITES the Location into the vpn2 namespace while the
		// TWFID is rewrite-alive. In noRewrite mode (the aged-TWFID truth:
		// proxying continues, rewriting stops) the raw internal host leaks.
		// The lowercase %3a keeps the UAAP login URL (uppercase %3A) on the
		// plain-page route below.
		if strings.Contains(r.URL, "service=http%3a%2f%2fapi.dekt") {
			atomic.AddInt64(&g.aacMints, 1)
			loc := "http://api.dekt.ac.acxk.net/User/Index/CoreLoginCallback?ticket=ST-1"
			g.mu.Lock()
			live := !g.noRewrite
			g.mu.Unlock()
			if live {
				loc = "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/User/Index/CoreLoginCallback?ticket=ST-1"
			}
			return transport.NewResponse(302, []byte("moved")).WithHeader("Location", loc), nil
		}
		page := `<html><form action="/cas/login" method="post">
			<input type="hidden" name="lt" value="LT-1"/>
			<input type="hidden" name="execution" value="E-1"/></form></html>`
		return transport.NewResponse(200, []byte(page)), nil
	default:
		g.mu.Unlock()
		return transport.NewResponse(404, []byte("not found")), nil
	}
}

func (g *chaosGateway) counts() (auths, posts int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.auths, g.posts
}

// casPostsTotal returns the cumulative CAS identity POSTs (borrow probes +
// logins), the signal the validation-cache tests measure.
func (g *chaosGateway) casPostsTotal() int64 { return atomic.LoadInt64(&g.casPosts) }

// aacMintsTotal returns the cumulative aac mint-walk requests.
func (g *chaosGateway) aacMintsTotal() int64 { return atomic.LoadInt64(&g.aacMints) }

func (g *chaosGateway) setCaptcha(v bool) { g.mu.Lock(); g.cap = v; g.mu.Unlock() }
func (g *chaosGateway) setCaptchaCode(code string) {
	g.mu.Lock()
	g.captchaCode = code
	g.mu.Unlock()
}
func (g *chaosGateway) setDown(v bool)           { g.mu.Lock(); g.down = v; g.mu.Unlock() }
func (g *chaosGateway) setThrottleOnCode(v bool) { g.mu.Lock(); g.throttle = v; g.mu.Unlock() }
func (g *chaosGateway) setNoRewrite(v bool) {
	g.mu.Lock()
	g.noRewrite = v
	g.mu.Unlock()
}
func (g *chaosGateway) setPasswordChange(v bool) {
	g.mu.Lock()
	g.passwordChange = v
	g.mu.Unlock()
}
func (g *chaosGateway) setBadCreds(v bool) {
	g.mu.Lock()
	g.badCreds = v
	g.mu.Unlock()
}
func (g *chaosGateway) setBadCAS(v bool) {
	g.mu.Lock()
	g.badCAS = v
	g.mu.Unlock()
}

// TestLoginGateSerializesGatewayLogins pins §7.1: concurrent full logins
// for distinct users leave the gate spaced by the limiter, never as a
// stampede. rate = 2/s burst 1 → 4 logins take ≥ ~1.5s.
func TestLoginGateSerializesGatewayLogins(t *testing.T) {
	gw := newChaosGateway(t)
	m := NewManager(func() transport.Transport { return gw },
		WithLoginGate(rate.NewLimiter(2.0, 1), 0))

	const users = 4
	var wg sync.WaitGroup
	start := time.Now()
	got := make([]error, users)
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, got[i] = m.Login(context.Background(),
				fmt.Sprintf("u%d", i), fmt.Sprintf("2024%04d", i), "Passw0rd!123", "", "dev", "test")
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i, err := range got {
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
	if elapsed < 1400*time.Millisecond {
		t.Fatalf("4 gated logins finished in %s; the gate did not serialize", elapsed)
	}
	auths, posts := gw.counts()
	if auths != users || posts != users {
		t.Fatalf("gateway touches = auth:%d post:%d, want %d/%d", auths, posts, users, users)
	}
}

// TestCaptchaStormStaysGated pins the storm path: every login hits the
// CAPTCHA challenge without submitting credentials, each solve is one
// gated credential POST, and everyone ends registered.
func TestCaptchaStormStaysGated(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	m := NewManager(func() transport.Transport { return gw },
		WithLoginGate(rate.NewLimiter(rate.Inf, 1), 0))

	const users = 3
	ids := make([]string, users)
	var wg sync.WaitGroup
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Login(context.Background(),
				fmt.Sprintf("u%d", i), fmt.Sprintf("2024%04d", i), "Passw0rd!123", "", "dev", "test")
			var ce *ChallengeError
			if err != nil && errors.As(err, &ce) {
				ids[i] = ce.ID
			}
		}(i)
	}
	wg.Wait()
	for i, id := range ids {
		if id == "" {
			t.Fatalf("user %d: expected a challenge", i)
		}
	}
	if auths, posts := gw.counts(); auths != users || posts != 0 {
		t.Fatalf("challenge phase touches = auth:%d post:%d, want %d/0", auths, posts, users)
	}

	for i, id := range ids {
		if _, err := m.SolveChallenge(context.Background(), id, "ab12"); err != nil {
			t.Fatalf("solve %d: %v", i, err)
		}
	}
	// ECLoginWithCaptcha re-fetches the auth page before submitting.
	if auths, posts := gw.counts(); auths != 2*users || posts != users {
		t.Fatalf("after solves: auth:%d post:%d, want %d/%d", auths, posts, 2*users, users)
	}
	for i := 0; i < users; i++ {
		if _, err := m.Session(fmt.Sprintf("u%d", i)); err != nil {
			t.Fatalf("session %d missing after solve: %v", i, err)
		}
	}
}

// TestGatewayOutage pins the school-down scenario: EC-level network
// failures surface as temporarily_unavailable, never create a challenge,
// never register a session, and recovery is a plain retry.
func TestGatewayOutage(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setDown(true)
	m := NewManager(func() transport.Transport { return gw }, WithoutLoginGate())

	_, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "dev", "test")
	if errs.CodeOf(err) != errs.TemporarilyUnavailable {
		t.Fatalf("outage login error = %v, want temporarily_unavailable", err)
	}
	if _, err := m.Session("u1"); err == nil {
		t.Fatal("outage must not register a session")
	}

	gw.setDown(false)
	if _, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "dev", "test"); err != nil {
		t.Fatalf("recovery login: %v", err)
	}
}
