package jwc

import (
	"encoding/json"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

// ==================== Generic select parsing ====================

func parseTermSelect(page, selector string) ([]TermItem, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, errs.NewParse("select parse failed: " + err.Error())
	}
	selectSel := doc.Find(selector).First()
	if selectSel.Length() == 0 {
		return nil, errs.NewParse("select not found: " + selector)
	}
	var terms []TermItem
	selectSel.Find("option").Each(func(index int, option *goquery.Selection) {
		code, _ := option.Attr("value")
		code = strings.TrimSpace(code)
		if code == "" {
			return
		}
		rawName := option.Text()
		name := strings.TrimSpace(rawName)
		name = strings.ReplaceAll(name, "春", "下")
		name = strings.ReplaceAll(name, "秋", "上")
		_, hasSelected := option.Attr("selected")
		terms = append(terms, TermItem{
			TermCode:  code,
			TermName:  name,
			IsCurrent: hasSelected || index == 0,
		})
	})
	return terms, nil
}

func parseSelectOptions(page, selector string) ([]ClassCurriculumOption, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, errs.NewParse("select parse failed: " + err.Error())
	}
	selectSel := doc.Find(selector).First()
	if selectSel.Length() == 0 {
		return []ClassCurriculumOption{}, nil
	}
	var options []ClassCurriculumOption
	selectSel.Find("option").Each(func(_ int, option *goquery.Selection) {
		code, _ := option.Attr("value")
		code = strings.TrimSpace(code)
		if code == "" {
			return
		}
		options = append(options, ClassCurriculumOption{
			Code: code,
			Name: strings.TrimSpace(option.Text()),
		})
	})
	return options, nil
}

// ==================== Score parsing ====================

func parseTermScoreResponse(body string) (*TermScoreResponse, error) {
	value, err := common.ParseJSON(body)
	if err != nil {
		return nil, err
	}
	if common.JSONString(value, "result") == "error" {
		return nil, errs.NewServer(-1, common.JSONString(value, "msg"))
	}
	list := common.RawField(value, "list")
	if list == nil || string(list) == "null" {
		return &TermScoreResponse{}, nil
	}
	var records []ScoreRecord
	for _, record := range common.ArrayItems(common.RawField(list, "records")) {
		if rec, ok := parseScoreArray(record); ok {
			records = append(records, rec)
		}
	}
	totalCount := int64(len(records))
	if pageContext := common.RawField(list, "pageContext"); pageContext != nil {
		totalCount = common.JSONI64(pageContext, "totalCount")
	}
	return &TermScoreResponse{TotalCount: totalCount, Records: records}, nil
}

// jsonArrayAt renders a non-string array element the way Rust's
// serde_json::Value::to_string does (compact JSON).
func jsonArrayAt(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return string(raw), true
}

func jsonArrayI64(raw json.RawMessage) int64 {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconvParseI64(s); err == nil {
			return v
		}
	}
	return 0
}

func parseScoreArray(record json.RawMessage) (ScoreRecord, bool) {
	arr := common.ArrayItems(record)
	if len(arr) < 11 {
		return ScoreRecord{}, false
	}
	at := func(index int) string {
		v, _ := jsonArrayAt(arr[index])
		return v
	}
	var retakeScore, makeupScore *string
	if len(arr) > 11 {
		if s, ok := jsonArrayAt(arr[11]); ok {
			retakeScore = &s
		}
	}
	if len(arr) > 12 {
		if s, ok := jsonArrayAt(arr[12]); ok {
			makeupScore = &s
		}
	}
	credits := at(6)
	if credits == "" {
		credits = "0"
	}
	return ScoreRecord{
		Sequence:     jsonArrayI64(arr[0]),
		TermID:       at(1),
		CourseCode:   at(2),
		CourseClass:  at(3),
		CourseNameCN: at(4),
		CourseNameEN: at(5),
		Credits:      credits,
		Hours:        jsonArrayI64(arr[7]),
		CourseType:   optString(at(8)),
		ExamType:     optString(at(9)),
		Score:        at(10),
		RetakeScore:  retakeScore,
		MakeupScore:  makeupScore,
		ExamTime:     nil,
	}, true
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseThisTermScoreRecord(item json.RawMessage) (ScoreRecord, bool) {
	id := common.RawField(item, "id")
	score := strings.TrimSpace(common.JSONString(item, "courseScore"))
	examTime := common.JSONString(id, "examtime")
	record := ScoreRecord{
		TermID:       common.JSONString(id, "executiveEducationPlanNumber"),
		CourseCode:   common.JSONString(id, "courseNumber"),
		CourseClass:  common.JSONString(item, "coureSequenceNumber"),
		CourseNameCN: common.JSONString(item, "courseName"),
		CourseNameEN: common.JSONString(item, "englishCourseName"),
		Credits:      common.JSONString(item, "credit"),
		Hours:        common.JSONI64(item, "studyHour"),
		CourseType:   common.JSONOptString(item, "coursePropertyName"),
		ExamType:     common.JSONOptString(item, "examTypeName"),
		Score:        score,
		ExamTime:     &examTime,
	}
	if record.CourseCode == "" && record.CourseNameCN == "" {
		return ScoreRecord{}, false
	}
	return record, true
}

// ==================== Exam parsing ====================

var (
	examNameRe = regexp.MustCompile(`[）)](.+)$`)
	seatRe     = regexp.MustCompile(`座位号[：:](.+?)(?:准考证号|$)`)
	examNoteRe = regexp.MustCompile(`准考证号[：:].*$`)
)

func parseExamSeats(page string) []SeatInfo {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil
	}
	var seats []SeatInfo
	doc.Find("div.widget-box").Each(func(_ int, box *goquery.Selection) {
		title := box.Find("h5.widget-title").First()
		main := box.Find("div.widget-main").First()
		if title.Length() == 0 || main.Length() == 0 {
			return
		}
		courseTitle := strings.TrimSpace(title.Text())
		courseName := courseTitle
		if m := examNameRe.FindStringSubmatch(courseTitle); m != nil {
			courseName = strings.TrimSpace(m[1])
		}
		if m := seatRe.FindStringSubmatch(strings.TrimSpace(main.Text())); m != nil {
			seat := strings.TrimSpace(m[1])
			if seat != "" {
				seats = append(seats, SeatInfo{CourseName: courseName, SeatNumber: seat})
			}
		}
	})
	return seats
}

