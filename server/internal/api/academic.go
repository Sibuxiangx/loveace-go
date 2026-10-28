package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/cache"
	"github.com/Sibuxiangx/loveace-go/server/internal/calendar"
	"github.com/Sibuxiangx/loveace-go/server/internal/dto"
)

// Cache TTLs per resource (DESIGN.md §11.3).
const (
	ttlSummary    = 60 * time.Second
	ttlTerms      = time.Hour
	ttlTermScores = 5 * time.Minute
	ttlPlan       = time.Hour
)

// registerAcademic wires the /v1/me/academic/* read endpoints. All are
// read-through cached per user with stale-if-error fallback.
func (s *Server) registerAcademic(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "academic-summary",
		Summary:     "Academic overview",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/summary",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:summary:"+claims.UserID, ttlSummary, func(ctx context.Context) (any, error) {
			info, err := s.deps.School.AcademicSummary(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			return dto.AcademicSummaryFrom(info), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-terms",
		Summary:     "Selectable terms",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/terms",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:terms:"+claims.UserID, ttlTerms, func(ctx context.Context) (any, error) {
			terms, err := s.deps.School.Terms(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			return dto.TermsFrom(terms), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-term-scores",
		Summary:     "One term's scores",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/terms/{term}/scores",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *TermScoresInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:scores:"+claims.UserID+":"+in.Term, ttlTermScores, func(ctx context.Context) (any, error) {
			page, err := s.deps.School.TermScores(ctx, claims.UserID, claims.DeviceID, in.Term)
			if err != nil {
				return nil, err
			}
			return dto.ScorePageFrom(page), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-plan-completion",
		Summary:     "Plan completion tree",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/plan/completion",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:plan:"+claims.UserID, ttlPlan, func(ctx context.Context) (any, error) {
			outcome, err := s.deps.School.PlanCompletion(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			return dto.PlanCompletionFrom(outcome), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-plan",
		Summary:     "Training-plan summary",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/plan",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:planinfo:"+claims.UserID, ttlPlan, func(ctx context.Context) (any, error) {
			info, err := s.deps.School.TrainingPlan(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			return dto.TrainingPlanFrom(info), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-exams",
		Summary:     "Aggregated exam view",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/exams",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:exams:"+claims.UserID, ttlTermScores, func(ctx context.Context) (any, error) {
			exams, err := s.deps.School.Exams(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			return dto.ExamListFrom(exams), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "academic-timetable",
		Summary:     "Raw student timetable for one term (calendar merge arrives with the calendar module)",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/timetable",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *TimetableInput) (*Output[dto.Envelope], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		if in.Term == "" {
			return nil, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "缺少学期代码 term")
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:timetable:"+claims.UserID+":"+in.Term, ttlPlan, func(ctx context.Context) (any, error) {
			schedule, err := s.deps.School.Timetable(ctx, claims.UserID, claims.DeviceID, in.Term)
			if err != nil {
				return nil, err
			}
			return dto.TimetableFrom(schedule), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return &Output[dto.Envelope]{Status: http.StatusOK, Body: *envelope(e)}, nil
	})
}

type TimetableInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Term             string `query:"term" required:"false"`
}

// MergedTimetableInput drives the calendar-merged view (§11.1): the raw
// schedule projected onto the published calendar over [from, to].
type MergedTimetableInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Term             string `query:"term" minLength:"1" required:"false"`
	From             string `query:"from" minLength:"1" required:"false"`
	To               string `query:"to" minLength:"1" required:"false"`
}

// registerMergedTimetable serves the merged view: holiday days carry no
// classes; makeup days show the followed week/weekday's timetable.
func (s *Server) registerMergedTimetable(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "academic-timetable-merged",
		Summary:     "Student timetable merged with the academic calendar",
		Method:      http.MethodGet,
		Path:        "/v1/me/academic/timetable/merged",
		Tags:        []string{"academic"},
	}, func(ctx context.Context, in *MergedTimetableInput) (*Output[[]calendar.DayView], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if in.Term == "" {
			return nil, newProblem(http.StatusUnprocessableEntity, "invalid_argument", "缺少学期代码 term")
		}
		if err := s.ensureSession(ctx, claims.UserID, claims.DeviceID); err != nil {
			return nil, err
		}
		term, _, err := s.calendarDays(ctx, in.Term, in.From, in.To, "published")
		if err != nil {
			return nil, err
		}
		e, err := s.cache.GetOrLoadSWR(ctx, "academic:timetable:merged:"+claims.UserID+":"+in.Term, ttlPlan, func(ctx context.Context) (any, error) {
			schedule, err := s.deps.School.Timetable(ctx, claims.UserID, claims.DeviceID, in.Term)
			if err != nil {
				return nil, err
			}
			var courses []jwc.ScheduleCourse
			for _, info := range schedule.DateList {
				courses = append(courses, info.SelectCourseList...)
			}
			// Exams are load-bearing here — the merged view is the only
			// place the post-teaching exam season appears (teaching ends
			// ~week 16-18; exams follow). A failing exam fetch fails the
			// request: silently omitting exams would hide them.
			exams, err := s.deps.School.Exams(ctx, claims.UserID, claims.DeviceID)
			if err != nil {
				return nil, err
			}
			rules, err := s.deps.Store.ListCalendarRules(ctx, in.Term, "published")
			if err != nil {
				return nil, err
			}
			return calendar.MergeDays(*term, rules, courses, exams.Exams, term.FirstWeekMonday, term.FirstWeekMonday.AddDate(0, 0, 7*max(term.Weeks, 1)-1)), nil
		})
		if err != nil {
			return nil, mapErr(err)
		}
		days, ok := e.Value.([]calendar.DayView)
		if !ok {
			return nil, newProblem(http.StatusInternalServerError, "error", "合并课表缓存类型异常")
		}
		// The cache holds the whole term; serve [from, to] only.
		from := term.FirstWeekMonday
		to := term.FirstWeekMonday.AddDate(0, 0, 7*max(term.Weeks, 1)-1)
		if in.From != "" {
			if d, err := time.Parse("2006-01-02", in.From); err == nil {
				from = d
			}
		}
		if in.To != "" {
			if d, err := time.Parse("2006-01-02", in.To); err == nil {
				to = d
			}
		}
		sliced := make([]calendar.DayView, 0, len(days))
		for _, d := range days {
			day, err := time.Parse("2006-01-02", d.Date)
			if err != nil {
				continue
			}
			if !day.Before(from) && !day.After(to) {
				sliced = append(sliced, d)
			}
		}
		return &Output[[]calendar.DayView]{Status: http.StatusOK, Body: sliced}, nil
	})
}

type TermScoresInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	Term             string `path:"term" required:"false"`
}

func envelope(e cache.Entry) *dto.Envelope {
	return &dto.Envelope{
		Data: e.Value,
		Meta: dto.Meta{
			FetchedAt: dto.FetchedAt(e.FetchedAt),
			Stale:     e.Stale,
			Source:    e.Source,
		},
	}
}
