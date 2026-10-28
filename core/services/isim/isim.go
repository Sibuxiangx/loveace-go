// Package isim ports the ISIM / dormitory electricity service operations.
//
// Source: desktop/lib/services/isim/isim_service.dart; mirrored by
// iOS/ISIMService.swift and Android/ISIMService.kt. A per-service JSESSIONID
// is established via /go?openid=..&sn=sn, then passed explicitly (the source
// never forwards the session's own service cookies to ISIM).
package isim

import (
	"context"
	"encoding/json"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/services/common"
)

// Service gives access to ISIM operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// Balance is the electricity balance.
type Balance struct {
	RemainingPurchased float64 `json:"remaining_purchased"`
	RemainingSubsidy   float64 `json:"remaining_subsidy"`
}

// Total sums purchased + subsidy.
func (b Balance) Total() float64 { return b.RemainingPurchased + b.RemainingSubsidy }

// UsageRecord is one electricity usage record.
type UsageRecord struct {
	RecordTime  string  `json:"record_time"`
	UsageAmount float64 `json:"usage_amount"`
	MeterName   string  `json:"meter_name"`
}

// PaymentRecord is one payment record.
type PaymentRecord struct {
	PaymentTime string  `json:"payment_time"`
	Amount      float64 `json:"amount"`
	PaymentType string  `json:"payment_type"`
}

// Info is the electricity info (usage + payments).
type Info struct {
	Balance      Balance         `json:"balance"`
	UsageRecords []UsageRecord   `json:"usage_records"`
	Payments     []PaymentRecord `json:"payments"`
}

// Option is a building/floor/room option.
type Option struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().ISIMBase, path)
}

func (sv *Service) host() string {
	if u, err := url.Parse(sv.s.Config().ISIMBase); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "hqkd-aufe-edu-cn.vpn2.aufe.edu.cn"
}

func (sv *Service) jsession() (string, bool) {
	return sv.s.Jar().GetForDomain("JSESSIONID", sv.host(), "/")
}

func (sv *Service) cookieHeader() string {
	twfid, _ := sv.s.TWFID()
	if j, ok := sv.jsession(); ok {
		return "JSESSIONID=" + j + "; TWFID=" + twfid
	}
	return "TWFID=" + twfid
}

// InitSession establishes the ISIM JSESSIONID.
func (sv *Service) InitSession(caller context.Context) error {
	target := sv.url("/go") + "?openid=" + config.URLEncode(sv.s.UserID()) + "&sn=sn"
	if _, err := sv.s.SendRead(caller, transport.NewRequest(transport.Get, target)); err != nil {
		return err
	}
	if _, ok := sv.jsession(); !ok {
		return errs.NewParse("ISIM JSESSIONID not set")
	}
	return nil
}

func (sv *Service) getWithCookie(caller context.Context, path string) (*transport.Response, error) {
	if err := sv.InitSession(caller); err != nil {
		return nil, err
	}
	j, _ := sv.jsession()
	request := transport.NewRequest(transport.Get, sv.url(path)).
		Header("Cookie", sv.cookieHeader()).
		Header("Referer", sv.s.Config().ISIMBase+"/about;jsessionid="+j).
		Header("X-Requested-With", "XMLHttpRequest").
		Header("Accept", "application/json, text/javascript, */*; q=0.01")
	return sv.s.SendRead(caller, request)
}

// Buildings parses the /about page's picker arrays.
func (sv *Service) Buildings(caller context.Context) ([]Option, error) {
	response, err := sv.getWithCookie(caller, "/about")
	if err != nil {
		return nil, err
	}
	return parseBuildings(response.BodyText()), nil
}

// Floors returns floors for a building.
func (sv *Service) Floors(caller context.Context, buildingCode string) ([]Option, error) {
	body, err := sv.getWithCookie(caller, "/about/floors/"+buildingCode)
	if err != nil {
		return nil, err
	}
	return parseCodeNameLists(body.BodyText(), "floordm", "floorname")
}

// Rooms returns rooms for a floor.
func (sv *Service) Rooms(caller context.Context, floorCode string) ([]Option, error) {
	body, err := sv.getWithCookie(caller, "/about/rooms/"+floorCode)
	if err != nil {
		return nil, err
	}
	return parseCodeNameLists(body.BodyText(), "roomdm", "roomname")
}

// BindRoom binds a room to the current ISIM session (session-local setup).
func (sv *Service) BindRoom(caller context.Context, roomCode string, displayText *string) (bool, error) {
	if err := sv.InitSession(caller); err != nil {
		return false, err
	}
	display := roomCode
	if displayText != nil {
		display = *displayText
	}
	form := [][2]string{
		{"roomdm", roomCode},
		{"room", display},
		{"openid", sv.s.UserID()},
		{"sn", "sn"},
		{"mode", "u"},
	}
	request := transport.NewRequest(transport.Post, sv.url("/about/rebinding")).
		Header("Content-Type", "application/x-www-form-urlencoded").
		Header("Cookie", sv.cookieHeader()).
		WithBody([]byte(config.FormEncode(form)))
	response, err := sv.s.SendRead(caller, request)
	if err != nil {
		return false, err
	}
	return response.Status == 200, nil
}