func convertSchoolExam(exam *ExamScheduleItem, seats []SeatInfo) (*UnifiedExamInfo, bool) {
	var lines []string
	for _, line := range strings.Split(exam.Title, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	if len(lines) == 0 || lines[0] == "" {
		return nil, false
	}
	courseName := lines[0]
	examTime := ""
	if len(lines) > 1 {
		examTime = lines[1]
	}
	examLocation := ""
	if len(lines) > 2 {
		examLocation = strings.TrimSpace(strings.Join(lines[2:], " "))
	}
	note := ""
	for _, seat := range seats {
		if seat.CourseName == courseName {
			note = "座位号: " + seat.SeatNumber
			break
		}
	}
	note = strings.TrimSpace(examNoteRe.ReplaceAllString(note, ""))
	return &UnifiedExamInfo{
		CourseName:   courseName,
		ExamDate:     exam.Start,
		ExamTime:     examTime,
		ExamLocation: examLocation,
		ExamType:     "校统考",
		Note:         note,
	}, true
}

func parseOtherExam(record json.RawMessage) (OtherExamRecord, bool) {
	// Array-shaped records use positional fields; object records use the
	// upstream KCM/KSRQ/... keys.
	if string(record[0:1]) != "{" {
		arr := common.ArrayItems(record)
		if len(arr) < 8 {
			return OtherExamRecord{}, false
		}
		at := func(i int) string {
			var s string
			_ = json.Unmarshal(arr[i], &s)
			if s == "" && string(arr[i]) != "null" {
				s = string(arr[i])
			}
			return s
		}
		return OtherExamRecord{
			CourseName:   at(2),
			ExamLocation: at(6),
			ExamDate:     at(4),
			ExamTime:     at(5),
			Note:         at(7),
			RowNumber:    at(0),
		}, true
	}
	courseName := common.JSONString(record, "KCM")
	examDate := common.JSONString(record, "KSRQ")
	examTime := common.JSONString(record, "KSSJ")
	if courseName == "" && examDate == "" && examTime == "" {
		return OtherExamRecord{}, false
	}
	return OtherExamRecord{
		TermCode:     common.JSONString(record, "ZXJXJHH"),
		TermName:     common.JSONString(record, "ZXJXJHM"),
		ExamName:     common.JSONString(record, "KSMC"),
		CourseCode:   common.JSONString(record, "KCH"),
		CourseName:   courseName,
		ClassNumber:  common.JSONString(record, "KXH"),
		StudentID:    common.JSONString(record, "XH"),
		StudentName:  common.JSONString(record, "XM"),
		ExamLocation: common.JSONString(record, "KSDD"),
		ExamDate:     examDate,
		ExamTime:     examTime,
		Note:         common.JSONString(record, "BZ"),
		RowNumber:    common.JSONString(record, "RN"),
	}, true
}

func convertOtherExam(record *OtherExamRecord) *UnifiedExamInfo {
	return &UnifiedExamInfo{
		CourseName:   record.CourseName,
		ExamDate:     timeutil.ChineseDateToStandard(record.ExamDate),
		ExamTime:     record.ExamTime,
		ExamLocation: record.ExamLocation,
		ExamType:     "其他考试",
		Note:         record.Note,
	}
}

// ==================== Course schedule parsing ====================

func optJSONString(value json.RawMessage, key string) *string {
	return common.JSONOptString(value, key)
}

func optJSONI64(value json.RawMessage, key string) *int64 {
	raw := common.RawField(value, key)
	if raw == nil || string(raw) == "null" {
		return nil
	}
	v := common.JSONI64(value, key)
	return &v
}

func parseCourseScheduleRecord(item json.RawMessage) CourseScheduleRecord {
	return CourseScheduleRecord{
		ID:      optJSONString(item, "id"),
		Zxjxjhh: optJSONString(item, "zxjxjhh"),
		Kch:     optJSONString(item, "kch"),
		Kxh:     optJSONString(item, "kxh"),
		Kcm:     optJSONString(item, "kcm"),
		Xf:      optJSONI64(item, "xf"),
		Xs:      optJSONI64(item, "xs"),
		Kkxsh:   optJSONString(item, "kkxsh"),
		Kkxsjc:  optJSONString(item, "kkxsjc"),
		Kslxdm:  optJSONString(item, "kslxdm"),
		Kslxmc:  optJSONString(item, "kslxmc"),
		Skjs:    optJSONString(item, "skjs"),
		Bkskrl:  optJSONI64(item, "bkskrl"),
		Bkskyl:  optJSONI64(item, "bkskyl"),
		Xqh:     optJSONString(item, "xqh"),
		Jxlh:    optJSONString(item, "jxlh"),
		Jash:    optJSONString(item, "jash"),
		Skzc:    optJSONString(item, "skzc"),
		Skxq:    optJSONI64(item, "skxq"),
		Skjc:    optJSONI64(item, "skjc"),
		Cxjc:    optJSONI64(item, "cxjc"),
		Zcsm:    optJSONString(item, "zcsm"),
		Xqm:     optJSONString(item, "xqm"),
		Jxlm:    optJSONString(item, "jxlm"),
		Jasm:    optJSONString(item, "jasm"),
		Mxbj:    firstNonNil(optJSONString(item, "bm"), optJSONString(item, "bjh")),
		Xss:     optJSONI64(item, "xss"),
	}
}

func parseCourseScheduleRecordFromClass(item json.RawMessage) CourseScheduleRecord {
	id := common.RawField(item, "id")
	return CourseScheduleRecord{
		Zxjxjhh: optJSONString(id, "zxjxjhh"),
		Kch:     optJSONString(id, "kch"),
		Kxh:     optJSONString(id, "kxh"),
		Kcm:     optJSONString(item, "kcm"),
		Xf:      optJSONI64(item, "xf"),
		Kkxsh:   optJSONString(item, "kkxsh"),
		Kkxsjc:  optJSONString(item, "kkxsm"),
		Kslxdm:  optJSONString(item, "kslxdm"),
		Kslxmc:  optJSONString(item, "kslxmc"),
		Skjs:    optJSONString(item, "jsm"),
		Bkskyl:  optJSONI64(item, "bkskyl"),
		Xqh:     optJSONString(item, "xqh"),
		Jxlh:    optJSONString(item, "jxlh"),
		Jxlm:    optJSONString(item, "jxlm"),
		Jash:    optJSONString(item, "jash"),
		Jasm:    optJSONString(item, "jasm"),
		Skzc:    optJSONString(id, "skzc"),
		Skxq:    optJSONI64(id, "skxq"),
		Skjc:    optJSONI64(id, "skjc"),
		Cxjc:    optJSONI64(item, "cxjc"),
		Zcsm:    optJSONString(item, "zcsm"),
		Xqm:     optJSONString(item, "xqm"),
		Mxbj:    firstNonNil(optJSONString(item, "bm"), optJSONString(item, "bjh")),
		Xss:     optJSONI64(item, "xss"),
	}
}

func firstNonNil(values ...*string) *string {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// ==================== Student schedule parsing ====================

func parseStudentSchedule(value json.RawMessage) (*StudentScheduleResponse, error) {
	var dateList []ScheduleDateInfo
	for _, info := range common.ArrayItems(common.RawField(value, "dateList")) {
		var courses []ScheduleCourse
		for _, course := range common.ArrayItems(common.RawField(info, "selectCourseList")) {
			courses = append(courses, parseScheduleCourse(course))
		}
		dateList = append(dateList, ScheduleDateInfo{
			ProgramPlanCode:  common.JSONString(info, "programPlanCode"),
			ProgramPlanName:  common.JSONString(info, "programPlanName"),
			TotalUnits:       common.JSONF64(info, "totalUnits"),
			SelectCourseList: courses,
		})
	}
	var xkxx []json.RawMessage
	if raw := common.RawField(value, "xkxx"); raw != nil {
		xkxx = common.ArrayItems(raw)
	}
	if xkxx == nil {
		xkxx = []json.RawMessage{}
	}
	return &StudentScheduleResponse{
		AllUnits:     common.JSONF64(value, "allUnits"),
		ErrorMessage: common.JSONString(value, "errorMessage"),
		XKXX:         xkxx,
		ShowSite:     common.JSONBool(value, "showSite"),
		SchoolID:     common.JSONString(value, "school_id"),
		DateList:     dateList,
	}, nil
}

func parseScheduleCourse(value json.RawMessage) ScheduleCourse {
	id := common.RawField(value, "id")
	var slots []ScheduleTimePlace
	for _, slot := range common.ArrayItems(common.RawField(value, "timeAndPlaceList")) {
		slots = append(slots, ScheduleTimePlace{
			ID:                           common.JSONString(slot, "id"),
			ExecutiveEducationPlanNumber: common.JSONString(slot, "executiveEducationPlanNumber"),
			CoureNumber:                  common.JSONString(slot, "coureNumber"),
			CoureSequenceNumber:          common.JSONString(slot, "coureSequenceNumber"),
			StudentNumber:                common.JSONString(slot, "studentNumber"),
			ClassWeek:                    common.JSONString(slot, "classWeek"),
			ClassDay:                     common.JSONI64(slot, "classDay"),
			ClassSessions:                common.JSONI64(slot, "classSessions"),
			ContinuingSession:            common.JSONI64(slot, "continuingSession"),
			CampusName:                   common.JSONString(slot, "campusName"),
			TeachingBuildingName:         common.JSONString(slot, "teachingBuildingName"),
			ClassroomName:                common.JSONString(slot, "classroomName"),
			WeekDescription:              common.JSONString(slot, "weekDescription"),
			CoursePropertiesName:         common.JSONString(slot, "coursePropertiesName"),
			CoureName:                    common.JSONString(slot, "coureName"),
		})
	}
	return ScheduleCourse{
		ID: ScheduleCourseID{
			ExecutiveEducationPlanNumber: common.JSONString(id, "executiveEducationPlanNumber"),
			CoureNumber:                  common.JSONString(id, "coureNumber"),
			CoureSequenceNumber:          common.JSONString(id, "coureSequenceNumber"),
			StudentNumber:                common.JSONString(id, "studentNumber"),
		},
		ProgramPlanNumber:      common.JSONString(value, "programPlanNumber"),
		CourseName:             common.JSONString(value, "courseName"),
		Unit:                   common.JSONF64(value, "unit"),
		ProgramPlanName:        common.JSONString(value, "programPlanName"),
		AttendClassTeacher:     common.JSONString(value, "attendClassTeacher"),
		StudyModeCode:          common.JSONString(value, "studyModeCode"),
		StudyModeName:          common.JSONString(value, "studyModeName"),
		CoursePropertiesCode:   common.JSONString(value, "coursePropertiesCode"),
		CoursePropertiesName:   common.JSONString(value, "coursePropertiesName"),
		ExamTypeCode:           common.JSONString(value, "examTypeCode"),
		ExamTypeName:           common.JSONString(value, "examTypeName"),
		TimeAndPlaceList:       slots,
		SelectCourseStatusCode: common.JSONString(value, "selectCourseStatusCode"),
		SelectCourseStatusName: common.JSONString(value, "selectCourseStatusName"),
	}
}

// ==================== Plan parsing ====================

var planButtonRe = regexp.MustCompile(`getPyfaIndex\('(\d+)'\)`)

func parsePlanSelection(page string) (*PlanSelectionResponse, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, false
	}
	hint := (*string)(nil)
	if alert := doc.Find(".alert-warning").First(); alert.Length() > 0 {
		text := strings.ReplaceAll(strings.TrimSpace(alert.Text()), "\n", " ")
		hint = &text
	}
	var options []PlanOption
	doc.Find("button.btn-success.btn-round").Each(func(_ int, button *goquery.Selection) {
		onclick, _ := button.Attr("onclick")
		m := planButtonRe.FindStringSubmatch(onclick)
		if m == nil {
			return
		}
		planID := m[1]
		planName := strings.TrimSpace(button.Text())
		planType := "主修"
		if strings.Contains(planName, "辅修") {
			planType = "辅修"
		} else if strings.Contains(planName, "微专业") {
			planType = "微专业"
		}
		_, isCurrent := button.Attr("class")
		current := strings.Contains(attrOrEmpty(button, "class"), "btn-success")
		_ = isCurrent
		options = append(options, PlanOption{
			PlanID:    planID,
			PlanName:  planName,
			PlanType:  planType,
			IsCurrent: current,
		})
	})
	if len(options) == 0 {
		return nil, false
	}
	return &PlanSelectionResponse{Options: options, Hint: hint}, true
}

func attrOrEmpty(sel *goquery.Selection, name string) string {
	v, _ := sel.Attr(name)
	return v
}

var planHeaderRe = regexp.MustCompile(`(\d{4})级(.+?)本科培养方案`)

func parsePlanHeader(page string) (planName, major, grade string) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return "", "", ""
	}
	var result string
	doc.Find("h4.widget-title").EachWithBreak(func(_ int, el *goquery.Selection) bool {
		text := strings.TrimSpace(el.Text())
		if strings.Contains(text, "培养方案") {
			result = text
			return false
		}
		return true
	})
	if result == "" {
		return "", "", ""
	}
	if m := planHeaderRe.FindStringSubmatch(result); m != nil {
		return result, m[2], m[1]
	}
	return result, "", ""
}

var zTreePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\$\.fn\.zTree\.init\s*\(\s*\$\(\s*["']#treeDemo["']\s*\)\s*,\s*\w+\s*,\s*(\[.*?\])\s*\)`),
	regexp.MustCompile(`(?s)\.zTree\.init\s*\([^,]+,\s*[^,]+,\s*(\[.*?\])\s*\)`),
	regexp.MustCompile(`(?s)init\s*\(\s*\$\(\s*["']#treeDemo["']\s*\)[^,]*,\s*[^,]*,\s*(\[.*?\])`),
}

var (
	jsLineCommentRe  = regexp.MustCompile(`//[^\n]*`)
	jsBlockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	trailingCommaRe  = regexp.MustCompile(`,(\s*[}\]])`)
	whitespaceRe     = regexp.MustCompile(`\s+`)
)

func extractZTreeNodes(page string) ([]json.RawMessage, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, errs.NewParse("planCompletion: zTree data not found")
	}
	var found []json.RawMessage
	doc.Find("script").EachWithBreak(func(_ int, script *goquery.Selection) bool {
		content := script.Text()
		if !strings.Contains(content, "zTree.init") || !strings.Contains(content, "flagId") {
			return true
		}
		for _, re := range zTreePatterns {
			m := re.FindStringSubmatch(content)
			if m == nil {
				continue
			}
			cleaned := cleanJSArray(m[1])
			var items []json.RawMessage
			if err := json.Unmarshal([]byte(cleaned), &items); err == nil && len(items) > 0 {
				found = items
				return false
			}
		}
		return true
	})
	if found == nil {
		return nil, errs.NewParse("planCompletion: zTree data not found")
	}
	return found, nil
}

func cleanJSArray(raw string) string {
	value := jsLineCommentRe.ReplaceAllString(raw, "")
	value = jsBlockCommentRe.ReplaceAllString(value, "")
	value = trailingCommaRe.ReplaceAllString(value, "$1")
	value = strings.TrimSpace(whitespaceRe.ReplaceAllString(value, " "))
	return value
}

var statsRe = regexp.MustCompile(
	`([^(]+)\(最低修读学分:([0-9.]+),通过学分:([0-9.]+),已修课程门数:(\d+),已及格课程门数:(\d+),未及格课程门数:(\d+),必修课缺修门数:(\d+)\)`)

func buildCategoryTree(nodes []json.RawMessage) []PlanCategory {
	isCourse := func(node json.RawMessage) bool { return common.JSONString(node, "flagType") == "kch" }
	idOf := func(node json.RawMessage) string { return common.JSONString(node, "id") }
	pidOf := func(node json.RawMessage) string { return common.JSONString(node, "pId") }

	ids := map[string]bool{}
	for _, node := range nodes {
		ids[idOf(node)] = true
	}
	var roots []json.RawMessage
	pick := func(pred func(json.RawMessage) bool) {
		for _, node := range nodes {
			if pred(node) {
				roots = append(roots, node)
			}
		}
	}
	if len(roots) == 0 {
		pick(func(node json.RawMessage) bool { return !isCourse(node) && pidOf(node) == "-1" })
	}
	if len(roots) == 0 {
		pick(func(node json.RawMessage) bool {
			if isCourse(node) {
				return false
			}
			pid := pidOf(node)
			return pid == "" || !ids[pid]
		})
	}
	if len(roots) == 0 {
		pick(func(node json.RawMessage) bool { return !isCourse(node) && pidOf(node) == "0" })
	}
	if len(roots) == 0 {
		pick(func(node json.RawMessage) bool { return !isCourse(node) })
	}
	var out []PlanCategory
	for _, node := range roots {
		out = append(out, buildCategoryWithChildren(node, nodes))
	}
	return out
}

func buildCategoryWithChildren(node json.RawMessage, nodes []json.RawMessage) PlanCategory {
	base := planCategoryFromNode(node)
	categoryID := common.JSONString(node, "id")
	var subcategories []PlanCategory
	var courses []PlanCourse
	for _, child := range nodes {
		if common.JSONString(child, "pId") != categoryID {
			continue
		}
		flagType := common.JSONString(child, "flagType")
		childID := common.JSONString(child, "id")
		hasChildren := false
		for _, n := range nodes {
			if common.JSONString(n, "pId") == childID {
				hasChildren = true
				break
			}
		}
		if flagType == "kch" {
			courses = append(courses, planCourseFromNode(child))
		} else if flagType == "001" || flagType == "002" || hasChildren {
			// Explicit category flags and any node with children are categories.
			subcategories = append(subcategories, buildCategoryWithChildren(child, nodes))
		} else {
			courses = append(courses, planCourseFromNode(child))
		}
	}
	base.Subcategories = subcategories
	base.Courses = courses
	return base
}

func planCategoryFromNode(node json.RawMessage) PlanCategory {
	name := common.JSONString(node, "name")
	flagID := common.JSONString(node, "flagId")
	clean := common.CleanHTMLText(name)
	if caps := statsRe.FindStringSubmatch(clean); caps != nil {
		minCredits, _ := strconvParseF64(caps[2])
		completedCredits, _ := strconvParseF64(caps[3])
		total, _ := strconvParseI64(caps[4])
		passed, _ := strconvParseI64(caps[5])
		failed, _ := strconvParseI64(caps[6])
		missing, _ := strconvParseI64(caps[7])
		return PlanCategory{
			CategoryID:             flagID,
			CategoryName:           strings.TrimSpace(caps[1]),
			MinCredits:             minCredits,
			CompletedCredits:       completedCredits,
			TotalCourses:           total,
			PassedCourses:          passed,
			FailedCourses:          failed,
			MissingRequiredCourses: missing,
		}
	}
	return PlanCategory{CategoryID: flagID, CategoryName: clean}
}

var (
	courseCodeRe  = regexp.MustCompile(`\[([^\]]+)\]`)
	creditRe      = regexp.MustCompile(`\[([0-9.]+)学分\]`)
	parenRe       = regexp.MustCompile(`\(([^)]+)\)`)
	scoreDigitsRe = regexp.MustCompile(`([0-9.]+)`)
	examDateRe    = regexp.MustCompile(`(\d{8})`)
)

func planCourseFromNode(node json.RawMessage) PlanCourse {
	name := common.JSONString(node, "name")
	var isPassed bool
	statusDescription := "未修读"
	switch {
	case strings.Contains(name, "fa-smile-o fa-1x green"):
		isPassed = true
		statusDescription = "已通过"
	case strings.Contains(name, "fa-meh-o fa-1x light-grey"):
		statusDescription = "未修读"
	case strings.Contains(name, "fa-frown-o fa-1x red"):
		statusDescription = "未通过"
	}
	clean := common.CleanHTMLText(name)
	courseCode := ""
	courseName := ""
	var credits *float64
	var score *string
	var examDate *string
	courseType := ""
	if common.JSONString(node, "flagType") == "kch" {
		if caps := courseCodeRe.FindStringSubmatchIndex(clean); caps != nil {
			courseCode = clean[caps[2]:caps[3]]
			remaining := strings.TrimSpace(clean[caps[1]:])
			if cc := creditRe.FindStringSubmatch(remaining); cc != nil {
				if v, err := strconvParseF64(cc[1]); err == nil {
					credits = &v
				}
				remaining = strings.TrimSpace(creditRe.ReplaceAllString(remaining, ""))
			}
			if pc := parenRe.FindStringSubmatchIndex(remaining); pc != nil {
				content := remaining[pc[2]:pc[3]]
				courseName = strings.TrimSpace(remaining[:pc[0]])
				if sm := scoreDigitsRe.FindStringSubmatch(content); sm != nil {
					s := sm[1]
					score = &s
				}
				if dm := examDateRe.FindStringSubmatch(content); dm != nil {
					d := dm[1]
					examDate = &d
				}
				if first, _, found := strings.Cut(content, ","); found {
					courseType = strings.TrimSpace(first)
				}
			} else {
				courseName = remaining
			}
		}
	}
	var courseTypeOpt *string
	if courseType != "" {
		courseTypeOpt = &courseType
	}
	return PlanCourse{
		CourseCode:        courseCode,
		CourseName:        courseName,
		Credits:           credits,
		Score:             score,
		ExamDate:          examDate,
		CourseType:        courseTypeOpt,
		IsPassed:          isPassed,
		StatusDescription: statusDescription,
	}
}

// ==================== Teacher evaluation parsing ====================

var tokenAttrRe = regexp.MustCompile(`(?:id|name)=["']tokenValue["'][^>]*value=["']([^"']+)["']`)

func parseEvalToken(page string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return ""
	}
	for _, selector := range []string{"input#tokenValue", `input[name=tokenValue]`} {
		input := doc.Find(selector).First()
		if input.Length() > 0 {
			if value, ok := input.Attr("value"); ok && value != "" {
				return value
			}
		}
	}
	if m := tokenAttrRe.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return ""
}

