// Package labor ports the labor-club (劳动俱乐部) service operations.
//
// Source: desktop/lib/services/labor_club/labor_club_service.dart; mirrored
// by iOS/LaborClubService.swift and Android/LaborClubService.kt. Ticket
// minting uses the same CAS callback pattern as AAC.
package labor

import (
	"context"
	"encoding/json"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

// Service gives access to labor-club operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// ProgressInfo is the labor-course progress.
type ProgressInfo struct {
	SumScore float64 `json:"sum_score"`
	Progress float64 `json:"progress"`
}

// FinishCount floors progress/10.
func (p ProgressInfo) FinishCount() int64 {
	return int64(p.Progress / 10.0)
}

// IsCompleted reports whether progress >= 100.
func (p ProgressInfo) IsCompleted() bool { return p.Progress >= 100.0 }

// Activity is a labor-club activity.
type Activity struct {
	ID              string  `json:"id"`
	Ico             *string `json:"ico"`
	State           int64   `json:"state"`
	StateName       string  `json:"state_name"`
	TypeID          string  `json:"type_id"`
	TypeName        string  `json:"type_name"`
	Title           string  `json:"title"`
	StartTime       string  `json:"start_time"`
	EndTime         string  `json:"end_time"`
	ChargeUserNo    string  `json:"charge_user_no"`
	ChargeUserName  string  `json:"charge_user_name"`
	ClubID          string  `json:"club_id"`
	ClubName        string  `json:"club_name"`
	MemberNum       int64   `json:"member_num"`
	AddTime         string  `json:"add_time"`
	PeopleNum       int64   `json:"people_num"`
	PeopleNumMin    *int64  `json:"people_num_min"`
	IsJoined        *bool   `json:"is_joined"`
	IsClosed        *bool   `json:"is_closed"`
	SignUpStartTime string  `json:"sign_up_start_time"`
	SignUpEndTime   string  `json:"sign_up_end_time"`
}

// CanApply mirrors the reference rule.
func (a Activity) CanApply() bool {
	if a.IsClosed != nil && *a.IsClosed {
		return false
	}
	if a.IsJoined != nil && *a.IsJoined {
		return false
	}
	return a.MemberNum < a.PeopleNum
}

// ClubInfo is a joined club.
type ClubInfo struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	TypeName     *string `json:"type_name"`
	Ico          *string `json:"ico"`
	ChairmanName *string `json:"chairman_name"`
	MemberNum    int64   `json:"member_num"`
}

// FormField is an activity form field.
type FormField struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsMust    bool   `json:"is_must"`
	FieldType int64  `json:"field_type"`
	Value     string `json:"value"`
}

// FlowData is one flow node.
type FlowData struct {
	NodeName string `json:"node_name"`
	UserName string `json:"user_name"`
	IsAdopt  *bool  `json:"is_adopt"`
	Time     string `json:"time"`
}

// Teacher is an activity teacher.
type Teacher struct {
	Name   string `json:"name"`
	UserNo string `json:"user_no"`
}

// SignItem is one sign-in record.
type SignItem struct {
	ID        string  `json:"id"`
	ItemType  int64   `json:"item_type"`
	TypeName  string  `json:"type_name"`
	StartTime string  `json:"start_time"`
	EndTime   string  `json:"end_time"`
	IsSign    bool    `json:"is_sign"`
	SignTime  *string `json:"sign_time"`
}

// ActivityDetail merges root-level formData/flowData/teacherList.
type ActivityDetail struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	StartTime       string      `json:"start_time"`
	EndTime         string      `json:"end_time"`
	ChargeUserName  string      `json:"charge_user_name"`
	ClubName        string      `json:"club_name"`
	MemberNum       int64       `json:"member_num"`
	PeopleNum       int64       `json:"people_num"`
	FormData        []FormField `json:"form_data"`
	FlowData        []FlowData  `json:"flow_data"`
	TeacherList     []Teacher   `json:"teacher_list"`
	SignList        []SignItem  `json:"sign_list"`
	SignUpStartTime *string     `json:"sign_up_start_time"`
	SignUpEndTime   *string     `json:"sign_up_end_time"`
}

// SignInRequest is the QR sign-in payload.
type SignInRequest struct {
	Content  string `json:"content"`
	Location string `json:"location"`
}

