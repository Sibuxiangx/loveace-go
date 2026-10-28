package school

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// fakeCaptchaSolver 按序吐码;"" 表示解码无效(ok=false)。
type fakeCaptchaSolver struct {
	mu    sync.Mutex
	codes []string
	pos   int
}

func (f *fakeCaptchaSolver) Solve(_ []byte) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pos >= len(f.codes) {
		// 耗尽后重复最后一个码("永远错"/"永远对"语义);全空则永远无效。
		if len(f.codes) == 0 {
			return "", false, nil
		}
		c := f.codes[len(f.codes)-1]
		if c == "" {
			return "", false, nil
		}
		return c, true, nil
	}
	c := f.codes[f.pos]
	f.pos++
	if c == "" {
		return "", false, nil
	}
	return c, true, nil
}

type capturedSolve struct {
	image    []byte
	code     string
	accepted bool
}

type fakeCaptchaRecorder struct {
	mu   sync.Mutex
	recs []capturedSolve
}

func (f *fakeCaptchaRecorder) Record(image []byte, code string, accepted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]byte(nil), image...)
	f.recs = append(f.recs, capturedSolve{image: cp, code: code, accepted: accepted})
}

func (f *fakeCaptchaRecorder) all() []capturedSolve {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedSolve(nil), f.recs...)
}

// TestCaptchaAutoSolveSucceeds:撞验证码时自动"取图→解→提交"一次通过,
// 不产生用户挑战,判卷结果被沉淀为金标。
func TestCaptchaAutoSolveSucceeds(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	rec := &fakeCaptchaRecorder{}
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{"ab12"}}),
		WithCaptchaRecorder(rec))

	if _, err := m.Login(context.Background(), "u1", "20000002", "Passw0rd!123", "", "dev", "test"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if gw.posts != 1 {
		t.Fatalf("应恰好 1 次提交,实际 %d", gw.posts)
	}
	st := m.CaptchaStats()
	if st.AutoSolved != 1 || st.Rounds != 1 || st.Accepted != 1 || st.Rejected != 0 {
		t.Fatalf("统计不符: %+v", st)
	}
	recs := rec.all()
	if len(recs) != 1 || !recs[0].accepted || recs[0].code != "ab12" || len(recs[0].image) == 0 {
		t.Fatalf("金标沉淀不符: %+v", recs)
	}
}

// TestCaptchaAutoSolveRetriesOnRejection:判错立即换新挑战(挑战一次性),
// 第 3 次通过;金标/困难负样本各自沉淀。
func TestCaptchaAutoSolveRetriesOnRejection(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	rec := &fakeCaptchaRecorder{}
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{"zzzz", "yyyy", "ab12"}}),
		WithCaptchaRecorder(rec))

	if _, err := m.Login(context.Background(), "u1", "20000002", "Passw0rd!123", "", "dev", "test"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if gw.posts != 3 {
		t.Fatalf("应 3 次提交,实际 %d", gw.posts)
	}
	st := m.CaptchaStats()
	if st.Rounds != 3 || st.Accepted != 1 || st.Rejected != 2 || st.AutoSolved != 1 {
		t.Fatalf("统计不符: %+v", st)
	}
	recs := rec.all()
	if len(recs) != 3 {
		t.Fatalf("应沉淀 3 条,实际 %d", len(recs))
	}
	if recs[0].accepted || recs[1].accepted || !recs[2].accepted {
		t.Fatalf("沉淀顺序/结论不符: %+v", recs)
	}
	if recs[2].code != "ab12" {
		t.Fatalf("金标码应为 ab12: %q", recs[2].code)
	}
}

// TestCaptchaInvalidDecodeNeverSubmits:解码全部无效 → 弃图不提交(零失败
// 额度消耗),轮次耗尽回退人工挑战路径,且人工路径仍可用。
func TestCaptchaInvalidDecodeNeverSubmits(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{"", "", ""}}),
		WithCaptchaLoop(3, 5))

	_, err := m.Login(context.Background(), "u1", "20000002", "Passw0rd!123", "", "dev", "test")
	var ce *ChallengeError
	if !errors.As(err, &ce) {
		t.Fatalf("应回退人工挑战,实际 %v", err)
	}
	if gw.posts != 0 {
		t.Fatalf("解码无效不应提交,实际 %d 次", gw.posts)
	}
	img, err := m.ChallengeImage(context.Background(), ce.ID)
	if err != nil {
		t.Fatalf("ChallengeImage: %v", err)
	}
	if len(img) == 0 {
		t.Fatalf("挑战图应非空")
	}
	s, err := m.SolveChallenge(context.Background(), ce.ID, "ab12")
	if err != nil {
		t.Fatalf("SolveChallenge: %v", err)
	}
	if s == nil {
		t.Fatalf("应注册会话")
	}
}