func isEvalClosed(page string) bool {
	return strings.Contains(page, "评估开关已关闭")
}

func evalClosedMessage(page string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return ""
	}
	var message string
	doc.Find(".alert").EachWithBreak(func(_ int, el *goquery.Selection) bool {
		text := el.Text()
		if strings.Contains(text, "评估开关已关闭") {
			message = strings.TrimSpace(text)
			return false
		}
		return true
	})
	return message
}

var (
	evalIsFinishedRe   = regexp.MustCompile(`var\s+isFinished\s*=\s*["']([^"']*)["']`)
	evalIsFirstVisitRe = regexp.MustCompile(`var\s+isFirstVisit\s*=\s*["']([^"']*)["']`)
)

// parseEvalProtection reads the first-visit protection gate from the index page.
func parseEvalProtection(page string) TeacherEvaluationIndexProtection {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return TeacherEvaluationIndexProtection{}
	}
	inputValue := func(id string) string {
		value, _ := doc.Find("input#" + id).First().Attr("value")
		return strings.TrimSpace(value)
	}
	var scripts []string
	doc.Find("script").Each(func(_ int, script *goquery.Selection) {
		scripts = append(scripts, script.Text())
	})
	joined := strings.Join(scripts, "\n")
	protection := TeacherEvaluationIndexProtection{
		SchoolID:     inputValue("schoolId"),
		InfoOff:      inputValue("infoOff"),
		HasAhcjTimer: strings.Contains(joined, "tsxxAhcj"),
	}
	protection.InfoTimeSeconds, _ = strconv.Atoi(inputValue("infoTime"))
	if m := evalIsFinishedRe.FindStringSubmatch(joined); m != nil {
		protection.IsFinished = strings.TrimSpace(m[1])
		protection.IsFinishedResolved = true
	}
	if m := evalIsFirstVisitRe.FindStringSubmatch(joined); m != nil {
		protection.IsFirstVisit = strings.TrimSpace(m[1])
		protection.IsFirstVisitResolved = true
	}
	return protection
}