// SignInResponse is the QR sign-in result.
type SignInResponse struct {
	Code int64           `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// IsSuccess mirrors code == 0.
func (r SignInResponse) IsSuccess() bool { return r.Code == 0 }

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().LaborAPIBase, path)
}

// Ticket resolves (and caches) a labor-club CAS ticket.
func (sv *Service) Ticket(caller context.Context, forceRefresh bool) (string, error) {
	if !forceRefresh {
		if cached, ok := sv.s.CachedTicket("labor"); ok {
			return cached, nil
		}
	}
	locations, err := sv.s.RedirectLocations(caller, config.LaborLoginService, 16)
	if err != nil {
		return "", err
	}
	for _, location := range locations {
		if contains(location, "register?ticket=") {
			if ticket, ok := common.ExtractTicketFromURL(location); ok {
				sv.s.StoreTicket("labor", ticket)
				return ticket, nil
			}
		}
	}
	return "", errs.NewParse("labor ticket not found in redirect chain")
}

// ResetTicket resets the cached labor ticket.
func (sv *Service) ResetTicket() { sv.s.ResetServiceTicket("labor") }

func (sv *Service) post(caller context.Context, path string, form [][2]string, kind session.Kind) (*transport.Response, error) {
	ticket, err := sv.Ticket(caller, false)
	if err != nil {
		return nil, err
	}
	twfid, _ := sv.s.TWFID()
	request := transport.NewRequest(transport.Post, sv.url(path)).
		Header("Content-Type", "application/x-www-form-urlencoded").
		Header("ticket", ticket).
		Header("sdp-app-session", twfid).
		WithBody([]byte(config.FormEncode(form)))
	if kind == session.Read {
		return sv.s.SendRead(caller, request)
	}
	return sv.s.SendWrite(caller, request)
}

func (sv *Service) data(caller context.Context, path string, form [][2]string) (json.RawMessage, error) {
	response, err := sv.post(caller, path, form, session.Read)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	code, data, err := common.Envelope(response.BodyText())
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errs.NewServer(code, "labor service error at "+path)
	}
	return data, nil
}

// Progress returns labor-course progress.
func (sv *Service) Progress(caller context.Context) (*ProgressInfo, error) {
	data, err := sv.data(caller, "/User/Center/DoGetScoreInfo", nil)
	if err != nil {
		return nil, err
	}
	return &ProgressInfo{
		SumScore: common.JSONF64(data, "SumScore"),
		Progress: common.JSONF64(data, "Progress"),
	}, nil
}

// JoinedActivities returns joined activities.
func (sv *Service) JoinedActivities(caller context.Context) ([]Activity, error) {
	data, err := sv.data(caller, "/User/Activity/DoGetJoinPageList",
		[][2]string{{"pageIndex", "1"}, {"pageSize", "100"}})
	if err != nil {
		return nil, err
	}
	return parseActivities(data), nil
}

// JoinedClubs returns joined clubs.
func (sv *Service) JoinedClubs(caller context.Context) ([]ClubInfo, error) {
	data, err := sv.data(caller, "/User/Club/DoGetJoinList", nil)
	if err != nil {
		return nil, err
	}
	items := common.ArrayItems(data)
	out := make([]ClubInfo, 0, len(items))
	for _, item := range items {
		out = append(out, ClubInfo{
			ID:           common.JSONString(item, "ID"),
			Name:         common.JSONString(item, "Name"),
			TypeName:     common.JSONOptString(item, "TypeName"),
			Ico:          common.JSONOptString(item, "Ico"),
			ChairmanName: common.JSONOptString(item, "CairmanName"),
			MemberNum:    common.JSONI64(item, "MemberNum"),
		})
	}
	return out, nil
}

// ClubActivities returns activities belonging to a club.
func (sv *Service) ClubActivities(caller context.Context, clubID string, pageIndex, pageSize int64) ([]Activity, error) {
	data, err := sv.data(caller, "/User/Activity/DoGetPageList", [][2]string{
		{"clubID", clubID},
		{"pageIndex", itoa(pageIndex)},
		{"pageSize", itoa(pageSize)},
	})
	if err != nil {
		return nil, err
	}
	return parseActivities(data), nil
}

// ApplyActivity applies to an activity (write).
func (sv *Service) ApplyActivity(caller context.Context, activityID, reason string) (string, error) {
	response, err := sv.post(caller, "/User/Activity/DoApplyJoin",
		[][2]string{{"activityID", activityID}, {"reason", reason}}, session.Write)
	if err != nil {
		return "", err
	}
	if err := common.RequireSuccess(response); err != nil {
		return "", err
	}
	code, _, err := common.Envelope(response.BodyText())
	if err != nil {
		return "", err
	}
	body := response.BodyText()
	message := common.JSONString(json.RawMessage(body), "msg")
	if code != 0 {
		if message == "" {
			message = "报名失败"
		}
		return "", errs.NewServer(code, message)
	}
	if message == "" {
		return "报名成功", nil
	}
	return message, nil
}

// ScanSignIn scans a QR code to sign in (write).
func (sv *Service) ScanSignIn(caller context.Context, request *SignInRequest) (*SignInResponse, error) {
	response, err := sv.post(caller, "/User/Center/DoScanSignQRImage",
		[][2]string{{"content", request.Content}, {"location", request.Location}}, session.Write)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	body := response.BodyText()
	return &SignInResponse{
		Code: common.JSONI64(json.RawMessage(body), "code"),
		Msg:  common.JSONString(json.RawMessage(body), "msg"),
		Data: common.RawField(json.RawMessage(body), "data"),
	}, nil
}

// SignList returns the sign-in list for an activity.
func (sv *Service) SignList(caller context.Context, activityID string, itemType, pageIndex, pageSize int64) ([]SignItem, error) {
	response, err := sv.post(caller, "/User/Activity/DoGetSignList", [][2]string{
		{"activityID", activityID},
		{"type", itoa(itemType)},
		{"pageIndex", itoa(pageIndex)},
		{"pageSize", itoa(pageSize)},
	}, session.Read)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	value := json.RawMessage(response.BodyText())
	code := common.JSONI64(value, "code")
	if code != 0 {
		return nil, errs.NewServer(code, "sign list failed")
	}
	items := common.ArrayItems(common.RawField(value, "data"))
	out := make([]SignItem, 0, len(items))
	for _, item := range items {
		out = append(out, parseSignItem(item))
	}
	return out, nil
}

// ActivityDetail returns the activity detail (merges root-level
// formData/flowData/teacherList).
func (sv *Service) ActivityDetail(caller context.Context, activityID string) (*ActivityDetail, error) {
	response, err := sv.post(caller, "/User/Activity/DoGetDetail",
		[][2]string{{"id", activityID}}, session.Read)
	if err != nil {
		return nil, err
	}
	if err := common.RequireSuccess(response); err != nil {
		return nil, err
	}
	root := json.RawMessage(response.BodyText())
	code := common.JSONI64(root, "code")
	if code != 0 {
		return nil, errs.NewServer(code, "activity detail failed")
	}
	// Merge root-level formData/flowData/teacherList into the data object.
	var merged map[string]json.RawMessage
	if data := common.RawField(root, "data"); data != nil && string(data) != "null" {
		if err := json.Unmarshal(data, &merged); err != nil {
			merged = map[string]json.RawMessage{}
		}
	} else {
		merged = map[string]json.RawMessage{}
	}
	for _, key := range []string{"formData", "flowData", "teacherList"} {
		if v := common.RawField(root, key); v != nil {
			merged[key] = v
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, errs.NewParse("activity detail merge failed")
	}
	return parseActivityDetail(json.RawMessage(raw)), nil
}

func parseActivities(data json.RawMessage) []Activity {
	items := common.ArrayItems(data)
	out := make([]Activity, 0, len(items))
	for _, item := range items {
		out = append(out, parseActivity(item))
	}
	return out
}

func parseActivity(value json.RawMessage) Activity {
	return Activity{
		ID:              common.JSONString(value, "ID"),
		Ico:             common.JSONOptString(value, "Ico"),
		State:           common.JSONI64(value, "State"),
		StateName:       common.JSONString(value, "StateName"),
		TypeID:          common.JSONString(value, "TypeID"),
		TypeName:        common.JSONString(value, "TypeName"),
		Title:           common.JSONString(value, "Title"),
		StartTime:       common.JSONString(value, "StartTime"),
		EndTime:         common.JSONString(value, "EndTime"),
		ChargeUserNo:    common.JSONString(value, "ChargeUserNo"),
		ChargeUserName:  common.JSONString(value, "ChargeUserName"),
		ClubID:          common.JSONString(value, "ClubID"),
		ClubName:        common.JSONString(value, "ClubName"),
		MemberNum:       common.JSONI64(value, "MemberNum"),
		AddTime:         common.JSONString(value, "AddTime"),
		PeopleNum:       common.JSONI64(value, "PeopleNum"),
		PeopleNumMin:    optI64(value, "PeopleNumMin"),
		IsJoined:        optBoolIfPresent(value, "IsJson"),
		IsClosed:        optBoolIfPresent(value, "IsClose"),
		SignUpStartTime: common.JSONString(value, "SignUpStartTime"),
		SignUpEndTime:   common.JSONString(value, "SignUpEndTime"),
	}
}

func parseSignItem(value json.RawMessage) SignItem {
	return SignItem{
		ID:        common.JSONString(value, "ID"),
		ItemType:  common.JSONI64(value, "Type"),
		TypeName:  common.JSONString(value, "TypeName"),
		StartTime: common.JSONString(value, "StartTime"),
		EndTime:   common.JSONString(value, "EndTime"),
		IsSign:    common.JSONBool(value, "IsSign"),
		SignTime:  common.JSONOptString(value, "SignTime"),
	}
}

func parseActivityDetail(value json.RawMessage) *ActivityDetail {
	detail := &ActivityDetail{
		ID:              common.JSONString(value, "ID"),
		Title:           common.JSONString(value, "Title"),
		StartTime:       common.JSONString(value, "StartTime"),
		EndTime:         common.JSONString(value, "EndTime"),
		ChargeUserName:  common.JSONString(value, "ChargeUserName"),
		ClubName:        common.JSONString(value, "ClubName"),
		MemberNum:       common.JSONI64(value, "MemberNum"),
		PeopleNum:       common.JSONI64(value, "PeopleNum"),
		SignUpStartTime: common.JSONOptString(value, "SignUpStartTime"),
		SignUpEndTime:   common.JSONOptString(value, "SignUpEndTime"),
	}
	for _, field := range common.ArrayItems(common.RawField(value, "formData")) {
		detail.FormData = append(detail.FormData, FormField{
			ID:        common.JSONString(field, "ID"),
			Name:      common.JSONString(field, "Name"),
			IsMust:    common.JSONBool(field, "IsMust"),
			FieldType: common.JSONI64(field, "FieldType"),
			Value:     common.JSONString(field, "Value"),
		})
	}
	for _, flow := range common.ArrayItems(common.RawField(value, "flowData")) {
		detail.FlowData = append(detail.FlowData, FlowData{
			NodeName: common.JSONString(flow, "FlowTypeName"),
			UserName: common.JSONString(flow, "ExamUserName"),
			IsAdopt:  optBool(flow, "IsAdopt"),
			Time:     common.JSONString(flow, "ExamTime"),
		})
	}
	for _, teacher := range common.ArrayItems(common.RawField(value, "teacherList")) {
		detail.TeacherList = append(detail.TeacherList, Teacher{
			Name:   common.JSONString(teacher, "UserName"),
			UserNo: common.JSONString(teacher, "UserNo"),
		})
	}
	for _, sign := range common.ArrayItems(common.RawField(value, "SignList")) {
		detail.SignList = append(detail.SignList, parseSignItem(sign))
	}
	return detail
}

func optI64(value json.RawMessage, key string) *int64 {
	raw := common.RawField(value, key)
	if raw == nil || string(raw) == "null" {
		return nil
	}
	v := common.JSONI64(value, key)
	return &v
}

func optBool(value json.RawMessage, key string) *bool {
	raw := common.RawField(value, key)
	if raw == nil || string(raw) == "null" {
		return nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return &b
	}
	return nil
}

func optBoolIfPresent(value json.RawMessage, key string) *bool {
	if common.RawField(value, key) == nil {
		return nil
	}
	v := common.JSONBool(value, key)
	return &v
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