// ElectricityInfo binds then fetches usage + payment pages.
func (sv *Service) ElectricityInfo(caller context.Context, roomCode string, displayText *string) (*Info, error) {
	ok, err := sv.BindRoom(caller, roomCode, displayText)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.NewOutcomeUnknown("ISIM room binding failed")
	}
	usage, err := sv.getWithCookie(caller, "/use/record")
	if err != nil {
		return nil, err
	}
	payments, err := sv.getWithCookie(caller, "/pay/record")
	if err != nil {
		return nil, err
	}
	return &Info{
		Balance:      parseBalance(usage.BodyText()),
		UsageRecords: parseUsageRecords(usage.BodyText()),
		Payments:     parsePaymentRecords(payments.BodyText()),
	}, nil
}

// --- parsers ---

var (
	valuesRe = regexp.MustCompile(`(?s)values:\s*\[(.*?)\]`)
	namesRe  = regexp.MustCompile(`(?s)displayValues:\s*\[(.*?)\]`)
)

func parseBuildings(page string) []Option {
	// The /about page embeds pickerBuilding arrays inside <script> blocks;
	// a lightweight scan is equivalent to the scraper-based Rust version.
	for _, script := range regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`).FindAllStringSubmatch(page, -1) {
		content := script[1]
		if !strings.Contains(content, "pickerBuilding") {
			continue
		}
		valuesCaps := valuesRe.FindStringSubmatch(content)
		namesCaps := namesRe.FindStringSubmatch(content)
		if valuesCaps == nil || namesCaps == nil {
			continue
		}
		values := splitJSArray(valuesCaps[1])
		names := splitJSArray(namesCaps[1])
		var options []Option
		for i, code := range values {
			if i >= len(names) {
				break
			}
			name := names[i]
			if code != "" && name != "请选择" && name != "" {
				options = append(options, Option{Code: code, Name: name})
			}
		}
		if len(options) > 0 {
			return options
		}
	}
	return nil
}

func splitJSArray(input string) []string {
	var out []string
	for _, item := range strings.Split(input, ",") {
		item = strings.TrimSpace(item)
		item = strings.Trim(item, `"`)
		item = strings.Trim(item, `'`)
		item = strings.TrimSpace(item)
		if item != "" && item != `""` {
			out = append(out, item)
		}
	}
	return out
}

func parseCodeNameLists(body, codeKey, nameKey string) ([]Option, error) {
	normalized := normalizeJSObject(body)
	var parsed []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(normalized), &parsed); err != nil {
		return nil, errs.NewParse("JSON parse failed: " + err.Error())
	}
	var first map[string]json.RawMessage
	if len(parsed) > 0 {
		first = parsed[0]
	}
	var codes, names []json.RawMessage
	if first != nil {
		if v, ok := first[codeKey]; ok {
			_ = json.Unmarshal(v, &codes)
		}
		if v, ok := first[nameKey]; ok {
			_ = json.Unmarshal(v, &names)
		}
	}
	var options []Option
	// Skip the first placeholder entry ("请选择") on both parallel arrays.
	for i := 1; i < len(codes) && i <= len(names); i++ {
		code := valueToString(codes[i])
		name := valueToString(names[i])
		if code != "" && name != "" && name != "请选择" {
			options = append(options, Option{Code: code, Name: name})
		}
	}
	return options, nil
}

var jsKeyRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)

func normalizeJSObject(body string) string {
	trimmed := strings.TrimSpace(body)
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	return jsKeyRe.ReplaceAllString(trimmed, `"$1":`)
}

func valueToString(value json.RawMessage) string {
	if value == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(value, &s); err == nil {
		return s
	}
	trimmed := strings.TrimSpace(string(value))
	trimmed = strings.TrimPrefix(trimmed, "\"")
	trimmed = strings.TrimSuffix(trimmed, "\"")
	return trimmed
}

var (
	itemContentSel = regexp.MustCompile(`(?s)<li[^>]*class="[^"]*item-content[^"]*"[^>]*>(.*?)</li>`)
	itemSel        = regexp.MustCompile(`(?s)<li[^>]*class="[^"]*item[^"]*"[^>]*>(.*?)</li>`)
	titleSel       = regexp.MustCompile(`(?s)<(?:[^>]*class="[^"]*(?:item-title|title|label)[^"]*"[^>]*)(.*?)</`)
	afterSel       = regexp.MustCompile(`(?s)<(?:[^>]*class="[^"]*(?:item-after|value|amount)[^"]*"[^>]*)(.*?)</`)
	numberRe       = regexp.MustCompile(`([\d.]+)`)
	balanceBodyRe  = regexp.MustCompile(`购电[：:]\s*([\d.]+)\s*度`)
	subsidyBodyRe  = regexp.MustCompile(`补助[：:]\s*([\d.]+)\s*度`)
)

