package school

import (
	"context"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// 20041 on a submitted code is the gateway's attack-suspicion throttle. It
// judges neither the code nor the credentials, so the loop must stop at once:
// no further submission goes into the throttle, no accepted or rejected
// verdict is recorded (no gold or negative sample, no breaker input), and the
// caller gets the retryable 429 rather than another captcha prompt.
func TestCaptchaThrottleStopsLoopWithoutVerdict(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	gw.setThrottleOnCode(true)
	rec := &fakeCaptchaRecorder{}
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{"ab12"}}),
		WithCaptchaRecorder(rec))

	_, err := m.Login(context.Background(), "u1", "20000002", "Passw0rd!123", "", "dev", "test")
	if errs.CodeOf(err) != errs.MaybeAttacked {
		t.Fatalf("throttle must surface as maybe_attacked, got %v", err)
	}
	st := m.CaptchaStats()
	if st.Rounds != 1 {
		t.Fatalf("loop must stop after the throttled round: rounds = %d, want 1", st.Rounds)
	}
	if st.Accepted != 0 || st.Rejected != 0 {
		t.Fatalf("throttle is not a verdict: accepted=%d rejected=%d", st.Accepted, st.Rejected)
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("throttle must not feed the captcha store: %d samples", n)
	}
}
