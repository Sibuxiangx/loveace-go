package api

// Admin endpoints (DESIGN.md §9/§10): session-group + captcha-loop
// observability plus the passive-health view. Guarded by a fixed bearer
// token (LOVEACED_ADMIN_TOKEN). When no token is configured the routes are
// not registered at all — monitoring is opt-in, never accidentally open.

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/go-chi/chi/v5"
)

func (s *Server) registerAdmin(r chi.Router) {
	if s.deps.Cfg.AdminToken == "" {
		return
	}
	r.Route("/v1/admin", func(adm chi.Router) {
		adm.Use(s.adminGuard)
		adm.Get("/sessions", s.adminSessions)
		adm.Get("/health", s.adminHealth)
		adm.Get("/stats", s.adminStats)
		adm.Post("/health/maintenance", s.adminMaintenance)
		adm.Get("/teacher-eval/pool", s.adminTeachPool)
		adm.Post("/teacher-eval/pause", s.adminTeachPause)
	})
}

// adminGuard admits only the exact configured token (constant-time).
func (s *Server) adminGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), bearerPrefix)
		ok := presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(s.deps.Cfg.AdminToken)) == 1
		if !ok {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(newProblem(http.StatusUnauthorized, "auth.invalid_token", "管理员令牌无效"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminSessions exposes the session-group snapshots and the captcha
// three-loop counters (the pollution/acceptance view the operator needs —
// the pool-era proxy ledger is gone with the pool itself, M4).
// Cookie values and session identities never appear (§7.7 boundary 11).
func (s *Server) adminSessions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_groups": s.deps.School.GroupStats(),
		"captcha":        s.deps.School.CaptchaStats(),
	})
}

// adminHealth is the full §9/§10 view: layer states, counters and the captcha
// pollution timeline, plus the three-loop CaptchaStats that explain that
// timeline (auto-solve rounds, verdicts, breaker state). No user identifiers,
// no cookies.
func (s *Server) adminHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(adminHealthView{
		FullStatus: s.deps.Health.Status(),
		Captcha:    s.deps.School.CaptchaStats(),
	})
}

// adminHealthView is the admin health body: the layered status with the
// captcha three-loop counters next to the pollution timeline it explains.
type adminHealthView struct {
	health.FullStatus
	Captcha school.CaptchaStats `json:"captcha"`
}

type maintenanceInput struct {
	Layer   string `json:"layer"`
	Minutes int    `json:"minutes"`
}

// adminMaintenance marks a layer "维护中" (§9): {"layer":"jwc","minutes":30};
// minutes<=0 clears the override.
func (s *Server) adminMaintenance(w http.ResponseWriter, r *http.Request) {
	var in maintenanceInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Layer == "" {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(newProblem(http.StatusUnprocessableEntity, "invalid_argument", "需要 layer 与 minutes 字段"))
		return
	}
	s.deps.Health.SetMaintenance(in.Layer, in.Minutes)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "layer": in.Layer, "minutes": in.Minutes})
}

// adminStats exposes the §11.4 usage aggregates (per-op usage, DAU/MAU,
// login funnel). Aggregate only.
func (s *Server) adminStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.deps.Stats.Stats())
}
