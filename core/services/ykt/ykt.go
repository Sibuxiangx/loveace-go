// Package ykt ports the YKT (campus card) service operations.
//
// Source: desktop/lib/services/ykt/*.dart, mirrored by
// ios/loveaceios/Service/YKTService.swift and android/.../data/service/
// YKTService.kt. Endpoints and HTML shapes are identical across clients.
package ykt

import (
	"context"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"regexp"
	"strconv"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

const (
	htmlAccept    = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
	slowTimeoutMS = 600_000
)

// Service gives access to campus-card operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// CardBalance is the campus-card balance.
type CardBalance struct {
	Balance     float64 `json:"balance"`
	BalanceText string  `json:"balance_text"`
}

// TransactionRecord is one campus-card transaction.
type TransactionRecord struct {
	AccountingTime  string   `json:"accounting_time"`
	TransactionTime string   `json:"transaction_time"`
	Expense         *float64 `json:"expense"`
	Income          *float64 `json:"income"`
	OperationType   string   `json:"operation_type"`
	Balance         float64  `json:"balance"`
	Area            string   `json:"area"`
	TerminalID      string   `json:"terminal_id"`
}

// IsExpense mirrors the Rust helper.
func (r TransactionRecord) IsExpense() bool { return r.Expense != nil && *r.Expense > 0.0 }

// IsIncome mirrors the Rust helper.
func (r TransactionRecord) IsIncome() bool { return r.Income != nil && *r.Income > 0.0 }

// Amount is positive for income, negative for expense.
func (r TransactionRecord) Amount() float64 {
	if r.IsIncome() {
		return *r.Income
	}
	return -(derefOr(r.Expense, 0))
}

// AmountText renders the signed CNY amount.
func (r TransactionRecord) AmountText() string {
	if r.IsIncome() {
		return "+" + format2(*r.Income) + "元"
	}
	if r.IsExpense() {
		return "-" + format2(*r.Expense) + "元"
	}
	return "0.00元"
}

// TransactionQueryResult is a transaction query page.
type TransactionQueryResult struct {
	Records   []TransactionRecord `json:"records"`
	StartDate string              `json:"start_date"`
	EndDate   string              `json:"end_date"`
}

// TotalExpense sums expense values.
func (r TransactionQueryResult) TotalExpense() float64 {
	var sum float64
	for _, rec := range r.Records {
		sum += derefOr(rec.Expense, 0)
	}
	return sum
}

// TotalIncome sums income values.
func (r TransactionQueryResult) TotalIncome() float64 {
	var sum float64
	for _, rec := range r.Records {
		sum += derefOr(rec.Income, 0)
	}
	return sum
}

// Count returns the record count.
func (r TransactionQueryResult) Count() int { return len(r.Records) }

// SelectOption is a generic value/name option.
type SelectOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// SelectOptionFromString parses "value,name".
func SelectOptionFromString(input string) *SelectOption {
	parts := strings.SplitN(input, ",", 2)
	if len(parts) != 2 {
		return nil
	}
	value := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if value == "" && name == "" {
		return nil
	}
	return &SelectOption{Value: value, Name: name}
}

// ParseSelectOptions parses a pipe-separated option list.
func ParseSelectOptions(response string) []SelectOption {
	var out []SelectOption
	for _, item := range strings.Split(response, "|") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if opt := SelectOptionFromString(item); opt != nil {
			out = append(out, *opt)
		}
	}
	return out
}

// StudentInfo is the electricity recharge page info.
type StudentInfo struct {
	StudentID     string  `json:"student_id"`
	Name          string  `json:"name"`
	AccountStatus string  `json:"account_status"`
	CardStatus    string  `json:"card_status"`
	Balance       float64 `json:"balance"`
	AccID         string  `json:"acc_id"`
}