// parseBalance extracts 剩余购电/剩余补助 from the usage page.
func parseBalance(page string) Balance {
	var purchased, subsidy float64
	blocks := itemContentSel.FindAllStringSubmatch(page, -1)
	if len(blocks) == 0 {
		blocks = itemSel.FindAllStringSubmatch(page, -1)
	}
	for _, block := range blocks {
		item := block[1]
		title := firstTagText(item, `class="[^"]*(?:item-title|title|label)[^"]*"`)
		valueText := firstTagText(item, `class="[^"]*(?:item-after|value|amount)[^"]*"`)
		amount := 0.0
		if m := numberRe.FindStringSubmatch(valueText); m != nil {
			amount, _ = strconv.ParseFloat(m[1], 64)
		}
		if strings.Contains(title, "剩余购电") || strings.Contains(title, "购电余额") {
			purchased = amount
		} else if strings.Contains(title, "剩余补助") || strings.Contains(title, "补助余额") {
			subsidy = amount
		}
	}
	if purchased == 0 && subsidy == 0 {
		body := common.StripTags(page)
		if m := balanceBodyRe.FindStringSubmatch(body); m != nil {
			purchased, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := subsidyBodyRe.FindStringSubmatch(body); m != nil {
			subsidy, _ = strconv.ParseFloat(m[1], 64)
		}
	}
	return Balance{RemainingPurchased: purchased, RemainingSubsidy: subsidy}
}

// firstTagText extracts the inner text of the first tag whose attributes
// match the given class-attribute pattern.
func firstTagText(fragment, classPattern string) string {
	re := regexp.MustCompile(`(?s)<[^>]*` + classPattern + `[^>]*>(.*?)</`)
	if m := re.FindStringSubmatch(fragment); m != nil {
		return strings.TrimSpace(common.StripTags(m[1]))
	}
	return ""
}

var (
	recordItemRe = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	usageRe      = regexp.MustCompile(`([\d.]+)度`)
	meterRe      = regexp.MustCompile(`电表:\s*(.+)`)
	amountRe     = regexp.MustCompile(`(-?[\d.]+)元`)
	payTypeRe    = regexp.MustCompile(`类型:\s*(.+)`)
)

func parseRecordItems(page string) [][3]string {
	var out [][3]string
	for _, item := range recordItemRe.FindAllStringSubmatch(page, -1) {
		body := item[1]
		title := firstClass(body, "item-title")
		after := firstClass(body, "item-after")
		subtitle := firstClass(body, "item-subtitle")
		if title == "" || after == "" || subtitle == "" {
			continue
		}
		out = append(out, [3]string{strings.TrimSpace(title), strings.TrimSpace(after), strings.TrimSpace(subtitle)})
	}
	return out
}

func firstClass(fragment, class string) string {
	re := regexp.MustCompile(`(?s)<[^>]*class="[^"]*` + class + `[^"]*"[^>]*>(.*?)</`)
	if m := re.FindStringSubmatch(fragment); m != nil {
		return common.StripTags(m[1])
	}
	return ""
}

func parseUsageRecords(page string) []UsageRecord {
	var records []UsageRecord
	for _, parts := range parseRecordItems(page) {
		title, after, subtitle := parts[0], parts[1], parts[2]
		if m := usageRe.FindStringSubmatch(after); m != nil {
			usage, _ := strconv.ParseFloat(m[1], 64)
			meter := subtitle
			if c := meterRe.FindStringSubmatch(subtitle); c != nil {
				meter = strings.TrimSpace(c[1])
			}
			records = append(records, UsageRecord{
				RecordTime:  title,
				UsageAmount: usage,
				MeterName:   meter,
			})
		}
	}
	return records
}

func parsePaymentRecords(page string) []PaymentRecord {
	var records []PaymentRecord
	for _, parts := range parseRecordItems(page) {
		title, after, subtitle := parts[0], parts[1], parts[2]
		if m := amountRe.FindStringSubmatch(after); m != nil {
			amount, _ := strconv.ParseFloat(m[1], 64)
			paymentType := subtitle
			if c := payTypeRe.FindStringSubmatch(subtitle); c != nil {
				paymentType = strings.TrimSpace(c[1])
			}
			records = append(records, PaymentRecord{
				PaymentTime: title,
				Amount:      amount,
				PaymentType: paymentType,
			})
		}
	}
	return records
}
