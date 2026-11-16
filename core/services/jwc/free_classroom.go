package jwc

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// Free-classroom (空闲教室) pages under the JWC site. The answers are the
// school's own rows: merging duplicate rows and deciding which rooms count as
// stopped are left to the clients.
const (
	freeClassroomPath     = "/student/teachingResources/freeClassroomQuery"
	freeClassroomRoomPath = "/student/teachingResources/classroomCurriculum"
	freeClassroomPageSize = 200
	freeClassroomMaxPages = 10
	freeMaxWeek           = 24
	freeMaxWeekday        = 7
	freeMaxSection        = 10
)

var (
	ajaxHeaders   = [][2]string{{"X-Requested-With", "XMLHttpRequest"}, {"Accept", ajaxAccept}}
	currentWeekRe = regexp.MustCompile(`第\s*(\d+)\s*周`)
)

// FreeClassroomBuilding is one teaching building. Stopped is set when the
// school marks the building 停用 in its name or remark.
type FreeClassroomBuilding struct {
	CampusNumber   string `json:"campus_number"`
	CampusName     string `json:"campus_name"`
	BuildingNumber string `json:"building_number"`
	BuildingName   string `json:"building_name"`
	Stopped        bool   `json:"stopped"`
}

// FreeClassroomOption is one classroom type from the query form.
type FreeClassroomOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// FreeClassroomMeta is the query page's teaching week (nil when the page does
// not show one) and its classroom types.
type FreeClassroomMeta struct {
	CurrentWeek *int64                `json:"current_week"`
	Types       []FreeClassroomOption `json:"types"`
}

// FreeClassroom is one classroom row as the school lists it. The same room can
// appear as more than one row.
type FreeClassroom struct {
	CampusNumber    string `json:"campus_number"`
	CampusName      string `json:"campus_name"`
	BuildingNumber  string `json:"building_number"`
	BuildingName    string `json:"building_name"`
	ClassroomNumber string `json:"classroom_number"`
	ClassroomName   string `json:"classroom_name"`
	StatusCode      string `json:"status_code"`
	TypeCode        string `json:"type_code"`
	TypeName        string `json:"type_name"`
	Seats           *int64 `json:"seats"`
}

// FreeClassroomQuery selects the classrooms free in every (week, slot) pair.
// Weeks are 1–24. Slots are "weekday/section", weekday 1–7, section 1–10.
// Campus, building and type are optional filters; "" means any.
type FreeClassroomQuery struct {
	Weeks          []string
	Slots          []string
	CampusNumber   string
	BuildingNumber string
	TypeCode       string
}

// FreeClassroomBuildings lists the teaching buildings of both campuses.
func (sv *Service) FreeClassroomBuildings(caller context.Context) ([]FreeClassroomBuilding, error) {
	out := []FreeClassroomBuilding{}
	for _, campus := range []string{"2", "1"} {
		response, err := sv.postForm(caller, freeClassroomPath+"/TeachBuildingName",
			[][2]string{{"codeCamListName", campus}}, ajaxHeaders, session.Read)
		if err != nil {
			return nil, err
		}
		if err := common.RequireSuccess(response); err != nil {
			return nil, err
		}
		value, err := common.ParseJSON(response.BodyText())
		if err != nil {
			return nil, err
		}
		for _, item := range common.ArrayItems(value) {
			no := common.JSONString(common.RawField(item, "id"), "teachingBuildingNumber")
			if no == "" {
				continue
			}
			name := common.JSONString(item, "teachingBuildingName")
			remark := common.JSONString(item, "remark")
			out = append(out, FreeClassroomBuilding{
				CampusNumber:   campus,
				CampusName:     freeCampusName(campus),
				BuildingNumber: no,
				BuildingName:   name,
				Stopped:        strings.Contains(remark, "停用") || strings.Contains(name, "停用"),
			})
		}
	}
	return out, nil
}

// FreeClassroomMeta reads the current teaching week and the classroom types
// from the query page.
func (sv *Service) FreeClassroomMeta(caller context.Context) (*FreeClassroomMeta, error) {
	response, err := sv.get(caller, freeClassroomPath+"/custom", nil)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	page := response.BodyText()
	meta := &FreeClassroomMeta{Types: []FreeClassroomOption{}}
	if m := currentWeekRe.FindStringSubmatch(page); m != nil {
		if week, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			meta.CurrentWeek = new(week)
		}
	}
	options, err := parseSelectOptions(page, "select#jslxdm")
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		meta.Types = append(meta.Types, FreeClassroomOption{Code: option.Code, Name: option.Name})
	}
	return meta, nil
}

