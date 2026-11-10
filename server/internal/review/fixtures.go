// Package review holds the fixed data behind the store-review account. Every
// value is synthetic: no real student, grade, balance or transaction. Requests
// from the review account never reach the school, so nothing here is fetched.
package review

import (
	"context"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/aac"
	"github.com/Sibuxiangx/loveace-go/core/services/competition"
	"github.com/Sibuxiangx/loveace-go/core/services/jwc"
	"github.com/Sibuxiangx/loveace-go/core/services/labor"
	"github.com/Sibuxiangx/loveace-go/core/services/ykt"
)

// Refused answers every write the review account may not run.
const Refused = "审核账号不执行真实写操作"

// applyReply is the canned answer to labor.apply_activity. Nobody is enrolled
// and the activity list does not change.
const applyReply = "报名成功"

// beijing is the school's clock. Sign-up windows are written in it so the
// mini program's date parsing sees the same wall-clock times the school sends.
var beijing = time.FixedZone("CST", 8*3600)

// ReadOp answers a read operation. ok is false when the review account has no
// fixture for the operation.
func ReadOp(name string) (any, bool) {
	now := time.Now().In(beijing)
	switch name {
	case "ykt.balance":
		return ykt.CardBalance{Balance: 88.6, BalanceText: "88.60"}, true
	case "ykt.recent_transactions", "ykt.monthly_transactions":
		return transactions(now), true
	case "aac.ticket":
		return map[string]any{"ready": true, "cached": true, "ticket_length": 32}, true
	case "aac.credit_info":
		return aac.AacCreditInfo{TotalScore: 6.5, IsTypeAdopt: true, TypeAdoptResult: "示例"}, true
	case "aac.credit_list":
		return creditList(), true
	case "labor.progress":
		return labor.ProgressInfo{SumScore: 12, Progress: 40}, true
	case "labor.joined_clubs":
		return []labor.ClubInfo{club()}, true
	case "labor.joined_activities":
		return []labor.Activity{joined(now)}, true
	case "labor.club_activities":
		return []labor.Activity{joined(now), signable(now), full(now), notStarted(now)}, true
	case "labor.sign_list":
		return signList(now), true
	case "labor.activity_detail":
		return activityDetail(now), true
	case "jwc.plan_completion":
		return planCompletion(), true
	case "competition.info":
		return competitionInfo(), true
	}
	return nil, false
}

// WriteOp answers a mutating operation the review account may run. ok is false
// for everything else: the review account never changes school state.
func WriteOp(name string) (any, bool) {
	switch name {
	case "labor.apply_activity":
		return applyReply, true
	case "aac.reset_ticket", "labor.reset_ticket":
		return map[string]any{"reset": true}, true
	}
	return nil, false
}

// Reads serves the academic read surface. Arguments are ignored: the same
// fixture answers every term.
type Reads struct{}

// AcademicSummary is the academic overview.
func (Reads) AcademicSummary(context.Context, string, string) (*jwc.AcademicInfo, error) {
	return &jwc.AcademicInfo{CompletedCourses: 32, FailedCourses: 0, GPA: 3.61, PendingCourses: 6, CurrentTerm: "2026-2027-1-1"}, nil
}

// Terms lists two selectable terms.
func (Reads) Terms(context.Context, string, string) ([]jwc.TermItem, error) {
	return []jwc.TermItem{
		{TermCode: "2026-2027-1-1", TermName: "2026-2027学年第一学期", IsCurrent: true},
		{TermCode: "2025-2026-2-1", TermName: "2025-2026学年第二学期", IsCurrent: false},
	}, nil
}

// TermScores answers one term's score page.
func (Reads) TermScores(_ context.Context, _, _, term string) (*jwc.TermScoreResponse, error) {
	records := []jwc.ScoreRecord{
		{Sequence: 1, TermID: term, CourseCode: "A001", CourseClass: "01", CourseNameCN: "示例高等数学", CourseNameEN: "Sample Calculus", Credits: "4", Hours: 64, Score: "88"},
		{Sequence: 2, TermID: term, CourseCode: "A002", CourseClass: "01", CourseNameCN: "示例大学英语", CourseNameEN: "Sample English", Credits: "3", Hours: 48, Score: "良"},
	}
	return &jwc.TermScoreResponse{TotalCount: int64(len(records)), Records: records}, nil
}

// PlanCompletion answers the plan-completion tree with consistent totals.
func (Reads) PlanCompletion(context.Context, string, string) (*jwc.PlanCompletionOutcome, error) {
	return planCompletion(), nil
}

// TrainingPlan is the training-plan summary.
func (Reads) TrainingPlan(context.Context, string, string) (*jwc.TrainingPlanInfo, error) {
	return &jwc.TrainingPlanInfo{PlanName: "示例培养方案", MajorName: "示例专业", Grade: "2026"}, nil
}