// UtilityPaymentRequest is an electricity recharge request.
type UtilityPaymentRequest struct {
	RoomID        string `json:"room_id"`
	DormID        string `json:"dorm_id"`
	DormName      string `json:"dorm_name"`
	BuildName     string `json:"build_name"`
	FloorName     string `json:"floor_name"`
	RoomName      string `json:"room_name"`
	AccID         string `json:"acc_id"`
	Balances      string `json:"balances"`
	PayType       string `json:"pay_type"`
	ChoosePayType string `json:"choose_pay_type"`
	Money         int64  `json:"money"`
}

// DefaultUtilityPaymentRequest mirrors the Rust Default impl
// (payType=2, choosePayType=1).
func DefaultUtilityPaymentRequest() UtilityPaymentRequest {
	return UtilityPaymentRequest{PayType: "2", ChoosePayType: "1"}
}

// ToFormData renders the wire form fields.
func (r UtilityPaymentRequest) ToFormData() [][2]string {
	return [][2]string{
		{"roomId", r.RoomID},
		{"dormId", r.DormID},
		{"dormName", r.DormName},
		{"buildName", r.BuildName},
		{"floorName", r.FloorName},
		{"roomName", r.RoomName},
		{"accId", r.AccID},
		{"balances", r.Balances},
		{"payType", r.PayType},
		{"choosePayType", r.ChoosePayType},
		{"money", strconv.FormatInt(r.Money, 10)},
	}
}

// UtilityPaymentResult is the recharge result.
type UtilityPaymentResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ElectricPurchaseRecord is one electricity purchase record.
type ElectricPurchaseRecord struct {
	Name         string  `json:"name"`
	StudentID    string  `json:"student_id"`
	Area         string  `json:"area"`
	RoomInfo     string  `json:"room_info"`
	Amount       float64 `json:"amount"`
	PurchaseDate string  `json:"purchase_date"`
	Department   string  `json:"department"`
}

// ElectricPurchaseQueryResult is a purchase-history page.
type ElectricPurchaseQueryResult struct {
	StartDate string                   `json:"start_date"`
	EndDate   string                   `json:"end_date"`
	Records   []ElectricPurchaseRecord `json:"records"`
}

// Count returns the record count.
func (r ElectricPurchaseQueryResult) Count() int { return len(r.Records) }

// TotalAmount sums amounts.
func (r ElectricPurchaseQueryResult) TotalAmount() float64 {
	var sum float64
	for _, rec := range r.Records {
		sum += rec.Amount
	}
	return sum
}

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().YKTBase, path)
}

func (sv *Service) get(caller context.Context, path string) (*transport.Response, error) {
	request := transport.NewRequest(transport.Get, sv.url(path)).
		Header("Accept", htmlAccept).
		Header("Upgrade-Insecure-Requests", "1")
	return sv.s.SendRead(caller, request)
}

func (sv *Service) post(caller context.Context, path string, form [][2]string, timeout int64, kind session.Kind) (*transport.Response, error) {
	request := transport.NewRequest(transport.Post, sv.url(path)).
		Header("Content-Type", "application/x-www-form-urlencoded").
		Header("Accept", htmlAccept).
		Header("Upgrade-Insecure-Requests", "1").
		WithBody([]byte(config.FormEncode(form)))
	request.TimeoutMS = timeout
	if kind == session.Read {
		return sv.s.SendRead(caller, request)
	}
	return sv.s.SendWrite(caller, request)
}

// InitSession establishes the campus-card CAS session (/casLogin.jsp).
func (sv *Service) InitSession(caller context.Context) error {
	_, err := sv.get(caller, "/casLogin.jsp")
	return err
}

// Balance queries the campus-card balance.
func (sv *Service) Balance(caller context.Context) (*CardBalance, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.get(caller, "/queryUserBalances.action")
	if err != nil {
		return nil, err
	}
	return parseCardBalance(response.BodyText())
}

// Transactions queries transactions in [startDate, endDate] (YYYY-MM-DD).
func (sv *Service) Transactions(caller context.Context, startDate, endDate string) (*TransactionQueryResult, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.post(caller, "/queryUserCostList.action",
		[][2]string{{"startDate", startDate}, {"endDate", endDate}},
		slowTimeoutMS, session.Read)
	if err != nil {
		return nil, err
	}
	return parseTransactions(response.BodyText(), startDate, endDate), nil
}

