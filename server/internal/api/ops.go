package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/ops"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/danielgtaylor/huma/v2"
)

// idemPendingTTL bounds an in-flight reservation: past it, the original
// executor is assumed crashed. The outcome is then reported as unknown —
// the reservation is NEVER re-executed (a retried payment must not
// charge twice); the client reconciles (e.g. re-reads the purchase
// history).
const idemPendingTTL = 10 * time.Minute

// idemEnvelope is what gets stored per idempotency key: the response
// status plus body, so replays are stable.
type idemEnvelope struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// opRunInput is the generic operation request. The ops are the core
// registry's stable JSON surface (the same wire format the CLI speaks).
type opRunInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	IdempotencyKey   string `header:"Idempotency-Key" required:"false"`
	Body             struct {
		Op   string         `json:"op" minLength:"1" required:"true"`
		Args map[string]any `json:"args" required:"false"`
	} `body:""`
}

// registerOps wires POST /v1/me/ops (M5): every non-auth operation in
// the core registry is reachable through one authenticated endpoint.
// Reads dispatch directly. Writes (Mutates) REQUIRE an Idempotency-Key
// and reserve/finalize in the store — a retry replays the stored
// outcome, never re-executes. auth.* is unreachable: the server owns the
// login lifecycle (session groups, §7.5) and must not allow parallel
// school logins.
func (s *Server) registerOps(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "me-run-op",
		Summary:     "Run a core operation",
		Method:      http.MethodPost,
		Path:        "/v1/me/ops",
		Tags:        []string{"ops"},
	}, func(ctx context.Context, in *opRunInput) (*Output[any], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		status, body, runErr := s.runOp(ctx, claims.UserID, claims.DeviceID,
			in.Body.Op, in.Body.Args, in.IdempotencyKey)
		if runErr != nil {
			return nil, runErr
		}
		return &Output[any]{Status: status, Body: body}, nil
	})
}

// opsAuthz is the server-side op guard: everything except the auth
// lifecycle (managed by /v1/auth) is reachable.
func opsAuthz(name string) bool {
	return !strings.HasPrefix(name, "auth.")
}

// idempotencyHash binds a key to the exact request: same key with a
// different payload is a client bug and is rejected.
func idempotencyHash(op string, args []byte) string {
	sum := sha256.Sum256(append([]byte(op+"\x00"), args...))
	return hex.EncodeToString(sum[:])
}

// runOp executes one operation with the M5 semantics. Returns the HTTP
// status and body, or an error (Problem) to render.
func (s *Server) runOp(ctx context.Context, userID, deviceID, opName string, args map[string]any, idemKey string) (int, any, error) {
	spec, ok := ops.Find(opName)
	if !ok || !opsAuthz(opName) {
		return 0, nil, newProblem(http.StatusNotFound, "not_found", "没有这个操作")
	}
	argsJSON, err := json.Marshal(args)
	if err != nil || len(argsJSON) == 0 {
		argsJSON = []byte("{}")
	}

	if !spec.Mutates {
		out, err := s.dispatchOp(ctx, userID, deviceID, spec.Name, argsJSON)
		if err != nil {
			return 0, nil, mapErr(err)
		}
		return http.StatusOK, out, nil
	}

	// Write path: idempotency is mandatory (§6/M5).
	if idemKey == "" {
		return 0, nil, newProblem(http.StatusPreconditionRequired,
			"idempotency_key_required", "写操作必须携带 Idempotency-Key 头")
	}
	hash := idempotencyHash(spec.Name, argsJSON)
	prev, existed, err := s.deps.Store.ReserveIdempotency(ctx, store.IdempotencyRecord{
		Key: idemKey, UserID: userID, Op: spec.Name, RequestHash: hash, Status: "pending",
	})
	if err != nil {
		return 0, nil, mapErr(err)
	}
	if existed {
		return s.replayOrRefuse(ctx, userID, idemKey, spec.Name, prev, hash)
	}

	out, derr := s.dispatchOp(ctx, userID, deviceID, spec.Name, argsJSON)
	switch {
	case derr == nil:
		body, _ := json.Marshal(out)
		s.finalizeIdem(ctx, userID, idemKey, "done", idemEnvelope{Status: http.StatusOK, Body: body})
		return http.StatusOK, json.RawMessage(body), nil
	case errs.CodeOf(derr) == errs.OutcomeUnknown:
		p := mapErr(derr)
		s.finalizeIdem(ctx, userID, idemKey, "unknown", idemEnvelope{Status: problemStatus(p), Body: marshalProblem(p)})
		return 0, nil, p
	default:
		p := mapErr(derr)
		s.finalizeIdem(ctx, userID, idemKey, "failed", idemEnvelope{Status: problemStatus(p), Body: marshalProblem(p)})
		return 0, nil, p
	}
}

// dispatchOp runs the operation against the user's live session with the
// same recovery semantics as the typed endpoints.
func (s *Server) dispatchOp(ctx context.Context, userID, deviceID, opName string, argsJSON []byte) (any, error) {
	if err := s.ensureSession(ctx, userID, deviceID); err != nil {
		return nil, err
	}
	sess, err := s.deps.School.SessionFor(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	out, err := ops.Dispatch(ctx, sess, opName, argsJSON)
	if err != nil {
		log.Printf("[ops] user=%s op=%s err=%v", userID, opName, err)
		s.deps.School.MarkAuthFailed(userID, sess, err)
		return nil, err
	}
	return out, nil
}

// replayOrRefuse resolves a reservation that already existed.
func (s *Server) replayOrRefuse(ctx context.Context, userID, key, opName string, prev store.IdempotencyRecord, hash string) (int, any, error) {
	if prev.RequestHash != hash {
		return 0, nil, newProblem(http.StatusUnprocessableEntity,
			"idempotency_key_mismatch", "同一 Idempotency-Key 的请求参数不一致")
	}
	if prev.Status == "pending" {
		if time.Since(prev.CreatedAt) <= idemPendingTTL {
			return 0, nil, newProblem(http.StatusConflict,
				"idempotency_in_progress", "该写操作正在执行，请稍后再试")
		}
		// The original executor crashed. Report unknown; never re-execute.
		p := newProblem(http.StatusConflict, "school.outcome_unknown",
			"操作结果不确定（执行器中断），请稍后查询对账")
		s.finalizeIdem(ctx, userID, key, "unknown", idemEnvelope{Status: p.Status, Body: marshalProblem(p)})
		return 0, nil, p
	}
	if len(prev.Response) == 0 {
		return 0, nil, newProblem(http.StatusInternalServerError, "error", "幂等记录缺失结果")
	}
	var env idemEnvelope
	if err := json.Unmarshal(prev.Response, &env); err != nil {
		return 0, nil, newProblem(http.StatusInternalServerError, "error", "幂等记录不可读")
	}
	if env.Status == http.StatusOK {
		return http.StatusOK, env.Body, nil
	}
	var p Problem
	if err := json.Unmarshal(env.Body, &p); err == nil && p.Code != "" {
		return 0, nil, &p
	}
	return env.Status, env.Body, nil
}

func (s *Server) finalizeIdem(ctx context.Context, userID, key, status string, env idemEnvelope) {
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	_ = s.deps.Store.FinalizeIdempotency(ctx, key, userID, status, b) // best-effort
}

func marshalProblem(p error) json.RawMessage {
	var problem *Problem
	if errors.As(p, &problem) {
		b, _ := json.Marshal(problem)
		return b
	}
	return json.RawMessage(`{"title":"error","status":500,"code":"error"}`)
}