func parseEvalCourse(value json.RawMessage) (TeacherEvaluationCourse, bool) {
	id := common.RawField(value, "id")
	questionnaire := common.RawField(value, "questionnaire")
	course := TeacherEvaluationCourse{
		Name:                    common.JSONString(value, "evaluationContent"),
		Teacher:                 common.JSONString(value, "evaluatedPeople"),
		EvaluatedPeople:         common.JSONString(value, "evaluatedPeople"),
		EvaluatedPeopleNumber:   common.JSONString(id, "evaluatedPeople"),
		CoureSequenceNumber:     common.JSONString(id, "coureSequenceNumber"),
		EvaluationContentNumber: common.JSONString(id, "evaluationContentNumber"),
		QuestionnaireCode:       common.JSONString(questionnaire, "questionnaireNumber"),
		QuestionnaireName:       common.JSONString(questionnaire, "questionnaireName"),
		IsEvaluated:             common.JSONString(value, "isEvaluated") == "是",
	}
	if course.Name == "" && course.Teacher == "" && course.EvaluationContentNumber == "" {
		return TeacherEvaluationCourse{}, false
	}
	return course, true
}

func evalPageForm(course *TeacherEvaluationCourse, pendingCount int64, indexToken string) [][2]string {
	return [][2]string{
		{"count", itoa(pendingCount)},
		{"evaluatedPeople", course.EvaluatedPeople},
		{"evaluatedPeopleNumber", course.EvaluatedPeopleNumber},
		{"questionnaireCode", course.QuestionnaireCode},
		{"questionnaireName", course.QuestionnaireName},
		{"coureSequenceNumber", course.CoureSequenceNumber},
		{"evaluationContentNumber", course.EvaluationContentNumber},
		{"evaluationContentContent", ""},
		{"tokenValue", indexToken},
	}
}