// RecentTransactions returns transactions over the last 7 days.
func (sv *Service) RecentTransactions(caller context.Context) (*TransactionQueryResult, error) {
	end := timeutil.TodayYMD()
	start := timeutil.YMDHoursAgo(7)
	return sv.Transactions(caller, start, end)
}

// MonthlyTransactions returns transactions over the last 30 days.
func (sv *Service) MonthlyTransactions(caller context.Context) (*TransactionQueryResult, error) {
	end := timeutil.TodayYMD()
	start := timeutil.YMDHoursAgo(30)
	return sv.Transactions(caller, start, end)
}

// UtilityPageInfo returns the electricity recharge page info.
func (sv *Service) UtilityPageInfo(caller context.Context) (*StudentInfo, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.get(caller, "/utilityUnBindUserPowerPayInit.action")
	if err != nil {
		return nil, err
	}
	return parseStudentInfo(response.BodyText())
}

// UtilityOptions returns electricity recharge options (dorm/building/floor/room).
func (sv *Service) UtilityOptions(caller context.Context, dormID, buildingID, floorID, dormName string) ([]SelectOption, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.post(caller, "/utilitBindXiaoQuData.action",
		[][2]string{
			{"dormId", dormID},
			{"buildingId", buildingID},
			{"floorId", floorID},
			{"dormName", dormName},
		},
		sv.s.Config().TimeoutMS, session.Read)
	if err != nil {
		return nil, err
	}
	return ParseSelectOptions(strings.TrimSpace(response.BodyText())), nil
}

// UtilityPay submits an electricity recharge (write, once).
func (sv *Service) UtilityPay(caller context.Context, request *UtilityPaymentRequest) (*UtilityPaymentResult, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.post(caller, "/utilityUnBindUserPowerPay.action", request.ToFormData(),
		sv.s.Config().TimeoutMS, session.Write)
	if err != nil {
		return nil, err
	}
	return parsePaymentResult(response.BodyText())
}

// PurchaseHistory returns purchase history between two dates (YYYY-MM-DD).
func (sv *Service) PurchaseHistory(caller context.Context, startDate, endDate string) (*ElectricPurchaseQueryResult, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.post(caller, "/utilityQueryRunningAccountInfo.action",
		[][2]string{
			{"startDate", startDate + " 00:00:00"},
			{"endDate", endDate + " 23:59:59"},
		},
		slowTimeoutMS, session.Read)
	if err != nil {
		return nil, err
	}
	return parsePurchaseHistory(response.BodyText(), startDate, endDate), nil
}

// RecentPurchaseHistory returns purchase history over the last 7 days.
func (sv *Service) RecentPurchaseHistory(caller context.Context) (*ElectricPurchaseQueryResult, error) {
	end := timeutil.TodayYMD()
	start := timeutil.YMDHoursAgo(7)
	return sv.PurchaseHistory(caller, start, end)
}

// --- parsers ---

var balancePatterns = []*regexp.Regexp{
	regexp.MustCompile(`校园卡余额[：:]\s*</label>\s*<label>\s*([\d.]+)\s*元`),
	regexp.MustCompile(`余额[：:]?\s*([\d.]+)\s*元`),
}

func parseCardBalance(page string) (*CardBalance, error) {
	for _, re := range balancePatterns {
		if m := re.FindStringSubmatch(page); m != nil {
			text := m[1]
			balance, _ := strconv.ParseFloat(text, 64)
			return &CardBalance{Balance: balance, BalanceText: text + "元"}, nil
		}
	}
	return nil, errs.NewParse("card balance not found")
}

var transactionRowRe = regexp.MustCompile(
	`(?s)<tr>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*</tr>`)

