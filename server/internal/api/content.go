package api

// Content endpoints (DESIGN.md §11.1/§11.2): announcements and the
// academic calendar.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/calendar"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/go-chi/chi/v5"

	"github.com/danielgtaylor/huma/v2"
)

// adminAuthz guards the huma-registered admin content endpoints (the chi
// adminGuard only covers the plain routes). Unconfigured token = the
// endpoints do not exist (404).
func (s *Server) adminAuthz(authz string) error {
	if s.deps.Cfg.AdminToken == "" {
		return newProblem(http.StatusNotFound, "not_found", "管理接口未启用")
	}
	presented := strings.TrimPrefix(authz, bearerPrefix)
	if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(s.deps.Cfg.AdminToken)) != 1 {
		return newProblem(http.StatusUnauthorized, "auth.invalid_token", "管理员令牌无效")
	}
	return nil
}

// ---- announcements (§11.2) ----

type announcementView struct {
	ID          string     `json:"id"`
	Level       string     `json:"level"`
	BodyMD      string     `json:"body_md"`
	Pinned      bool       `json:"pinned,omitempty"`
	RequiresAck bool       `json:"requires_ack,omitempty"`
	AckedAt     *time.Time `json:"acked_at,omitempty"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
}

// announcementVisible applies the display window/audience-agnostic part:
// active and inside [starts_at, ends_at].
func announcementVisible(a store.Announcement, now time.Time) bool {
	if !a.Active || a.Audience == "" {
		return false
	}
	if a.StartsAt.After(now) {
		return false
	}
	if a.EndsAt != nil && a.EndsAt.Before(now) {
		return false
	}
	return true
}

// registerPublicContent wires the pre-login announcement list.
func (s *Server) registerPublicContent(humaAPI huma.API) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "public-announcements",
		Summary:     "Pre-login announcements",
		Method:      http.MethodGet,
		Path:        "/v1/public/announcements",
		Tags:        []string{"public"},
	}, func(ctx context.Context, _ *struct{}) (*Output[[]announcementView], error) {
		rows, err := s.deps.Store.ListAnnouncements(ctx, true)
		if err != nil {
			return nil, mapErr(err)
		}
		now := s.now()
		out := []announcementView{}
		for _, a := range rows {
			if !announcementVisible(a, now) || (a.Audience != "pre_login" && a.Audience != "both") {
				continue
			}
			out = append(out, announcementView{ID: a.ID, Level: a.Level, BodyMD: a.BodyMD,
				Pinned: a.Pinned, StartsAt: a.StartsAt, EndsAt: a.EndsAt})
		}
		return &Output[[]announcementView]{Status: http.StatusOK, Body: out}, nil
	})
}

type myAnnouncementsInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
}

// registerMeContent wires the post-login announcements (with ack state)
// and the ack action.
func (s *Server) registerMeContent(humaAPI huma.API) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "my-announcements",
		Summary:     "Post-login announcements with ack state",
		Method:      http.MethodGet,
		Path:        "/v1/me/announcements",
		Tags:        []string{"me"},
	}, func(ctx context.Context, in *myAnnouncementsInput) (*Output[[]announcementView], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		rows, err := s.deps.Store.ListAnnouncements(ctx, true)
		if err != nil {
			return nil, mapErr(err)
		}
		acks, err := s.deps.Store.ListAcks(ctx, claims.UserID)
		if err != nil {
			return nil, mapErr(err)
		}
		now := s.now()
		out := []announcementView{}
		for _, a := range rows {
			if !announcementVisible(a, now) || (a.Audience != "post_login" && a.Audience != "both") {
				continue
			}
			view := announcementView{ID: a.ID, Level: a.Level, BodyMD: a.BodyMD,
				Pinned: a.Pinned, RequiresAck: a.RequiresAck, StartsAt: a.StartsAt, EndsAt: a.EndsAt}
			if at, ok := acks[a.ID]; ok {
				t := at
				view.AckedAt = &t
			}
			out = append(out, view)
		}
		return &Output[[]announcementView]{Status: http.StatusOK, Body: out}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "ack-announcement",
		Summary:     "Acknowledge an announcement",
		Method:      http.MethodPost,
		Path:        "/v1/me/announcements/{id}/ack",
		Tags:        []string{"me"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		ID               string `path:"id" minLength:"1" required:"false"`
	}) (*Output[struct{}], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.deps.Store.AckAnnouncement(ctx, claims.UserID, in.ID); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})
}

// ---- calendar (§11.1) ----

type calendarInput struct {
	TermID string `query:"term_id" minLength:"1" required:"false"`
	From   string `query:"from" minLength:"1" required:"false"`
	To     string `query:"to" minLength:"1" required:"false"`
}

// registerPublicCalendar wires the materialised day view as a plain
// route: only the published rule set renders, and the response carries a
// version ETag (If-None-Match → 304) plus the public cache header.
func (s *Server) registerPublicCalendar(r chi.Router) {
	r.Get("/v1/public/calendar", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		q := req.URL.Query()
		term, days, err := s.calendarDays(ctx, q.Get("term_id"), q.Get("from"), q.Get("to"), "published")
		if err != nil {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(problemStatus(err))
			_ = json.NewEncoder(w).Encode(mapErr(err))
			return
		}
		etag := `W/"cal-` + term.ID + `-v` + strconv.Itoa(term.Version) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", publicCacheTTLs["/v1/public/calendar"])
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(days)
	})
}

