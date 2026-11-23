package jwc

import (
	"context"
	"regexp"
	"strconv"

	"github.com/Sibuxiangx/loveace-go/core/errs"
)

// freeCurrentSectionPath is the 今天 page of the free-classroom query. It carries the school's current
// teaching section as a script variable.
const freeCurrentSectionPath = "/student/teachingResources/freeClassroom/today"

var currentSectionRe = regexp.MustCompile(`var sec = parseInt\('(\d+)'\)`)

// FreeClassroomCurrentSection is the teaching section the school says it is now, 1 to 10. It is a
// parse error when the page gives no section in that range.
func (sv *Service) FreeClassroomCurrentSection(caller context.Context) (int64, error) {
	response, err := sv.get(caller, freeCurrentSectionPath, nil)
	if err != nil {
		return 0, err
	}
	match := currentSectionRe.FindStringSubmatch(response.BodyText())
	if match == nil {
		return 0, errs.NewParse("free classroom: current section not found")
	}
	section, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || section < 1 || section > freeMaxSection {
		return 0, errs.NewParse("free classroom: current section out of range")
	}
	return section, nil
}
