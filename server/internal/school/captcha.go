package school

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// ===== 验证码三循环(验证码原生化 M2) =====
//
// L1 尝试环(单挑战):取图 → 解 → 提交。挑战一次性(2026-10-20 实测:
// 同一 TWFID 二次提交必回 auth 页),判错即弃,一图一提交。
// L2 登录环(单次 EC 登录内):20023 → 立即取新挑战重解重提;解码无效的图
// 直接弃掉(不烧失败额度)。上限 capFetches / capRounds。
// L3 会话环 + 熔断:轮次耗尽回退人工挑战路径(ChallengeError);滑动窗口
// 接受率跌破阈值(网关换验证码样式 / 模型失效)开闸暂停自动解,期满自愈。
//
// 依据(loveace-go/AGENTS.md §5):网关先验码后验凭据,20023=码错,
// 20004=码对(凭据被拒);每次提交(对错都算)是一次失败登录,污染自续、
// 停手 ~3 分钟自愈 —— 所以"在验证码模式里工作"是可持续的。

// CaptchaSolver 把验证码图解成提交码。ok=false 表示解码无效(非 4 位或出
// 实测 31 字符集),调用方弃图重取,不应提交。
type CaptchaSolver interface {
	Solve(image []byte) (code string, ok bool, err error)
}

// CaptchaRecorder 在每次有结论的提交(自动解或人工手解)后回调:
// accepted=true 表示码被网关接受(金标签),false 表示被拒(困难负样本)。
type CaptchaRecorder interface {
	Record(image []byte, code string, accepted bool)
}

// 自动解默认参数。
const (
	// DefaultCaptchaFetches:单次登录最多取几张挑战图(含解码无效弃图)。
	DefaultCaptchaFetches = 8
	// DefaultCaptchaRounds:单次登录最多提交几个码。按 34.8% 单发接受率,
	// 5 轮内至少一次通过的概率 ≈ 88%;剩余回退人工挑战。
	DefaultCaptchaRounds = 5
	// DefaultCaptchaWindow:熔断滑动窗口(次)。
	DefaultCaptchaWindow = 20
	// DefaultCaptchaMinRate:窗口内最低接受率(≈ 模型实测水平的一半)。
	DefaultCaptchaMinRate = 0.15
	// DefaultCaptchaOpenFor:熔断开闸时长(期间直接走人工挑战路径)。
	DefaultCaptchaOpenFor = 10 * time.Minute
)

// WithCaptchaSolver 启用撞验证码的自动解环。nil(缺省)= 旧行为:一律回退
// 人工挑战路径。
func WithCaptchaSolver(s CaptchaSolver) Option {
	return func(m *Manager) { m.captchaSolver = s }
}

// WithCaptchaRecorder 挂接判卷结果沉淀(金标/困难负样本)。
func WithCaptchaRecorder(r CaptchaRecorder) Option {
	return func(m *Manager) { m.captchaRecorder = r }
}

// WithCaptchaLoop 覆盖自动解上限(调参/测试)。
func WithCaptchaLoop(maxFetches, maxRounds int) Option {
	return func(m *Manager) {
		if maxFetches > 0 {
			m.capFetches = maxFetches
		}
		if maxRounds > 0 {
			m.capRounds = maxRounds
		}
	}
}

// WithCaptchaBreaker 覆盖熔断参数(调参/测试)。
func WithCaptchaBreaker(windowSize int, minRate float64, openFor time.Duration) Option {
	return func(m *Manager) {
		if windowSize > 0 {
			m.br.windowSize = windowSize
		}
		if minRate > 0 {
			m.br.minRate = minRate
		}
		if openFor > 0 {
			m.br.openFor = openFor
		}
	}
}

// captchaBreaker 是 L3 熔断器:窗口内提交接受率过低或连续全错时开闸
// (暂停自动解),开闸期满自动闭合。窗口记账包含人工手解的提交。
type captchaBreaker struct {
	mu          sync.Mutex
	window      []bool
	pos, count  int
	consecWrong int
	openedUntil time.Time
	windowSize  int
	minRate     float64
	openFor     time.Duration
	now         func() time.Time
}

// record 记一次提交结果;返回是否刚发生"闭合→开闸"的翻转(供计数)。
func (b *captchaBreaker) record(accepted bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.window) == 0 {
		b.window = make([]bool, b.windowSize)
	}
	b.window[b.pos] = accepted
	b.pos = (b.pos + 1) % len(b.window)
	if b.count < len(b.window) {
		b.count++
	}
	if accepted {
		b.consecWrong = 0
	} else {
		b.consecWrong++
	}
	alreadyOpen := b.isOpenLocked()
	if alreadyOpen {
		return false
	}
	open := false
	if b.count == len(b.window) {
		sum := 0
		for _, v := range b.window {
			if v {
				sum++
			}
		}
		open = float64(sum)/float64(len(b.window)) < b.minRate
	}
	if !open && b.consecWrong >= len(b.window) {
		open = true
	}
	if open {
		b.openedUntil = b.now().Add(b.openFor)
	}
	return open
}