// problemStatus extracts the HTTP status from a Problem, if it is one.
func problemStatus(err error) int {
	var p *Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return http.StatusInternalServerError
}

// calendarDays resolves the term and materialises [from,to] with the
// rules of the given status.
func (s *Server) calendarDays(ctx context.Context, termID, fromStr, toStr, status string) (*store.CalendarTerm, []calendar.DayInfo, error) {
	terms, err := s.deps.Store.ListCalendarTerms(ctx)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	var term *store.CalendarTerm
	for i := range terms {
		if terms[i].ID == termID {
			term = &terms[i]
		}
	}
	if term == nil {
		return nil, nil, newProblem(http.StatusNotFound, "not_found", "没有这个学期的校历")
	}
	rules, err := s.deps.Store.ListCalendarRules(ctx, term.ID, status)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	from := term.FirstWeekMonday
	to := from.AddDate(0, 0, 7*max(term.Weeks, 1)-1)
	if fromStr != "" {
		if d, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = d
		}
	}
	if toStr != "" {
		if d, err := time.Parse("2006-01-02", toStr); err == nil {
			to = d
		}
	}
	days := calendar.Materialise(*term, rules, from, to)
	return term, days, nil
}

// ---- admin content management ----

type createAnnouncementInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Body             struct {
		Level       string `json:"level" minLength:"1" required:"false"`
		BodyMD      string `json:"body_md" minLength:"1" required:"false"`
		Audience    string `json:"audience" minLength:"1" required:"false"`
		Pinned      bool   `json:"pinned" required:"false"`
		RequiresAck bool   `json:"requires_ack" required:"false"`
		DurationH   int    `json:"duration_hours" required:"false"`
	} `body:""`
}

