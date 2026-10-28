// Package aac ports the AAC ("爱安财" second-classroom) service operations.
//
// Source: desktop/lib/services/aac/aac_service.dart + aac_ticket_manager.dart;
// mirrored by iOS/AACService.swift and Android AACService.kt. The CAS ticket
// is minted by following the login service until the register?ticket=
// callback, then sent as ticket + sdp-app-session.
package aac

import (
	"context"
	"encoding/json"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

// Service gives access to AAC operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// AacCreditInfo is the AAC total-credit summary.
type AacCreditInfo struct {
	TotalScore      float64 `json:"total_score"`
	IsTypeAdopt     bool    `json:"is_type_adopt"`
	TypeAdoptResult string  `json:"type_adopt_result"`
}

// AacCreditItem is a single credit item.
type AacCreditItem struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	TypeName string  `json:"type_name"`
	UserNo   string  `json:"user_no"`
	Score    float64 `json:"score"`
	AddTime  string  `json:"add_time"`
}

// AacCreditCategory is a credit category with children.
type AacCreditCategory struct {
	ID         string          `json:"id"`
	ShowNum    int64           `json:"show_num"`
	TypeName   string          `json:"type_name"`
	TotalScore float64         `json:"total_score"`
	Children   []AacCreditItem `json:"children"`
}

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().AACAPIBase, path)
}

// Ticket resolves (and caches) an AAC CAS ticket, or fetches a fresh one.
func (sv *Service) Ticket(caller context.Context, forceRefresh bool) (string, error) {
	if !forceRefresh {
		if cached, ok := sv.s.CachedTicket("aac"); ok {
			return cached, nil
		}
	}
	locations, err := sv.s.RedirectLocations(caller, config.AACLoginService, 16)
	if err != nil {
		return "", err
	}
	for _, location := range locations {
		if contains(location, "register?ticket=") {
			if ticket, ok := common.ExtractTicketFromURL(location); ok {
				sv.s.StoreTicket("aac", ticket)
				return ticket, nil
			}
		}
	}
	return "", errs.NewParse("AAC ticket not found in redirect chain")
}

// ResetTicket resets the cached AAC ticket.
func (sv *Service) ResetTicket() { sv.s.ResetServiceTicket("aac") }

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

// CreditInfo returns the total credit score (/User/Center/DoGetScoreInfo).
func (sv *Service) CreditInfo(caller context.Context) (*AacCreditInfo, error) {
	response, err := sv.post(caller, "/User/Center/DoGetScoreInfo?sf_request_type=ajax", nil, session.Read)
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
		return nil, errs.NewServer(code, "AAC credit info failed")
	}
	return &AacCreditInfo{
		TotalScore:      common.JSONF64(data, "TotalScore"),
		IsTypeAdopt:     rawBool(data, "IsTypeAdopt"),
		TypeAdoptResult: common.JSONString(data, "TypeAdoptResult"),
	}, nil
}

// CreditList returns the credit detail list (/User/Center/DoGetScoreList).
func (sv *Service) CreditList(caller context.Context) ([]AacCreditCategory, error) {
	response, err := sv.post(caller, "/User/Center/DoGetScoreList?sf_request_type=ajax",
		[][2]string{{"pageIndex", "1"}, {"pageSize", "100"}}, session.Read)
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
		return nil, errs.NewServer(code, "AAC credit list failed")
	}
	items := common.ArrayItems(data)
	out := make([]AacCreditCategory, 0, len(items))
	for _, item := range items {
		out = append(out, parseCreditCategory(item))
	}
	return out, nil
}

func parseCreditCategory(value json.RawMessage) AacCreditCategory {
	var children []AacCreditItem
	for _, child := range common.ArrayItems(common.RawField(value, "children")) {
		children = append(children, AacCreditItem{
			ID:       common.JSONString(child, "ID"),
			Title:    common.JSONString(child, "Title"),
			TypeName: common.JSONString(child, "TypeName"),
			UserNo:   common.JSONString(child, "UserNo"),
			Score:    common.JSONF64(child, "Score"),
			AddTime:  common.JSONString(child, "AddTime"),
		})
	}
	if children == nil {
		children = []AacCreditItem{}
	}
	return AacCreditCategory{
		ID:         common.JSONString(value, "ID"),
		ShowNum:    common.JSONI64(value, "ShowNum"),
		TypeName:   common.JSONString(value, "TypeName"),
		TotalScore: common.JSONF64(value, "TotalScore"),
		Children:   children,
	}
}

func rawBool(value json.RawMessage, key string) bool {
	raw := common.RawField(value, key)
	if raw == nil {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