// FreeClassroomSearch returns the classrooms free in every selected week and
// slot. It reads pages until the school's total is reached, at most ten pages.
func (sv *Service) FreeClassroomSearch(caller context.Context, query FreeClassroomQuery) ([]FreeClassroom, error) {
	weeks, err := normalizeFreeWeeks(query.Weeks)
	if err != nil {
		return nil, err
	}
	slots, err := normalizeFreeSlots(query.Slots)
	if err != nil {
		return nil, err
	}
	out := []FreeClassroom{}
	for page := 1; page <= freeClassroomMaxPages; page++ {
		response, err := sv.postForm(caller, freeClassroomPath+"/search", [][2]string{
			{"weeks", strings.Join(weeks, ",")},
			{"jslxdm", query.TypeCode},
			{"codeCampusListNumber", query.CampusNumber},
			{"teaNum", query.BuildingNumber},
			{"wSection", strings.Join(slots, ",")},
			{"pageNum", strconv.Itoa(page)},
			{"pageSize", strconv.Itoa(freeClassroomPageSize)},
		}, ajaxHeaders, session.Read)
		if err != nil {
			return nil, err
		}
		if err := common.RequireSuccess(response); err != nil {
			return nil, err
		}
		value, err := common.ParseJSON(response.BodyText())
		if err != nil {
			return nil, err
		}
		answers := common.ArrayItems(value)
		if len(answers) == 0 {
			return nil, errs.NewParse("free classroom: search answer has no page")
		}
		total := common.JSONI64(common.RawField(answers[0], "pageContext"), "totalCount")
		records := common.ArrayItems(common.RawField(answers[0], "records"))
		for _, record := range records {
			if room, ok := freeSearchRoom(record); ok {
				out = append(out, room)
			}
		}
		if len(records) == 0 || int64(len(out)) >= total {
			break
		}
	}
	return out, nil
}

// FreeClassroomRooms lists every classroom of one building with its status
// code, whether or not it is free. The building name is the caller's, since
// the school's room list does not carry it.
func (sv *Service) FreeClassroomRooms(caller context.Context, campusNumber, buildingNumber, buildingName string) ([]FreeClassroom, error) {
	if err := requireFreeBuilding(campusNumber, buildingNumber, buildingName); err != nil {
		return nil, err
	}
	response, err := sv.get(caller, freeClassroomRoomPath+"/"+campusNumber+"/"+buildingNumber+"/classroomJson", nil)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	value, err := common.ParseJSON(response.BodyText())
	if err != nil {
		return nil, err
	}
	out := []FreeClassroom{}
	for _, item := range common.ArrayItems(value) {
		number := common.JSONString(common.RawField(item, "id"), "classroomNumber")
		if number == "" {
			continue
		}
		out = append(out, FreeClassroom{
			CampusNumber:    campusNumber,
			CampusName:      freeCampusName(campusNumber),
			BuildingNumber:  buildingNumber,
			BuildingName:    buildingName,
			ClassroomNumber: number,
			ClassroomName:   freeNameOr(common.JSONString(item, "classroomName"), number),
			StatusCode:      common.JSONString(item, "classroomStatusCode"),
			TypeCode:        common.JSONString(item, "classroomTypeCode"),
			Seats:           optJSONI64(item, "placeNum"),
		})
	}
	return out, nil
}

// FreeClassroomOccupancy returns the occupied slots of one classroom as
// "weekday_week_section" tokens, sorted and without duplicates.
func (sv *Service) FreeClassroomOccupancy(caller context.Context, campusNumber, buildingNumber, buildingName, classroomNumber string) ([]string, error) {
	if err := requireFreeBuilding(campusNumber, buildingNumber, buildingName); err != nil {
		return nil, err
	}
	if classroomNumber == "" {
		return nil, errs.NewInvalidArgument("classroom_number is required")
	}
	target := config.WithQuery(sv.url(freeClassroomPath+"/classroomQuery"), [][2]string{
		{"campusName", freeCampusName(campusNumber)},
		{"teachingBuildingName", buildingName},
		{"classroomName", classroomNumber},
		{"campusNumber", campusNumber},
		{"teachingBuildingNumber", buildingNumber},
		{"classroomNumber", classroomNumber},
	})
	response, err := sv.getURL(caller, target)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(response.BodyText()))
	if err != nil {
		return nil, errs.NewParse("classroom detail parse failed: " + err.Error())
	}
	value, ok := doc.Find("#wsSection").First().Attr("value")
	if !ok {
		return nil, errs.NewParse("占用信息缺失")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token != "" && !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return out, nil
}

