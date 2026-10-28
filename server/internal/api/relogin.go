package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// ensureSession guarantees a live school session before a school-backed
// read. After a restart (school sessions are in-memory) the first request
// silently re-logs-in from the vaulted credentials — via EC-pool
// derivation when possible, never a gateway login storm (DESIGN §7.2
// step 6, §8). The recovered session joins the user's group bound to the
// requesting device.
func (s *Server) ensureSession(ctx context.Context, userID, deviceID string) error {
	if _, err := s.deps.School.SessionFor(ctx, userID, deviceID); err == nil {
		return nil
	}
	// One-fail-stop (§5.2): invalid credentials never retry.
	if u, err := s.deps.Store.GetUser(ctx, userID); err == nil && u.CredentialState == store.CredInvalid {
		return newProblem(http.StatusUnauthorized, "school.credentials_invalid",
			"学校密码已失效，请重新输入密码")
	}
	cred, err := s.deps.Store.GetCredential(ctx, userID)
	if err != nil {
		return newProblem(http.StatusUnauthorized, "auth.school_login_required",
			"没有已保存的凭据，请重新登录")
	}
	plaintext, err := s.deps.Vault.Decrypt(userID, cred.Blob)
	if err != nil {
		return newProblem(http.StatusUnauthorized, "auth.school_login_required",
			"已保存凭据不可用，请重新登录")
	}
	var payload vault.CredPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil || payload.Account == "" {
		return newProblem(http.StatusUnauthorized, "auth.school_login_required",
			"已保存凭据不可用，请重新登录")
	}
	gw := payload.GatewayPassword
	if gw == "" {
		gw = payload.Password
	}
	// ReloginVaulted: derivation-first (a stale password dies at the UAAP
	// layer, never at the EC gateway) and ladder-free (a school-side
	// password change must not cool the user out of their own next login).
	sess, err := s.deps.School.ReloginVaulted(ctx, userID, deviceID, payload.Account, payload.Password, gw)
	if err != nil {
		if errs.CodeOf(err) == errs.InvalidCredentials {
			_ = s.deps.Store.SetCredentialState(ctx, userID, store.CredInvalid)
			_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
				At: s.now(), UserID: userID, Event: "relogin.invalid_credentials",
			})
		}
		return mapErr(err)
	}
	if deviceID != "" {
		s.deps.School.AttachDevice(userID, deviceID, "", sess)
	}
	_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
		At: s.now(), UserID: userID, Event: "relogin.success",
	})
	return nil
}
