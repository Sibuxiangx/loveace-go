// The merged view (DESIGN.md §11.1): the student's raw schedule
// (jwc.ScheduleCourse rows) projected onto the materialised calendar, so
// holidays show no classes and makeup days show the followed week's
// timetable. Exams (期末/统考/其他) are overlaid by their own dates —
// the raw timetable never showed them (teaching ends ~week 16-18, exams
// come after), so the post-teaching exam season used to be invisible.
package calendar

import (
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// ClassView is one class occurrence on a day.
type ClassView struct {
	CourseName   string `json:"course_name"`
	Teacher      string `json:"teacher,omitempty"`
	Location     string `json:"location,omitempty"`
	StartSession int64  `json:"start_session"`
	EndSession   int64  `json:"end_session"`
}

// ExamView is one exam seated on a day.
type ExamView struct {
	CourseName string `json:"course_name"`
	ExamType   string `json:"exam_type,omitempty"`
	Time       string `json:"time,omitempty"`
	Location   string `json:"location,omitempty"`
	Note       string `json:"note,omitempty"`
}

// DayView is one calendar day with the classes that actually happen on
// it (holiday/vacation days have no classes; makeup days show the
// followed week/weekday's classes) plus any exams seated that date.
type DayView struct {
	DayInfo
	Classes []ClassView `json:"classes"`
	Exams   []ExamView  `json:"exams,omitempty"`
}

// MergeDays renders [from, to] with classes and exams. Rules drive the
// calendar layer exactly like the public day view; exams are placed by
// their own dates, independent of the week grid.
func MergeDays(term store.CalendarTerm, rules []store.CalendarRule, courses []jwc.ScheduleCourse, exams []jwc.UnifiedExamInfo, from, to time.Time) []DayView {
	days := Materialise(term, rules, from, to)
	examsByDate := map[string][]ExamView{}
	for _, exam := range exams {
		date, ok := examDate(exam.ExamDate)
		if !ok {
			continue // an unparseable date cannot be placed on the grid
		}
		examsByDate[date] = append(examsByDate[date], ExamView{
			CourseName: exam.CourseName,
			ExamType:   exam.ExamType,
			Time:       exam.ExamTime,
			Location:   exam.ExamLocation,
			Note:       exam.Note,
		})
	}
	out := make([]DayView, 0, len(days))
	for _, info := range days {
		week, weekday := info.WeekNo, info.Weekday
		switch info.Kind {
		case KindMakeup:
			if info.Follows != nil {
				week, weekday = info.Follows.WeekNo, info.Follows.Weekday
			}
		case KindHoliday, KindVacation:
			out = append(out, DayView{DayInfo: info, Classes: []ClassView{}})
			continue
		}
		out = append(out, DayView{DayInfo: info, Classes: classesOn(courses, week, weekday)})
	}
	// Exams overlay after the week-grid pass: they render on their date
	// no matter the kind (teaching day, holiday, or the post-teaching
	// exam season where the week grid is empty).
	for i := range out {
		if exams := examsByDate[out[i].Date]; len(exams) > 0 {
			out[i].Exams = exams
		}
	}
	return out
}

// examDate normalises an exam date to YYYY-MM-DD. Accepts the standard
// form and the compact "YYYYMMDD" the plan pages emit.
func examDate(raw string) (string, bool) {
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

func classesOn(courses []jwc.ScheduleCourse, week, weekday int) []ClassView {
	classes := []ClassView{}
	for _, course := range courses {
		for _, slot := range course.TimeAndPlaceList {
			if int(slot.ClassDay) != weekday || !jwc.WeekMatches(slot.ClassWeek, week) {
				continue
			}
			classes = append(classes, ClassView{
				CourseName:   course.CourseName,
				Teacher:      course.AttendClassTeacher,
				Location:     slot.LocationDescription(),
				StartSession: slot.ClassSessions,
				EndSession:   slot.EndSession(),
			})
		}
	}
	return classes
}