// freeSearchRoom reads one search record. Records without a classroom number are skipped.
func freeSearchRoom(record json.RawMessage) (FreeClassroom, bool) {
	id := common.RawField(record, "id")
	number := common.JSONString(id, "classroomNumber")
	if number == "" {
		return FreeClassroom{}, false
	}
	return FreeClassroom{
		CampusNumber:    common.JSONString(id, "campusNumber"),
		CampusName:      common.JSONString(record, "campusName"),
		BuildingNumber:  common.JSONString(id, "teachingBuildingNumber"),
		BuildingName:    common.JSONString(record, "teachingBuildingName"),
		ClassroomNumber: number,
		ClassroomName:   freeNameOr(common.JSONString(record, "classroomName"), number),
		StatusCode:      common.JSONString(record, "classroomStatusCode"),
		TypeCode:        common.JSONString(record, "classroomTypeCode"),
		TypeName:        common.JSONString(record, "classroomTypeName"),
		Seats:           optJSONI64(record, "classNumberOfSeats"),
	}, true
}

// normalizeFreeWeeks validates and sorts the week list. The school answers an
// empty week list with unreliable rows, so at least one week is required.
func normalizeFreeWeeks(weeks []string) ([]string, error) {
	if len(weeks) == 0 {
		return nil, errs.NewInvalidArgument("weeks must not be empty")
	}
	values := make([]int, 0, len(weeks))
	for _, raw := range weeks {
		week, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || week < 1 || week > freeMaxWeek {
			return nil, errs.NewInvalidArgument("week must be 1-24: " + raw)
		}
		values = append(values, week)
	}
	sort.Ints(values)
	out := make([]string, 0, len(values))
	for _, week := range values {
		out = append(out, strconv.Itoa(week))
	}
	return out, nil
}

// normalizeFreeSlots validates and sorts the slot list. Like weeks, at least
// one slot is required.
func normalizeFreeSlots(slots []string) ([]string, error) {
	if len(slots) == 0 {
		return nil, errs.NewInvalidArgument("slots must not be empty")
	}
	type slot struct{ weekday, section int }
	parsed := make([]slot, 0, len(slots))
	for _, raw := range slots {
		day, section, ok := strings.Cut(strings.TrimSpace(raw), "/")
		if !ok {
			return nil, freeSlotError(raw)
		}
		weekday, err1 := strconv.Atoi(day)
		sec, err2 := strconv.Atoi(section)
		if err1 != nil || err2 != nil || weekday < 1 || weekday > freeMaxWeekday || sec < 1 || sec > freeMaxSection {
			return nil, freeSlotError(raw)
		}
		parsed = append(parsed, slot{weekday: weekday, section: sec})
	}
	sort.Slice(parsed, func(i, j int) bool {
		if parsed[i].weekday != parsed[j].weekday {
			return parsed[i].weekday < parsed[j].weekday
		}
		return parsed[i].section < parsed[j].section
	})
	out := make([]string, 0, len(parsed))
	for _, s := range parsed {
		out = append(out, strconv.Itoa(s.weekday)+"/"+strconv.Itoa(s.section))
	}
	return out, nil
}

func freeSlotError(raw string) error {
	return errs.NewInvalidArgument("slot must be weekday/section with weekday 1-7 and section 1-10: " + raw)
}

func requireFreeBuilding(campusNumber, buildingNumber, buildingName string) error {
	if !isDigitsOnly(campusNumber) || !isDigitsOnly(buildingNumber) {
		return errs.NewInvalidArgument("campus_number and building_number must be numeric")
	}
	if buildingName == "" {
		return errs.NewInvalidArgument("building_name is required")
	}
	return nil
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func freeCampusName(campusNumber string) string {
	switch campusNumber {
	case "2":
		return "西校"
	case "1":
		return "东校"
	}
	return campusNumber
}

func freeNameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}