// Exams is one final exam.
func (Reads) Exams(context.Context, string, string) (*jwc.ExamInfoResponse, error) {
	return &jwc.ExamInfoResponse{
		Exams: []jwc.UnifiedExamInfo{{
			CourseName: "示例大学英语", ExamDate: "2027-01-10", ExamTime: "09:00-11:00",
			ExamLocation: "示例教学楼 101", ExamType: "期末考试",
		}},
		TotalCount: 1,
	}, nil
}

// Timetable is a three-course week, so the merged view shows classes.
func (Reads) Timetable(context.Context, string, string, string) (*jwc.StudentScheduleResponse, error) {
	return &jwc.StudentScheduleResponse{
		AllUnits: 8,
		DateList: []jwc.ScheduleDateInfo{{
			ProgramPlanCode: "review", ProgramPlanName: "示例专业", TotalUnits: 8,
			SelectCourseList: []jwc.ScheduleCourse{
				course("A001", "示例高等数学", 4, 1, 1, 2, "1-16"),
				course("A002", "示例大学英语", 3, 3, 3, 2, "1-16"),
				course("A003", "示例体育", 1, 5, 5, 2, "2-12"),
			},
		}},
	}, nil
}

func course(code, name string, unit float64, day, sessions, continuing int64, week string) jwc.ScheduleCourse {
	return jwc.ScheduleCourse{
		ID:                   jwc.ScheduleCourseID{CoureNumber: code, CoureSequenceNumber: "01"},
		CourseName:           name,
		Unit:                 unit,
		AttendClassTeacher:   "示例老师",
		CoursePropertiesName: "必修",
		TimeAndPlaceList: []jwc.ScheduleTimePlace{{
			ClassWeek: week, ClassDay: day, ClassSessions: sessions, ContinuingSession: continuing,
			CampusName: "示例校区", TeachingBuildingName: "示例教学楼", ClassroomName: "101", CoureName: name,
		}},
	}
}

func planCompletion() *jwc.PlanCompletionOutcome {
	info := jwc.PlanCompletionInfo{
		PlanName: "示例培养方案", Major: "示例专业", Grade: "2026",
		Categories: []jwc.PlanCategory{
			{
				CategoryID: "1", CategoryName: "公共必修", MinCredits: 30, CompletedCredits: 2,
				Courses: []jwc.PlanCourse{
					{CourseCode: "G001", CourseName: "示例思想课", Credits: new(2.0), Score: new("88"), IsPassed: true, StatusDescription: "已通过"},
					{CourseCode: "G002", CourseName: "示例英语课", Credits: new(4.0), StatusDescription: "未修读"},
				},
			},
			{
				CategoryID: "2", CategoryName: "专业必修", MinCredits: 60, CompletedCredits: 3,
				Courses: []jwc.PlanCourse{
					{CourseCode: "Z001", CourseName: "示例专业课", Credits: new(3.0), Score: new("76"), IsPassed: true, StatusDescription: "已通过"},
				},
			},
		},
	}
	info = info.CalculateStatistics()
	return &jwc.PlanCompletionOutcome{Kind: jwc.OutcomeInfo, Info: &info}
}

func transactions(now time.Time) ykt.TransactionQueryResult {
	day := now.Format("2006-01-02")
	return ykt.TransactionQueryResult{
		StartDate: now.AddDate(0, 0, -7).Format("2006-01-02"),
		EndDate:   day,
		Records: []ykt.TransactionRecord{
			{AccountingTime: day + " 12:10:00", TransactionTime: day + " 12:09:41", Expense: new(12.5), OperationType: "食堂消费", Balance: 88.6, Area: "示例食堂", TerminalID: "T01"},
			{AccountingTime: day + " 08:00:00", TransactionTime: day + " 07:59:12", Income: new(100.0), OperationType: "充值", Balance: 101.1, Area: "示例窗口", TerminalID: "T02"},
		},
	}
}

func creditList() []aac.AacCreditCategory {
	return []aac.AacCreditCategory{
		{ID: "c1", ShowNum: 1, TypeName: "示例素质拓展", TotalScore: 4, Children: []aac.AacCreditItem{
			{ID: "i1", Title: "示例讲座", TypeName: "示例素质拓展", Score: 2, AddTime: "2026-09-18"},
			{ID: "i2", Title: "示例志愿服务", TypeName: "示例素质拓展", Score: 2, AddTime: "2026-10-09"},
		}},
		{ID: "c2", ShowNum: 2, TypeName: "示例创新实践", TotalScore: 2.5, Children: []aac.AacCreditItem{
			{ID: "i3", Title: "示例科创项目", TypeName: "示例创新实践", Score: 2.5, AddTime: "2026-06-30"},
		}},
	}
}