func parseQuestionnaire(page string) (*TeacherEvaluationQuestionnaire, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, errs.NewParse("questionnaire parse failed: " + err.Error())
	}
	title := ""
	for _, selector := range []string{"div.title", "h1", "h2"} {
		if sel := doc.Find(selector).First(); sel.Length() > 0 {
			title = strings.TrimSpace(sel.Text())
			break
		}
	}
	inputValue := func(name string) string {
		v, _ := doc.Find(`input[name=` + name + `]`).First().Attr("value")
		return v
	}
	return &TeacherEvaluationQuestionnaire{
		Title:                 title,
		TokenValue:            parseEvalToken(page),
		QuestionnaireCode:     inputValue("wjdm"),
		EvaluatedPeopleNumber: inputValue("bprdm"),
		EvaluationContent:     inputValue("pgnr"),
		RadioQuestions:        parseRadioQuestions(doc),
		TextQuestions:         parseTextQuestions(doc),
	}, nil
}

func parseRadioQuestions(doc *goquery.Document) []TeacherEvaluationRadioQuestion {
	var order []string
	groups := map[string][]TeacherEvaluationOption{}
	titles := map[string]string{}
	doc.Find(`input[type=radio][name]`).Each(func(_ int, radio *goquery.Selection) {
		name, _ := radio.Attr("name")
		if name == "" {
			return
		}
		value, _ := radio.Attr("value")
		if value == "" {
			return
		}
		score, weight := 0.0, 0.0
		if s, w, found := strings.Cut(value, "_"); found {
			score, _ = strconvParseF64(s)
			weight, _ = strconvParseF64(w)
		}
		label := ""
		if id, ok := radio.Attr("id"); ok && id != "" {
			if l := doc.Find(`label[for="` + id + `"]`).First(); l.Length() > 0 {
				label = strings.TrimSpace(l.Text())
			}
		}
		if label == "" {
			if parent := radio.Closest("label"); parent.Length() > 0 {
				label = strings.TrimSpace(parent.Text())
			}
		}
		if label == "" {
			if parent := radio.Closest("td"); parent.Length() > 0 {
				label = strings.TrimSpace(parent.Text())
			}
		}
		if _, exists := groups[name]; !exists {
			order = append(order, name)
			title := ""
			if row := radio.Closest("tr"); row.Length() > 0 {
				row.Find("td").Each(func(_ int, cell *goquery.Selection) {
					if title == "" && len([]rune(strings.TrimSpace(cell.Text()))) > 5 {
						title = strings.TrimSpace(cell.Text())
					}
				})
			}
			titles[name] = title
		}
		groups[name] = append(groups[name], TeacherEvaluationOption{
			Key: name, Value: value, Score: score, Weight: weight, Label: label,
		})
	})
	var questions []TeacherEvaluationRadioQuestion
	for _, key := range order {
		options := groups[key]
		if len(options) == 0 {
			continue
		}
		questions = append(questions, TeacherEvaluationRadioQuestion{
			Title:   titles[key],
			Key:     key,
			Options: options,
		})
	}
	return questions
}