// TestCaptchaBreakerOpensAndRecovers:接受率崩塌 → 熔断开闸(直接走人工
// 路径,不再烧提交),期满自动闭合恢复自动解。
func TestCaptchaBreakerOpensAndRecovers(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{"zzzz"}}),
		WithCaptchaLoop(8, 2),
		WithCaptchaBreaker(3, 0.01, 80*time.Millisecond))

	// 两次登录各烧 2 次提交(全错) → 连续 4 错 ≥ 窗口 3 → 第二次登录期间熔断。
	for i := 0; i < 2; i++ {
		_, err := m.Login(context.Background(), fmt.Sprintf("u%d", i), "20000002", "Passw0rd!123", "", "dev", "test")
		var ce *ChallengeError
		if !errors.As(err, &ce) {
			t.Fatalf("login %d 应回退人工挑战,实际 %v", i, err)
		}
	}
	if got := gw.posts; got != 4 {
		t.Fatalf("两次登录应共 4 次提交,实际 %d", got)
	}
	if !m.CaptchaStats().BreakerOpen {
		t.Fatalf("熔断应处于开闸")
	}
	if m.CaptchaStats().BreakerOpens != 1 {
		t.Fatalf("开闸翻转应恰 1 次,实际 %d", m.CaptchaStats().BreakerOpens)
	}
	// 开闸期间:直接人工路径,零提交。
	_, err := m.Login(context.Background(), "u9", "20000002", "Passw0rd!123", "", "dev", "test")
	var ce *ChallengeError
	if !errors.As(err, &ce) {
		t.Fatalf("开闸期间应直接回退人工,实际 %v", err)
	}
	if got := gw.posts; got != 4 {
		t.Fatalf("开闸期间不应再提交,实际 %d", got)
	}
	// 期满闭合:自动解恢复(继续判错,但提交恢复)。
	time.Sleep(100 * time.Millisecond)
	if m.CaptchaStats().BreakerOpen {
		t.Fatalf("开闸期满应闭合")
	}
	_, err = m.Login(context.Background(), "u10", "20000002", "Passw0rd!123", "", "dev", "test")
	if !errors.As(err, &ce) {
		t.Fatalf("恢复后仍应回退人工(码全错),实际 %v", err)
	}
	if got := gw.posts; got != 6 {
		t.Fatalf("恢复后应再烧 2 次提交,实际 %d", got)
	}
}

// TestUserSolveRecordedAsGolden:人工手解通过的码 = 学校确认的金标,连同
// 图片一起沉淀。
func TestUserSolveRecordedAsGolden(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setCaptcha(true)
	gw.setCaptchaCode("ab12")
	rec := &fakeCaptchaRecorder{}
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(),
		WithCaptchaSolver(&fakeCaptchaSolver{codes: []string{""}}),
		WithCaptchaRecorder(rec))

	_, err := m.Login(context.Background(), "u1", "20000002", "Passw0rd!123", "", "dev", "test")
	var ce *ChallengeError
	if !errors.As(err, &ce) {
		t.Fatalf("应产生人工挑战,实际 %v", err)
	}
	img, err := m.ChallengeImage(context.Background(), ce.ID)
	if err != nil {
		t.Fatalf("ChallengeImage: %v", err)
	}
	if _, err := m.SolveChallenge(context.Background(), ce.ID, "ab12"); err != nil {
		t.Fatalf("SolveChallenge: %v", err)
	}
	recs := rec.all()
	if len(recs) != 1 || !recs[0].accepted || recs[0].code != "ab12" {
		t.Fatalf("人工金标沉淀不符: %+v", recs)
	}
	if string(recs[0].image) != string(img) {
		t.Fatalf("沉淀的图应与挑战图一致")
	}
}
