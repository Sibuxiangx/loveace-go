package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/cache"
	"github.com/Sibuxiangx/loveace-go/server/internal/dto"
	"github.com/Sibuxiangx/loveace-go/server/internal/gate"
	"github.com/Sibuxiangx/loveace-go/server/internal/review"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// This file is the whole store-review path. The review account is one fixed
// login whose requests are answered from fixtures (server/internal/review):
// it never opens a school session, never writes the vault and never counts
// toward the school rejection cache. Every branch below is a no-op unless
// LOVEACED_REVIEW_ACCOUNT and LOVEACED_REVIEW_PASSWORD are both set.

// reviewAccount is the configured review login, bound to the user row it
// lives in. A nil *reviewAccount matches nothing.
type reviewAccount struct {
	id       string
	password []byte
	hash     string // vault.AccountHash of id: the review user's row key
}

func newReviewAccount(d Deps) *reviewAccount {
	if d.Cfg.ReviewAccount == "" || d.Cfg.ReviewPassword == "" {
		return nil
	}
	return &reviewAccount{
		id:       d.Cfg.ReviewAccount,
		password: []byte(d.Cfg.ReviewPassword),
		hash:     vault.AccountHash(d.Pepper, d.Cfg.ReviewAccount),
	}
}

func (r *reviewAccount) matches(account string) bool {
	return r != nil && account == r.id
}

func (r *reviewAccount) checkPassword(password string) bool {
	return r != nil && subtle.ConstantTimeCompare([]byte(password), r.password) == 1
}

// reviewed reports whether userID is the review account. A store error answers
// false: the normal path then decides, and fails, on its own.
func (s *Server) reviewed(ctx context.Context, userID string) bool {
	if s.reviewer == nil {
		return false
	}
	u, err := s.deps.Store.GetUser(ctx, userID)
	return err == nil && u.AccountHash == s.reviewer.hash
}

// loginReview signs the review account in without the school. The password is
// checked against the configured secret; no vault entry, school session or
// rejection record is created.
func (s *Server) loginReview(ctx context.Context, account, password, deviceName, platform string) (*dto.TokenPair, error) {
	if !s.reviewer.checkPassword(password) {
		return nil, newProblem(http.StatusUnauthorized, "school.credentials_invalid", "账号或密码错误")
	}
	u, _, err := s.deps.Store.GetOrCreateUser(ctx, s.reviewer.hash)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := s.deps.Store.SetCredentialState(ctx, u.ID, store.CredValid); err != nil {
		return nil, mapErr(err)
	}
	device, err := s.deps.Store.CreateDevice(ctx, u.ID, deviceName, platform)
	if err != nil {
		return nil, mapErr(err)
	}
	pair, err := s.newTokenPair(ctx, u.ID, device.ID, s.now())
	if err != nil {
		return nil, mapErr(err)
	}
	pair.User = s.userView(ctx, u.ID)
	pair.User.AccountMasked = gate.MaskedAccount(account)
	_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: s.now(), UserID: u.ID, DeviceID: device.ID, Event: "login.review",
	})
	return pair, nil
}

// academicReads is the school surface the academic endpoints read through.
// *school.Manager serves students; review.Reads serves the review account.
type academicReads interface {
	AcademicSummary(ctx context.Context, userID, deviceID string) (*jwc.AcademicInfo, error)
	Terms(ctx context.Context, userID, deviceID string) ([]jwc.TermItem, error)
	TermScores(ctx context.Context, userID, deviceID, termCode string) (*jwc.TermScoreResponse, error)
	PlanCompletion(ctx context.Context, userID, deviceID string) (*jwc.PlanCompletionOutcome, error)
	TrainingPlan(ctx context.Context, userID, deviceID string) (*jwc.TrainingPlanInfo, error)
	Exams(ctx context.Context, userID, deviceID string) (*jwc.ExamInfoResponse, error)
	Timetable(ctx context.Context, userID, deviceID, termCode string) (*jwc.StudentScheduleResponse, error)
}

// academicFor picks where an academic read comes from. The review account
// never reaches ensureSession, so it cannot trigger a school login.
func (s *Server) academicFor(ctx context.Context, claims token.AccessClaims) (academicReads, error) {
	if s.reviewed(ctx, claims.UserID) {
		return review.Reads{}, nil
	}
	if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
		return nil, err
	}
	return s.deps.School, nil
}

// reviewEnvelope wraps a fixture in the envelope a live read gets.
func reviewEnvelope(v any) *Output[dto.Envelope] {
	return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(cache.Entry{
		Value: v, FetchedAt: time.Now(), Source: "live",
	})}
}

// reviewRunOp answers a core operation for the review account. Reads come from
// fixtures. Writes are either the canned success or refused. The idempotency
// key is still required, so the client contract matches a student's.
func reviewRunOp(name string, mutates bool, idemKey string) (int, any, error) {
	if !mutates {
		out, ok := review.ReadOp(name)
		if !ok {
			return 0, nil, newProblem(http.StatusNotFound, "not_found", "审核账号不支持这个操作")
		}
		return http.StatusOK, out, nil
	}
	if idemKey == "" {
		return 0, nil, newProblem(http.StatusPreconditionRequired, "idempotency_key_required", "写操作必须携带 Idempotency-Key 头")
	}
	out, ok := review.WriteOp(name)
	if !ok {
		return 0, nil, refuseReviewWrite()
	}
	return http.StatusOK, out, nil
}

// refuseReviewWrite answers a write the review account may not run.
func refuseReviewWrite() error {
	return newProblem(http.StatusForbidden, "review.write_refused", review.Refused)
}