// registerAdminContent wires announcement + calendar authoring (§4 管理).
func (s *Server) registerAdminContent(humaAPI huma.API) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-create-announcement",
		Summary:     "Create an announcement",
		Method:      http.MethodPost,
		Path:        "/v1/admin/announcements",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *createAnnouncementInput) (*Output[announcementView], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		b := in.Body
		level := b.Level
		if !contains(map[string]bool{"info": true, "warning": true, "critical": true}, level) {
			level = "info"
		}
		audience := b.Audience
		if !contains(map[string]bool{"pre_login": true, "post_login": true, "both": true}, audience) {
			return nil, newProblem(http.StatusUnprocessableEntity, "invalid_argument",
				`audience 必须是 pre_login | post_login | both`)
		}
		if strings.TrimSpace(b.BodyMD) == "" {
			return nil, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "正文不能为空")
		}
		now := s.now()
		a := store.Announcement{
			ID: store.NewID(), Level: level, BodyMD: b.BodyMD, Audience: audience,
			Pinned: b.Pinned, RequiresAck: b.RequiresAck, Active: true, StartsAt: now,
		}
		if b.DurationH > 0 {
			end := now.Add(time.Duration(b.DurationH) * time.Hour)
			a.EndsAt = &end
		}
		if err := s.deps.Store.CreateAnnouncement(ctx, a); err != nil {
			return nil, mapErr(err)
		}
		return &Output[announcementView]{Status: http.StatusCreated, Body: announcementView{
			ID: a.ID, Level: a.Level, BodyMD: a.BodyMD, Pinned: a.Pinned,
			RequiresAck: a.RequiresAck, StartsAt: a.StartsAt, EndsAt: a.EndsAt,
		}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-deactivate-announcement",
		Summary:     "Deactivate an announcement",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/announcements/{id}",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		ID               string `path:"id" minLength:"1" required:"false"`
	}) (*Output[struct{}], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		if err := s.deps.Store.DeactivateAnnouncement(ctx, in.ID); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-upsert-calendar-term",
		Summary:     "Create or update a calendar term",
		Method:      http.MethodPut,
		Path:        "/v1/admin/calendar/terms",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		Body             struct {
			TermID          string `json:"term_id" minLength:"1" required:"false"`
			FirstWeekMonday string `json:"first_week_monday" minLength:"1" required:"false"`
			Weeks           int    `json:"weeks" required:"false"`
		} `body:""`
	}) (*Output[struct{}], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		b := in.Body
		monday, err := time.Parse("2006-01-02", b.FirstWeekMonday)
		if err != nil || b.TermID == "" {
			return nil, newProblem(http.StatusUnprocessableEntity,
				"invalid_argument", "需要 term_id 与 first_week_monday(YYYY-MM-DD)")
		}
		weeks := b.Weeks
		if weeks <= 0 {
			weeks = 20
		}
		if err := s.deps.Store.UpsertCalendarTerm(ctx, store.CalendarTerm{
			ID: b.TermID, FirstWeekMonday: monday, Weeks: weeks,
		}); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-add-calendar-rule",
		Summary:     "Add a calendar day rule (holiday/makeup/exam/vacation)",
		Method:      http.MethodPost,
		Path:        "/v1/admin/calendar/rules",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		Body             struct {
			TermID        string `json:"term_id" minLength:"1" required:"false"`
			Day           string `json:"day" minLength:"1" required:"false"`
			Kind          string `json:"kind" minLength:"1" required:"false"`
			FollowWeek    int    `json:"follow_week" required:"false"`
			FollowWeekday int    `json:"follow_weekday" required:"false"`
		} `body:""`
	}) (*Output[struct{}], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		b := in.Body
		day, err := time.Parse("2006-01-02", b.Day)
		if err != nil || b.TermID == "" {
			return nil, newProblem(http.StatusUnprocessableEntity,
				"invalid_argument", "需要 term_id 与 day(YYYY-MM-DD)")
		}
		switch b.Kind {
		case calendar.KindHoliday, calendar.KindMakeup, calendar.KindExam, calendar.KindVacation:
		default:
			return nil, newProblem(http.StatusUnprocessableEntity,
				"invalid_argument", "kind 必须是 holiday | makeup | exam | vacation")
		}
		if b.Kind == calendar.KindMakeup && (b.FollowWeek <= 0 || b.FollowWeekday <= 0 || b.FollowWeekday > 7) {
			return nil, newProblem(http.StatusUnprocessableEntity,
				"invalid_argument", "makeup 需要 follow_week(>=1) 与 follow_weekday(1..7)")
		}
		if err := s.deps.Store.AddCalendarRule(ctx, store.CalendarRule{
			ID: store.NewID(), TermID: b.TermID, Day: day, Kind: b.Kind,
			FollowWeek: b.FollowWeek, FollowWeekday: b.FollowWeekday,
			Status: "draft",
		}); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusCreated}, nil
	})

	// Rules are edited as drafts: delete only drafts (published rules
	// retire through a new version at publish time).
	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-delete-calendar-rule",
		Summary:     "Delete a draft calendar rule",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/calendar/rules/{id}",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		ID               string `path:"id" minLength:"1" required:"false"`
	}) (*Output[struct{}], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		if err := s.deps.Store.DeleteCalendarRule(ctx, in.ID); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})

	// Publish: draft → published at version+1; older versions retire;
	// audited; the public calendar's ETag changes.
	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-publish-calendar",
		Summary:     "Publish the term's draft calendar rules",
		Method:      http.MethodPost,
		Path:        "/v1/admin/calendar/publish",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		Body             struct {
			TermID string `json:"term_id" minLength:"1" required:"false"`
		} `body:""`
	}) (*Output[map[string]any], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		version, err := s.deps.Store.PublishCalendar(ctx, in.Body.TermID)
		if err != nil {
			return nil, mapErr(err)
		}
		_ = s.deps.Store.AppendAudit(ctx, store.AuditEvent{
			At: s.now(), Event: "calendar.published", Detail: `{"term_id":"` + in.Body.TermID + `","version":` + itoa(version) + `}`,
		})
		return &Output[map[string]any]{Status: http.StatusOK,
			Body: map[string]any{"term_id": in.Body.TermID, "version": version}}, nil
	})

	// Preview materialises the DRAFT rules — what publish would ship.
	huma.Register(humaAPI, huma.Operation{
		OperationID: "admin-preview-calendar",
		Summary:     "Preview the term's draft calendar rules",
		Method:      http.MethodGet,
		Path:        "/v1/admin/calendar/preview",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		RawAuthorization string `header:"Authorization" required:"false"`
		TermID           string `query:"term_id" minLength:"1" required:"false"`
		From             string `query:"from" minLength:"1" required:"false"`
		To               string `query:"to" minLength:"1" required:"false"`
	}) (*Output[[]calendar.DayInfo], error) {
		if err := s.adminAuthz(in.RawAuthorization); err != nil {
			return nil, err
		}
		_, days, err := s.calendarDays(ctx, in.TermID, in.From, in.To, "draft")
		if err != nil {
			return nil, err
		}
		return &Output[[]calendar.DayInfo]{Status: http.StatusOK, Body: days}, nil
	})
}

func itoa(v int) string { return strconv.Itoa(v) }

func contains(set map[string]bool, v string) bool { return set[v] }