func parseTextQuestions(doc *goquery.Document) []TeacherEvaluationTextQuestion {
	var questions []TeacherEvaluationTextQuestion
	doc.Find(`textarea[name]`).Each(func(_ int, textarea *goquery.Selection) {
		name, _ := textarea.Attr("name")
		if name == "" {
			return
		}
		title := ""
		if row := textarea.Closest("tr"); row.Length() > 0 {
			if cell := row.Find("td").First(); cell.Length() > 0 {
				title = strings.TrimSpace(cell.Text())
			}
		}
		questions = append(questions, TeacherEvaluationTextQuestion{
			Key:      name,
			Title:    title,
			Required: name == "zgpj" || strings.Contains(name, "zgpj"),
			Kind:     classifyTextType(name, title),
		})
	})
	return questions
}

func classifyTextType(name, title string) string {
	if name == "zgpj" || strings.Contains(name, "zgpj") {
		return "overall"
	}
	if strings.Contains(title, "启发") || strings.Contains(title, "启示") {
		return "inspiration"
	}
	if strings.Contains(title, "建议") || strings.Contains(title, "意见") || strings.Contains(title, "改进") {
		return "suggestion"
	}
	return "general"
}

// chooseOption mirrors the Rust selection strategy: 80% full-weight, else
// second-highest weight, with deterministic rng ordering. TeacherEvalScoreMax
// instead picks uniformly among full-weight options when there are any.
func chooseOption(question *TeacherEvaluationRadioQuestion, rng *common.SimpleRng, mode TeacherEvalScoreMode) (string, bool) {
	if len(question.Options) == 0 {
		return "", false
	}
	sorted := make([]TeacherEvaluationOption, len(question.Options))
	copy(sorted, question.Options)
	// Stable sort by weight descending (Rust sort_by is stable).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Weight > sorted[j-1].Weight; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var full []TeacherEvaluationOption
	for _, option := range sorted {
		if absF(option.Weight-1.0) < 1e-9 {
			full = append(full, option)
		}
	}
	if mode == TeacherEvalScoreMax && len(full) > 0 {
		return full[rng.Below(len(full))].Value, true
	}
	if len(full) > 0 && rng.NextF64() < 0.8 {
		return full[rng.Below(len(full))].Value, true
	}
	secondWeight := 0.0
	hasSecond := false
	for _, option := range sorted {
		if absF(option.Weight-1.0) > 1e-9 {
			secondWeight = option.Weight
			hasSecond = true
			break
		}
	}
	var candidates []TeacherEvaluationOption
	if hasSecond {
		for _, option := range sorted {
			if absF(option.Weight-secondWeight) < 1e-9 {
				candidates = append(candidates, option)
			}
		}
	} else {
		candidates = sorted
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[rng.Below(len(candidates))].Value, true
}

var evalTextPools = map[string][]string{
	"inspiration": {
		"课程内容对我很有启发，帮助我从不同角度理解专业问题，也提升了分析和解决问题的能力",
		"老师的讲解让我对课程知识有了新的认识，课堂案例也启发我把理论和实际问题联系起来思考",
		"这门课让我收获很多，不仅理解了知识点，也学会了更有条理地分析问题和表达自己的观点",
	},
	"suggestion": {
		"老师讲课很好，很认真负责，我没有什么建议，希望老师继续保持现有的教学方式",
		"整体教学效果很好，建议后续可以适当增加课堂互动和案例拓展，帮助同学进一步巩固理解",
		"目前课程安排比较合理，没有明显建议，希望继续保持认真负责的教学态度和清晰的讲解方式",
	},
	"overall": {
		"老师授课有条理有重点，教会我做事要分清主次、抓住关键的思维方法",
		"老师讲课认真负责，课程内容充实丰富，理论与实践结合得很好，让我收获颇丰，对专业知识有了更深入的理解",
		"老师教学认真细致，课堂安排合理，知识点讲解清楚，能够结合实际帮助我们理解课程内容",
		"课程内容讲解清晰，老师备课充分，课堂节奏适中，整体学习体验很好，收获也比较明显",
	},
}

func randomText(kind string, rng *common.SimpleRng) string {
	pool := evalTextPools[kind]
	if pool == nil {
		pool = evalTextPools["overall"]
	}
	for i := 0; i < 3; i++ {
		answer := sanitizeAnswer(pool[rng.Below(len(pool))])
		if isValidAnswer(answer) {
			return answer
		}
	}
	return ""
}

func sanitizeAnswer(input string) string {
	return whitespaceRe.ReplaceAllString(input, "")
}

func isValidAnswer(answer string) bool {
	runes := []rune(answer)
	if len(runes) < 4 {
		return false
	}
	// Reject three identical consecutive characters.
	for i := 0; i+2 < len(runes); i++ {
		if runes[i] == runes[i+1] && runes[i+1] == runes[i+2] {
			return false
		}
	}
	return true
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func strconvParseI64(s string) (int64, error) {
	v, err := strconvParseF64(s)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

func strconvParseF64(s string) (float64, error) {
	return parseFloat(s)
}

func parseFloat(s string) (float64, error) {
	var f float64
	if s == "" {
		return 0, errBadNumber
	}
	neg := false
	i := 0
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		i++
	}
	intPart := 0.0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		intPart = intPart*10 + float64(s[i]-'0')
	}
	if i < len(s) && s[i] == '.' {
		i++
		scale := 0.1
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			intPart += float64(s[i]-'0') * scale
			scale /= 10
		}
	}
	if i != len(s) {
		return 0, errBadNumber
	}
	if neg {
		intPart = -intPart
	}
	f = intPart
	return f, nil
}

type badNumberError struct{}

func (badNumberError) Error() string { return "bad number" }

var errBadNumber = badNumberError{}