const clubID = "review-club"

func club() labor.ClubInfo {
	return labor.ClubInfo{ID: clubID, Name: "示例劳动俱乐部", TypeName: new("示例校区"), ChairmanName: new("示例负责人"), MemberNum: 12}
}

const stampLayout = "2006-01-02 15:04:05"

func stamp(t time.Time) string { return t.Format(stampLayout) }

// activity builds one club activity. The sign-up window is relative to now, so
// 可报名 / 人数已满 / 未开始 hold whenever the review happens.
func activity(id, title string, now time.Time) labor.Activity {
	start := now.Add(14 * 24 * time.Hour)
	return labor.Activity{
		ID: id, State: 1, StateName: "进行中", TypeID: "review-type", TypeName: "示例类型", Title: title,
		StartTime: stamp(start), EndTime: stamp(start.Add(2 * time.Hour)),
		ChargeUserName: "示例负责人", ClubID: clubID, ClubName: "示例劳动俱乐部",
		MemberNum: 3, PeopleNum: 30, IsJoined: new(false), IsClosed: new(false),
		SignUpStartTime: stamp(now.Add(-24 * time.Hour)), SignUpEndTime: stamp(now.Add(7 * 24 * time.Hour)),
	}
}

func signable(now time.Time) labor.Activity {
	return activity("review-open", "示例志愿服务", now)
}

func full(now time.Time) labor.Activity {
	a := activity("review-full", "示例校园清洁", now)
	a.MemberNum = a.PeopleNum
	return a
}

func notStarted(now time.Time) labor.Activity {
	a := activity("review-later", "示例讲座听课", now)
	a.SignUpStartTime = stamp(now.Add(3 * 24 * time.Hour))
	a.SignUpEndTime = stamp(now.Add(10 * 24 * time.Hour))
	return a
}

const joinedID = "review-joined"

// joined is an activity the account already attends: past, with sign records.
func joined(now time.Time) labor.Activity {
	start := now.Add(-2 * 24 * time.Hour)
	return labor.Activity{
		ID: joinedID, State: 2, StateName: "已结束", TypeID: "review-type", TypeName: "示例类型",
		Title: "示例志愿活动", StartTime: stamp(start), EndTime: stamp(start.Add(2 * time.Hour)),
		ChargeUserName: "示例负责人", ClubID: clubID, ClubName: "示例劳动俱乐部",
		MemberNum: 20, PeopleNum: 30, IsJoined: new(true), IsClosed: new(false),
		SignUpStartTime: stamp(now.Add(-10 * 24 * time.Hour)), SignUpEndTime: stamp(now.Add(-5 * 24 * time.Hour)),
	}
}

func signList(now time.Time) []labor.SignItem {
	start := now.Add(-2 * 24 * time.Hour)
	return []labor.SignItem{{
		ID: "review-sign-1", ItemType: 1, TypeName: "签到",
		StartTime: stamp(start), EndTime: stamp(start.Add(time.Hour)),
		IsSign: true, SignTime: new(stamp(start.Add(5 * time.Minute))),
	}}
}

func activityDetail(now time.Time) labor.ActivityDetail {
	a := joined(now)
	return labor.ActivityDetail{
		ID: a.ID, Title: a.Title, StartTime: a.StartTime, EndTime: a.EndTime,
		ChargeUserName: a.ChargeUserName, ClubName: a.ClubName,
		MemberNum: a.MemberNum, PeopleNum: a.PeopleNum,
		FormData:        []labor.FormField{{ID: "f1", Name: "活动地点", Value: "示例活动室 101"}},
		FlowData:        []labor.FlowData{{NodeName: "发起", UserName: "示例负责人", IsAdopt: new(true), Time: a.SignUpStartTime}},
		TeacherList:     []labor.Teacher{{Name: "示例老师", UserNo: "T0001"}},
		SignList:        signList(now),
		SignUpStartTime: new(a.SignUpStartTime),
		SignUpEndTime:   new(a.SignUpEndTime),
	}
}

func competitionInfo() competition.FullResponse {
	return competition.FullResponse{
		StudentID:        "review",
		TotalAwardsCount: 1,
		Awards: []competition.AwardProject{{
			ProjectID: "p1", ProjectName: "示例竞赛", Level: "校级", Grade: "三等奖",
			AwardDate: "2026-05-20", ApplicantID: "review", ApplicantName: "示例学生",
			Order: 1, Credits: 0.5, Status: "已认定", VerificationStatus: "通过",
		}},
		CreditsSummary: &competition.CreditsSummary{DisciplineCompetitionCredits: new(0.5), OtherProjectCredits: new(0.0)},
	}
}