func parseTransactions(page, startDate, endDate string) *TransactionQueryResult {
	var records []TransactionRecord
	for _, caps := range transactionRowRe.FindAllStringSubmatch(page, -1) {
		get := func(index int) string { return common.CleanHTMLText(caps[index]) }
		records = append(records, TransactionRecord{
			AccountingTime:  get(1),
			TransactionTime: get(2),
			Expense:         common.ParseAmount(get(3)),
			Income:          common.ParseAmount(get(4)),
			OperationType:   get(5),
			Balance:         derefOr(common.ParseAmount(get(6)), 0),
			Area:            get(7),
			TerminalID:      get(8),
		})
	}
	return &TransactionQueryResult{Records: records, StartDate: startDate, EndDate: endDate}
}

var studentInfoPatterns = map[string]*regexp.Regexp{
	"student_id":     regexp.MustCompile(`编号</label>\s*<label>(\d+)</label>`),
	"name":           regexp.MustCompile(`姓名</label>\s*<label>([^<]+)</label>`),
	"account_status": regexp.MustCompile(`账户状态</label>\s*<label>([^<]+)</label>`),
	"card_status":    regexp.MustCompile(`卡状态</label>\s*<label>([^<]+)</label>`),
	"balance_text":   regexp.MustCompile(`校园余额</label>\s*<label>([\d.]+)</label>`),
	"acc_id":         regexp.MustCompile(`name="accId"\s+value\s*=\s*"(\d+)"`),
}

func parseStudentInfo(page string) (*StudentInfo, error) {
	extract := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(page); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	balanceText := extract(studentInfoPatterns["balance_text"])
	balance, _ := strconv.ParseFloat(balanceText, 64)
	return &StudentInfo{
		StudentID:     extract(studentInfoPatterns["student_id"]),
		Name:          extract(studentInfoPatterns["name"]),
		AccountStatus: extract(studentInfoPatterns["account_status"]),
		CardStatus:    extract(studentInfoPatterns["card_status"]),
		Balance:       balance,
		AccID:         extract(studentInfoPatterns["acc_id"]),
	}, nil
}

var messageValueRe = regexp.MustCompile(`id="message"[^>]*value\s*=\s*"([^"]*)"`)

func parsePaymentResult(page string) (*UtilityPaymentResult, error) {
	message := ""
	if m := messageValueRe.FindStringSubmatch(page); m != nil {
		message = m[1]
	}
	if strings.Contains(message, "成功") {
		return &UtilityPaymentResult{Success: true, Message: message}, nil
	}
	if message != "" {
		return &UtilityPaymentResult{Success: false, Message: message}, nil
	}
	if strings.Contains(page, "缴费成功") || strings.Contains(page, "充值成功") {
		return &UtilityPaymentResult{Success: true, Message: "充值成功"}, nil
	}
	return &UtilityPaymentResult{Success: false, Message: "未知结果"}, nil
}

var purchaseRowRe = regexp.MustCompile(
	`(?s)<tr>\s*<td>([^<]*)</td>\s*<td>([^<]*)</td>\s*<td>([^<]*)</td>\s*<td[^>]*>([^<]*)</td>\s*<td>([^<]*)</td>\s*<td[^>]*>([^<]*)</td>\s*<td>([^<]*)</td>\s*</tr>`)

func parsePurchaseHistory(page, startDate, endDate string) *ElectricPurchaseQueryResult {
	var records []ElectricPurchaseRecord
	for _, caps := range purchaseRowRe.FindAllStringSubmatch(page, -1) {
		get := func(index int) string { return strings.TrimSpace(caps[index]) }
		name := get(1)
		if name == "" || name == "姓名" {
			continue
		}
		amount, _ := strconv.ParseFloat(get(5), 64)
		records = append(records, ElectricPurchaseRecord{
			Name:         name,
			StudentID:    get(2),
			Area:         get(3),
			RoomInfo:     get(4),
			Amount:       amount,
			PurchaseDate: get(6),
			Department:   get(7),
		})
	}
	return &ElectricPurchaseQueryResult{StartDate: startDate, EndDate: endDate, Records: records}
}

func derefOr(v *float64, fallback float64) float64 {
	if v == nil {
		return fallback
	}
	return *v
}

func format2(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
