package api

import (
	"errors"
	"math"
	"net/http"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/server/internal/gate"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
)

// Problem is the RFC 9457 problem+json body with our stable `code`
// extension (DESIGN.md §3.1). It is returned directly from handlers, so
// huma serializes every field.
type Problem struct {
	Type        string                `json:"type,omitempty"`
	Title       string                `json:"title"`
	Status      int                   `json:"status"`
	Detail      string                `json:"detail,omitempty"`
	Code        string                `json:"code"`
	ChallengeID string                `json:"challenge_id,omitempty"`
	TaskID      string                `json:"task_id,omitempty"`
	Violations  []gate.Violation      `json:"violations,omitempty"`
	Sessions    []school.SessionEntry `json:"sessions,omitempty"`
	// RetryAfter is a body-carried hint (seconds); huma error rendering
	// cannot set real headers on Problem returns.
	RetryAfter int `json:"retry_after,omitempty"`
}

func (p *Problem) Error() string  { return p.Detail }
func (p *Problem) GetStatus() int { return p.Status }

// ContentType upgrades error responses to the problem+json media type.
func (p *Problem) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

func newProblem(status int, code, detail string) *Problem {
	return &Problem{Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

// mapErr converts core and school errors into Problem responses. Stable
// codes come from DESIGN.md §3.1.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	var ce *school.ChallengeError
	if errors.As(err, &ce) {
		p := newProblem(http.StatusConflict, "school.captcha_required",
			"学校网关需要验证码，请取图后提交")
		p.ChallengeID = ce.ID
		return p
	}
	var le *school.SessionLimitError
	if errors.As(err, &le) {
		p := newProblem(http.StatusConflict, "auth.session_limit",
			"该账号的活跃学校会话已达上限；请先在其他设备退出，或删除不需要的设备释放额度")
		p.Sessions = le.Active
		return p
	}
	var cool *school.AccountCoolingError
	if errors.As(err, &cool) {
		p := newProblem(http.StatusTooManyRequests, "auth.account_cooling",
			"密码错误次数过多，账号已进入冷静期；请稍后再试")
		p.RetryAfter = int(math.Ceil(cool.RetryAfter.Seconds()))
		return p
	}
	switch {
	case errors.Is(err, school.ErrChallengeUnknownOrExpired):
		return newProblem(http.StatusNotFound, "not_found",
			"验证码挑战不存在或已过期，请重新登录")
	case errors.Is(err, school.ErrNoSession):
		return newProblem(http.StatusUnauthorized, "auth.school_login_required",
			"没有可用的学校会话，请重新登录")
	}
	switch code := errs.CodeOf(err); code {
	case errs.InvalidCredentials:
		return newProblem(http.StatusUnauthorized, "school.credentials_invalid",
			"账号或密码错误（学校侧已拒绝）")
	case errs.PasswordChangeRequired:
		return newProblem(http.StatusForbidden, "school.password_change_required",
			"学校要求修改密码：请自行登录智慧校园，按指引修改密码后重新登录")
	case errs.InteractionRequired:
		return newProblem(http.StatusConflict, "school.captcha_required",
			"学校网关需要验证码")
	case errs.Throttled, errs.MaybeAttacked:
		return newProblem(http.StatusTooManyRequests, "rate_limited",
			"请求过于频繁，请稍后再试")
	case errs.Network, errs.Timeout, errs.TemporarilyUnavailable:
		return newProblem(http.StatusServiceUnavailable, "school.unavailable",
			"学校服务暂时不可用，请稍后再试")
	case errs.Parse:
		return newProblem(http.StatusBadGateway, "school.parse_changed",
			"学校页面结构已变化，解析失败")
	case errs.Server:
		return newProblem(http.StatusBadGateway, "school.server_error",
			"学校服务返回错误")
	case errs.HTTP:
		return newProblem(http.StatusBadGateway, "school.http_error",
			"学校服务返回异常响应")
	case errs.OutcomeUnknown:
		return newProblem(http.StatusConflict, "school.outcome_unknown",
			"操作结果不确定，请稍后查询")
	case errs.AuthRequired:
		return newProblem(http.StatusUnauthorized, "auth.school_login_required",
			"学校会话已失效，请重新登录")
	case errs.Cancelled:
		return newProblem(http.StatusConflict, "cancelled", "请求已取消")
	case errs.NotFound:
		return newProblem(http.StatusNotFound, "not_found", "资源不存在")
	case errs.InvalidArgument, errs.NonAsciiCredential:
		return newProblem(http.StatusUnprocessableEntity, "auth.password_invalid_format",
			"凭据格式不合法")
	default:
		return newProblem(http.StatusInternalServerError, "error", "内部错误")
	}
}
