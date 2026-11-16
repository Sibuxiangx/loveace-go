package review

import (
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/portal"
)

// The portal fixtures ignore kind, range and page: one synthetic answer serves
// every request, so the review account never shows a real student's records.

func portalAccess(now time.Time) portal.AccessPage {
	records := []portal.AccessRecord{
		{ID: "review-access-1", Time: stamp(now.Add(-3 * time.Hour)), Direction: "entry", Detail: "示例宿舍门禁"},
		{ID: "review-access-2", Time: stamp(now.Add(-2 * time.Hour)), Direction: "exit", Detail: "示例宿舍门禁"},
	}
	return portal.AccessPage{Records: records, Total: len(records)}
}

func portalBorrowing(now time.Time) portal.BorrowingResult {
	return portal.BorrowingResult{
		Current: []portal.BorrowRecord{
			{ID: "review-loan-1", Title: "示例图书一", BorrowedAt: stamp(now.Add(-5 * 24 * time.Hour))},
		},
		History: []portal.BorrowRecord{
			{
				ID: "review-loan-2", Title: "示例图书二",
				BorrowedAt: stamp(now.Add(-30 * 24 * time.Hour)),
				ReturnedAt: new(stamp(now.Add(-24 * time.Hour))),
			},
		},
	}
}

func portalConsumption(now time.Time) portal.ConsumptionPage {
	day := now.Format("2006-01-02")
	records := []portal.ConsumptionRecord{
		{ID: "review-cost-1", Date: day, Time: "12:10:00", Area: "示例食堂", Amount: "12.50"},
		{ID: "review-cost-2", Date: day, Time: "07:59:12", Area: "示例超市", Amount: "8.00"},
	}
	return portal.ConsumptionPage{Records: records, Total: len(records)}
}

func portalMail() portal.MailInitialPassword {
	return portal.MailInitialPassword{LoginID: "review.student", Password: "review-mail-0001"}
}