func (b *captchaBreaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isOpenLocked()
}

func (b *captchaBreaker) isOpenLocked() bool {
	return b.now().Before(b.openedUntil)
}

// autoSolveCaptcha 是 L2 登录环。返回 done=true 表示 EC 登录已完成;
// done=false 表示网关已不再要求验证码(污染自愈),调用方应重走一次普通
// ECLogin。invalid_credentials 照实透传(码已被接受后的凭据真判);其余
// 错误由 ecPhase 统一回退人工挑战路径。
func (m *Manager) autoSolveCaptcha(ctx context.Context, s *session.Session) (bool, error) {
	fetches, rounds := 0, 0
	for {
		if fetches >= m.capFetches || rounds >= m.capRounds {
			return false, errs.NewInteractionRequired("captcha rounds exhausted")
		}
		ch, err := s.EcCaptcha(ctx)
		if err != nil {
			return false, err
		}
		if !ch.Required || len(ch.Image) == 0 {
			return false, nil // 网关不再要码:重走普通登录
		}
		fetches++
		code, ok, err := m.captchaSolver.Solve(ch.Image)
		if err != nil {
			return false, err // 求解器故障 → 回退人工挑战
		}
		if !ok {
			continue // 解码无效:弃图不提交(不烧失败额度)
		}
		// 一次提交 = 一次网关登录:与全量登录同闸(§7.1)。
		if err := m.gateEC(ctx); err != nil {
			return false, err
		}
		rounds++
		atomic.AddInt64(&m.captcha.rounds, 1)
		st := s.ECLoginWithCaptcha(ctx, code)
		switch st.Outcome {
		case session.ECSuccess:
			m.noteCaptchaVerdict(ch.Image, code, true)
			atomic.AddInt64(&m.captcha.autoSolved, 1)
			return true, nil
		case session.ECCaptchaInvalid:
			m.noteCaptchaVerdict(ch.Image, code, false)
			continue // 挑战一次性:立即换新挑战
		case session.ECInvalidCredentials:
			m.noteCaptchaVerdict(ch.Image, code, true)
			return false, errs.NewInvalidCredentials()
		case session.ECCaptchaRequired:
			m.noteCaptchaVerdict(ch.Image, code, false)
			continue
		case session.ECMaybeAttacked:
			// Attack-suspicion throttle: the gateway judged neither the code nor
			// the credentials. No verdict is recorded and no further submission
			// goes into the throttle; the caller gets a retryable 429.
			return false, errs.NewMaybeAttacked()
		case session.ECRequestRejected:
			// 20026: the request shape was refused, so there is no verdict.
			return false, errs.NewTemporarilyUnavailable("gateway rejected the captcha login request (20026)")
		case session.ECNetworkError:
			return false, errs.NewTemporarilyUnavailable("gateway unreachable during captcha login")
		default:
			m.noteCaptchaVerdict(ch.Image, code, false)
			return false, errs.NewTemporarilyUnavailable("gateway login failed: " + string(st.Outcome))
		}
	}
}

// noteCaptchaVerdict 记账(计数器 + 熔断窗口)并喂沉淀钩子(有图才喂)。
func (m *Manager) noteCaptchaVerdict(img []byte, code string, accepted bool) {
	if opened := m.br.record(accepted); opened {
		atomic.AddInt64(&m.captcha.breakerOpens, 1)
	}
	if accepted {
		atomic.AddInt64(&m.captcha.accepted, 1)
	} else {
		atomic.AddInt64(&m.captcha.rejected, 1)
	}
	if m.captchaRecorder != nil && len(img) > 0 {
		m.captchaRecorder.Record(img, code, accepted)
	}
}

// CaptchaStats 汇总三循环运行计数(admin health 观测用)。
type CaptchaStats struct {
	AutoSolved   int64 // 自动解完成的登录数
	Rounds       int64 // 自动解提交次数
	Accepted     int64 // 被网关接受的提交(金标)
	Rejected     int64 // 被拒提交(困难负样本)
	BreakerOpens int64 // 熔断开闸次数
	BreakerOpen  bool  // 当前是否开闸(人工挑战回退中)
}

func (m *Manager) CaptchaStats() CaptchaStats {
	return CaptchaStats{
		AutoSolved:   atomic.LoadInt64(&m.captcha.autoSolved),
		Rounds:       atomic.LoadInt64(&m.captcha.rounds),
		Accepted:     atomic.LoadInt64(&m.captcha.accepted),
		Rejected:     atomic.LoadInt64(&m.captcha.rejected),
		BreakerOpens: atomic.LoadInt64(&m.captcha.breakerOpens),
		BreakerOpen:  m.br.isOpen(),
	}
}
