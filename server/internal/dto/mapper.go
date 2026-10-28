package dto

import "github.com/Sibuxiangx/loveace-go/core/services/jwc"

// The From* functions are the ONLY place where school models become API
// shapes (DESIGN.md §2: dto/mapper is the single conversion point).

// AcademicSummaryFrom converts the academic overview.
func AcademicSummaryFrom(in *jwc.AcademicInfo) *AcademicSummary {
	if in == nil {
		return nil
	}
	return &AcademicSummary{
		CompletedCourses: in.CompletedCourses,
		FailedCourses:    in.FailedCourses,
		PendingCourses:   in.PendingCourses,
		GPA:              in.GPA,
		CurrentTerm:      in.CurrentTerm,
	}
}

// TermsFrom converts the term list.
func TermsFrom(in []jwc.TermItem) []Term {
	out := make([]Term, 0, len(in))
	for _, t := range in {
		out = append(out, Term{Code: t.TermCode, Name: t.TermName, IsCurrent: t.IsCurrent})
	}
	return out
}

// ScorePageFrom converts one term's score page.
func ScorePageFrom(in *jwc.TermScoreResponse) *ScorePage {
	if in == nil {
		return nil
	}
	return &ScorePage{TotalCount: in.TotalCount, Records: ScoreRecordsFrom(in.Records)}
}

// ScoreRecordsFrom converts score records.
func ScoreRecordsFrom(in []jwc.ScoreRecord) []ScoreRecord {
	out := make([]ScoreRecord, 0, len(in))
	for _, r := range in {
		out = append(out, ScoreRecord{
			Sequence:     r.Sequence,
			TermID:       r.TermID,
			CourseCode:   r.CourseCode,
			CourseClass:  r.CourseClass,
			CourseNameCN: r.CourseNameCN,
			CourseNameEN: r.CourseNameEN,
			Credits:      r.Credits,
			Hours:        r.Hours,
			CourseType:   r.CourseType,
			ExamType:     r.ExamType,
			Score:        r.Score,
			RetakeScore:  r.RetakeScore,
			MakeupScore:  r.MakeupScore,
			ExamTime:     r.ExamTime,
		})
	}
	return out
}

// ExamListFrom converts the aggregated exam response.
func ExamListFrom(in *jwc.ExamInfoResponse) *ExamList {
	if in == nil {
		return nil
	}
	out := &ExamList{TotalCount: in.TotalCount, Exams: make([]ExamItem, 0, len(in.Exams))}
	for _, e := range in.Exams {
		out.Exams = append(out.Exams, ExamItem{
			CourseName:   e.CourseName,
			ExamDate:     e.ExamDate,
			ExamTime:     e.ExamTime,
			ExamLocation: e.ExamLocation,
			ExamType:     e.ExamType,
			Note:         e.Note,
		})
	}
	return out
}

// TrainingPlanFrom converts the training-plan summary.
func TrainingPlanFrom(in *jwc.TrainingPlanInfo) *TrainingPlan {
	if in == nil {
		return nil
	}
	return &TrainingPlan{PlanName: in.PlanName, MajorName: in.MajorName, Grade: in.Grade}
}

// TimetableFrom converts the student schedule document.
func TimetableFrom(in *jwc.StudentScheduleResponse) *Timetable {
	if in == nil {
		return nil
	}
	out := &Timetable{Units: in.AllUnits, Courses: []TimetableCourse{}}
	for _, c := range in.Courses() {
		course := TimetableCourse{
			CourseCode: c.CourseCode(),
			CourseSeq:  c.CourseSequence(),
			CourseName: c.CourseName,
			Teacher:    c.AttendClassTeacher,
			Units:      c.Unit,
			Status:     c.SelectCourseStatusName,
			Slots:      []TimetableSlot{},
		}
		for _, tp := range c.TimeAndPlaceList {
			course.Slots = append(course.Slots, TimetableSlot{
				Day:             tp.ClassDay,
				StartSession:    tp.ClassSessions,
				EndSession:      tp.EndSession(),
				Location:        tp.LocationDescription(),
				WeekDescription: tp.WeekDescription,
			})
		}
		out.Courses = append(out.Courses, course)
	}
	return out
}

// PlanCompletionFrom converts the plan-completion outcome.
func PlanCompletionFrom(in *jwc.PlanCompletionOutcome) *PlanCompletion {
	if in == nil {
		return nil
	}
	out := &PlanCompletion{Kind: string(in.Kind)}
	if in.Info != nil {
		info := &PlanCompletionInfo{
			PlanName:      in.Info.PlanName,
			Major:         in.Info.Major,
			Grade:         in.Info.Grade,
			Categories:    planCategoriesFrom(in.Info.Categories),
			TotalCourses:  in.Info.TotalCourses,
			PassedCourses: in.Info.PassedCourses,
			FailedCourses: in.Info.FailedCourses,
			UnreadCourses: in.Info.UnreadCourses,
		}
		out.Info = info
	}
	if in.Selection != nil {
		sel := &PlanSelection{Hint: in.Selection.Hint}
		for _, o := range in.Selection.Options {
			sel.Options = append(sel.Options, PlanOption{
				PlanID:    o.PlanID,
				PlanName:  o.PlanName,
				PlanType:  o.PlanType,
				IsCurrent: o.IsCurrent,
			})
		}
		out.Selection = sel
	}
	return out
}

func planCategoriesFrom(in []jwc.PlanCategory) []PlanCategory {
	out := make([]PlanCategory, 0, len(in))
	for _, c := range in {
		out = append(out, PlanCategory{
			CategoryID:             c.CategoryID,
			CategoryName:           c.CategoryName,
			MinCredits:             c.MinCredits,
			CompletedCredits:       c.CompletedCredits,
			TotalCourses:           c.TotalCourses,
			PassedCourses:          c.PassedCourses,
			FailedCourses:          c.FailedCourses,
			MissingRequiredCourses: c.MissingRequiredCourses,
			Subcategories:          planCategoriesFrom(c.Subcategories),
			Courses:                planCoursesFrom(c.Courses),
		})
	}
	return out
}

func planCoursesFrom(in []jwc.PlanCourse) []PlanCourse {
	out := make([]PlanCourse, 0, len(in))
	for _, c := range in {
		out = append(out, PlanCourse{
			CourseCode:        c.CourseCode,
			CourseName:        c.CourseName,
			Credits:           c.Credits,
			Score:             c.Score,
			IsPassed:          c.IsPassed,
			StatusDescription: c.StatusDescription,
		})
	}
	return out
}
