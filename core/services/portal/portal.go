// Package portal reads the school portal (门户, all.aufe.edu.cn through the
// VPN): dormitory and library access records, library loans, the
// campus-card consumption list and the enterprise-mail initial password.
//
// Every read is an e-cology cube search. getList opens a 32-character dataKey,
// counts gives the row total and datas pages the rows. Fields are matched by
// column title, never by index. The portal session is a CAS walk into the
// portal followed by one hpdata POST that yields hrmResourceID. That id is
// cached as a service ticket, and a VPN security intercept starts a cooldown.
package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const (
	// ticketService names the cached hrmResourceID among the service tickets.
	ticketService = "portal"

	hpid         = "38522"
	subCompanyID = "21"
	menuIDs      = "-665,-1001"
	windowWidth  = "390"
	referer      = config.PortalBase + "/spa/portal/static/index.html"

	listPath   = "/api/cube/search/getList"
	countsPath = "/api/ec/dev/table/counts"
	datasPath  = "/api/ec/dev/table/datas"
	hpdataPath = "/api/portal/homepage/hpdata"

	maxPages            = 50
	accessPageSize      = 10  // the portal answers ten access rows per page
	consumptionPageSize = 100 // and one hundred consumption rows
	interceptCooldown   = 180 * time.Second
	interceptMessage    = "学校 VPN 拦截了门户请求。请彻底退出 App 后重新打开再试"
)

// Cube ids and date fields are the portal's own (see LoveACE-work's
// docs/research/aufe-portal-*.md). Each date field takes a range code.
const (
	cubeMailPassword     = 82510
	cubeBorrowCurrent    = 84509
	cubeBorrowHistory    = 84510
	cubeDormAccess       = 37501
	dormDateField        = "178950"
	cubeLibraryAccess    = 71001
	libraryDateField     = "316587"
	cubeConsumption      = 74507
	consumptionDateField = "299018"
	consumptionMonth     = "3"
)

// Service reads the portal for one account session.
type Service struct{ s *session.Session }

// New binds a portal service to an account session.
func New(s *session.Session) *Service { return &Service{s: s} }

// AccessRecord is one dormitory or library entry. Direction is "entry" or
// "exit" for dormitory records and empty for library records.
type AccessRecord struct {
	ID        string `json:"id"`
	Time      string `json:"time"`
	Direction string `json:"direction,omitempty"`
	Detail    string `json:"detail"`
}

// AccessPage is one page of access records. Total counts the whole range.
type AccessPage struct {
	Records []AccessRecord `json:"records"`
	Total   int            `json:"total"`
}

// BorrowRecord is one library loan. ReturnedAt is nil while the book is out.
type BorrowRecord struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	BorrowedAt string  `json:"borrowed_at"`
	ReturnedAt *string `json:"returned_at"`
}

// BorrowingResult holds the current loans and the loan history.
type BorrowingResult struct {
	Current []BorrowRecord `json:"current"`
	History []BorrowRecord `json:"history"`
}

// ConsumptionRecord is one campus-card consumption row. The portal gives no
// balance after the transaction, no transaction type and no income/expense sign.
type ConsumptionRecord struct {
	ID     string `json:"id"`
	Date   string `json:"date"`
	Time   string `json:"time"`
	Area   string `json:"area"`
	Amount string `json:"amount"`
}

// ConsumptionPage is one page of this month's consumption rows.
type ConsumptionPage struct {
	Records []ConsumptionRecord `json:"records"`
	Total   int                 `json:"total"`
}

// MailInitialPassword is the enterprise-mail initial password. It is a
// plaintext school value: callers must not cache or log it.
type MailInitialPassword struct {
	LoginID  string `json:"login_id"`
	Password string `json:"password"`
}

// AccessRecords reads one page of dormitory or library access records.
// kind is "dormitory" or "library"; rangeName is one of today, yesterday,
// this_week (default), this_month or all.
func (sv *Service) AccessRecords(ctx context.Context, kind, rangeName string, page int) (*AccessPage, error) {
	if page < 1 {
		return nil, errs.NewInvalidArgument("page must be at least 1")
	}
	var cube int
	var dateField string
	switch kind {
	case "dormitory":
		cube, dateField = cubeDormAccess, dormDateField
	case "library":
		cube, dateField = cubeLibraryAccess, libraryDateField
	default:
		return nil, errs.NewInvalidArgument("kind must be dormitory or library")
	}
	if rangeName == "" {
		rangeName = "this_week"
	}
	code, err := accessCode(kind, rangeName)
	if err != nil {
		return nil, err
	}
	var out AccessPage
	err = sv.withSession(ctx, func(uid string) error {
		t, total, err := sv.readPage(ctx, uid, cube, [][2]string{{"canIsQuickSearch", "1"}, {dateField, code}}, page)
		if err != nil {
			return err
		}
		offset := (page - 1) * accessPageSize
		out = AccessPage{Total: total, Records: make([]AccessRecord, 0, len(t.rows))}
		for i, row := range t.rows {
			out.Records = append(out.Records, accessRecord(kind, t, row, offset+i))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Borrowing reads the current loans and the loan history.
func (sv *Service) Borrowing(ctx context.Context) (*BorrowingResult, error) {
	var out BorrowingResult
	err := sv.withSession(ctx, func(uid string) error {
		current, err := sv.readAll(ctx, uid, cubeBorrowCurrent, nil)
		if err != nil {
			return err
		}
		history, err := sv.readAll(ctx, uid, cubeBorrowHistory, nil)
		if err != nil {
			return err
		}
		out = BorrowingResult{Current: loans(current, false), History: loans(history, true)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ConsumptionRecords reads one page of this month's campus-card consumption.
func (sv *Service) ConsumptionRecords(ctx context.Context, page int) (*ConsumptionPage, error) {
	if page < 1 {
		return nil, errs.NewInvalidArgument("page must be at least 1")
	}
	var out ConsumptionPage
	err := sv.withSession(ctx, func(uid string) error {
		t, total, err := sv.readPage(ctx, uid, cubeConsumption,
			[][2]string{{"canIsQuickSearch", "1"}, {consumptionDateField, consumptionMonth}}, page)
		if err != nil {
			return err
		}
		offset := (page - 1) * consumptionPageSize
		out = ConsumptionPage{Total: total, Records: make([]ConsumptionRecord, 0, len(t.rows))}
		for i, row := range t.rows {
			out.Records = append(out.Records, ConsumptionRecord{
				ID:     t.recordID(row, offset+i),
				Date:   t.text(row, "交易日期"),
				Time:   t.text(row, "交易时间"),
				Area:   t.text(row, "交易地点"),
				Amount: t.text(row, "交易金额"),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// MailInitialPassword reads the enterprise-mail initial password. It returns
// nil when the portal has no row for the account.
func (sv *Service) MailInitialPassword(ctx context.Context) (*MailInitialPassword, error) {
	var out *MailInitialPassword
	err := sv.withSession(ctx, func(uid string) error {
		t, err := sv.readAll(ctx, uid, cubeMailPassword, nil)
		if err != nil {
			return err
		}
		out = nil
		if len(t.rows) == 0 {
			return nil
		}
		row := t.rows[0]
		password := t.text(row, "邮箱初始密码")
		if password == "" {
			return errs.NewParse("门户返回的数据缺少邮箱初始密码")
		}
		out = &MailInitialPassword{LoginID: t.text(row, "登录名"), Password: password}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// accessCode maps a range name to the portal's date code. The library's
// "this week" is the two-field form "2,,"; the dormitory uses plain "2".
func accessCode(kind, rangeName string) (string, error) {
	codes := map[string]string{"today": "1", "yesterday": "9", "this_week": "2", "this_month": "3", "all": "0"}
	code, ok := codes[rangeName]
	if !ok {
		return "", errs.NewInvalidArgument("unknown range: " + rangeName)
	}
	if kind == "library" && rangeName == "this_week" {
		return "2,,", nil
	}
	return code, nil
}

func accessRecord(kind string, t table, row map[string]any, index int) AccessRecord {
	rec := AccessRecord{ID: t.recordID(row, index), Time: t.text(row, "通过时间")}
	if kind == "dormitory" {
		rec.Direction = direction(t.text(row, "通过方向"))
		rec.Detail = t.text(row, "系统名称")
		return rec
	}
	var parts []string
	for _, part := range []string{t.text(row, "校区"), t.text(row, "日期")} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	rec.Detail = strings.Join(parts, " · ")
	return rec
}

// direction maps the dormitory's 通过方向 code: 0 is entry, 1 is exit.
func direction(raw string) string {
	switch raw {
	case "0":
		return "entry"
	case "1":
		return "exit"
	}
	return ""
}

// loans maps a borrowing cube. Current loans have no return time, and an
// empty 还书时间 means the book is still out.
func loans(t table, history bool) []BorrowRecord {
	out := make([]BorrowRecord, 0, len(t.rows))
	for i, row := range t.rows {
		rec := BorrowRecord{ID: t.recordID(row, i), Title: t.text(row, "书名")}
		if history {
			rec.BorrowedAt = t.text(row, "借书时间")
			if returned := t.text(row, "还书时间"); returned != "" {
				rec.ReturnedAt = &returned
			}
		} else {
			rec.BorrowedAt = t.text(row, "借阅时间")
		}
		out = append(out, rec)
	}
	return out
}

// withSession runs one read. A stale portal session (the portal answers with
// its login page) is re-established once and the read replayed.
func (sv *Service) withSession(ctx context.Context, read func(uid string) error) error {
	uid, err := sv.userID(ctx)
	if err != nil {
		return err
	}
	err = read(uid)
	if errs.CodeOf(err) != errs.AuthRequired {
		return err
	}
	sv.s.ResetServiceTicket(ticketService)
	uid, err = sv.userID(ctx)
	if err != nil {
		return err
	}
	return read(uid)
}

// userID returns the portal user id, running the handshake on first use.
// Concurrent first callers share one handshake through the service gate.
func (sv *Service) userID(ctx context.Context) (string, error) {
	if uid, ok := sv.s.CachedTicket(ticketService); ok {
		return uid, nil
	}
	gate := sv.s.ServiceGate(ticketService)
	gate.Lock()
	defer gate.Unlock()
	if uid, ok := sv.s.CachedTicket(ticketService); ok {
		return uid, nil
	}
	if time.Now().Before(gate.CooldownUntil()) {
		return "", errs.NewTemporarilyUnavailable(interceptMessage)
	}
	uid, err := sv.handshake(ctx, gate)
	if err != nil {
		return "", err
	}
	sv.s.StoreTicket(ticketService, uid)
	return uid, nil
}

// handshake walks the portal's CAS entry, fetches the web-proxy config (its
// answer is unused) and posts hpdata. The gateway rewrites the CAS service
// redirect into the vpn2 namespace while the session is healthy; any other
// Location makes RedirectLocations treat the session as expired and recover.
func (sv *Service) handshake(ctx context.Context, gate *session.ServiceGate) (string, error) {
	if _, err := sv.s.RedirectLocations(ctx, config.PortalLoginService, 10); err != nil {
		return "", err
	}
	_, _ = sv.s.SendRead(ctx, transport.NewRequest(transport.Get, config.PortalBase+"/sf-webproxy/api/vpn-config").
		Header("X-Requested-With", "XMLHttpRequest").
		Header("Referer", referer))
	form := [][2]string{
		{"hpid", hpid},
		{"subCompanyId", subCompanyID},
		{"menuIds", menuIDs},
		{"menuPathIds", menuIDs},
		{"isSetting", "false"},
		{"isfromportal", "1"},
		{"isfromhp", "0"},
	}
	response, err := sv.s.SendRead(ctx, sv.apiRequest(transport.Post, hpdataPath, form))
	if err != nil {
		return "", err
	}
	if response.Status == 404 {
		if v, _ := response.Header("Errormsg"); v == "securityIntercept" {
			gate.SetCooldown(time.Now().Add(interceptCooldown))
			return "", errs.NewTemporarilyUnavailable(interceptMessage)
		}
	}
	body, err := decodeObject(response)
	if err != nil {
		return "", err
	}
	uid := ""
	if hp, ok := body["hpinfo"].(map[string]any); ok {
		uid = textOf(hp["userid"])
	}
	if uid == "" {
		return "", errs.NewAuthRequiredSimple()
	}
	return uid, nil
}

// apiRequest builds one portal API call. Every call carries the XHR and
// referer headers the portal checks, and the VPN session id.
func (sv *Service) apiRequest(method transport.Method, path string, form [][2]string) *transport.Request {
	twfid, _ := sv.s.TWFID()
	request := transport.NewRequest(method, config.PortalBase+path+"?sf_request_type=fetch").
		Header("X-Requested-With", "XMLHttpRequest").
		Header("Referer", referer).
		Header("sdp-app-session", twfid)
	if form != nil {
		request.Header("Content-Type", "application/x-www-form-urlencoded").WithBody([]byte(config.FormEncode(form)))
	}
	return request
}

// call posts one form to a portal API and decodes the JSON object it answers.
func (sv *Service) call(ctx context.Context, path string, form [][2]string) (map[string]any, error) {
	response, err := sv.s.SendRead(ctx, sv.apiRequest(transport.Post, path, form))
	if err != nil {
		return nil, err
	}
	return decodeObject(response)
}

// table is one cube's columns (title to dbField) and the rows read so far.
type table struct {
	columns map[string]string
	rows    []map[string]any
}

func (t table) text(row map[string]any, title string) string {
	field, ok := t.columns[title]
	if !ok {
		return ""
	}
	return textOf(row[field])
}

func (t table) recordID(row map[string]any, index int) string {
	if id := t.text(row, "数据ID"); id != "" {
		return id
	}
	return fmt.Sprintf("row-%d", index)
}

// open runs getList and counts for one cube, returning the dataKey and total.
func (sv *Service) open(ctx context.Context, uid string, cube int, extra [][2]string) (string, int, error) {
	form := [][2]string{
		{"customid", strconv.Itoa(cube)},
		{"hrmResourceID", uid},
		{"guid", "search"},
		{"isNewTableSearch", "1"},
		{"displayType", ""},
		{"isTempSearch", ""},
		{"windowWidth", windowWidth},
		{"isTreeType", ""},
	}
	form = append(form, extra...)
	listed, err := sv.call(ctx, listPath, form)
	if err != nil {
		return "", 0, err
	}
	dataKey := textOf(listed["datas"])
	if len(dataKey) != 32 {
		return "", 0, errs.NewParse("门户列表响应缺少数据键")
	}
	counted, err := sv.call(ctx, countsPath, [][2]string{{"dataKey", dataKey}})
	if err != nil {
		return "", 0, err
	}
	return dataKey, intOf(counted["count"]), nil
}

// load fetches one datas page: the column definitions and that page's rows.
func (sv *Service) load(ctx context.Context, dataKey string, page int) (table, error) {
	body, err := sv.call(ctx, datasPath, [][2]string{
		{"dataKey", dataKey},
		{"current", strconv.Itoa(page)},
		{"sortParams", "[]"},
	})
	if err != nil {
		return table{}, err
	}
	t := table{columns: map[string]string{}}
	for _, raw := range asList(body["columns"]) {
		if col, ok := raw.(map[string]any); ok {
			if title := textOf(col["title"]); title != "" {
				t.columns[title] = textOf(col["dbField"])
			}
		}
	}
	for _, raw := range asList(body["datas"]) {
		if row, ok := raw.(map[string]any); ok {
			t.rows = append(t.rows, row)
		}
	}
	return t, nil
}

// readPage loads one page of a cube and the cube's total row count.
func (sv *Service) readPage(ctx context.Context, uid string, cube int, extra [][2]string, page int) (table, int, error) {
	dataKey, total, err := sv.open(ctx, uid, cube, extra)
	if err != nil {
		return table{}, 0, err
	}
	if total <= 0 {
		return table{columns: map[string]string{}}, 0, nil
	}
	t, err := sv.load(ctx, dataKey, page)
	return t, total, err
}

// readAll loads every row of a cube, paging until the counted total is reached.
func (sv *Service) readAll(ctx context.Context, uid string, cube int, extra [][2]string) (table, error) {
	dataKey, total, err := sv.open(ctx, uid, cube, extra)
	if err != nil {
		return table{}, err
	}
	out := table{columns: map[string]string{}}
	for page := 1; len(out.rows) < total && page <= maxPages; page++ {
		t, err := sv.load(ctx, dataKey, page)
		if err != nil {
			return table{}, err
		}
		if page == 1 {
			out.columns = t.columns
		}
		if len(t.rows) == 0 {
			break
		}
		out.rows = append(out.rows, t.rows...)
	}
	return out, nil
}

// decodeObject parses a portal JSON object. An empty body means no data. A
// body that is not JSON but looks like the VPN login page means the session
// expired.
func decodeObject(response *transport.Response) (map[string]any, error) {
	text := response.BodyText()
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		if looksLikeLoginPage(text) {
			return nil, errs.NewAuthRequiredSimple()
		}
		return nil, errs.NewParse("门户响应格式异常")
	}
	return out, nil
}

func looksLikeLoginPage(text string) bool {
	head := strings.ToLower(text)
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(head, "<html") || strings.Contains(head, "login_auth.csp") || strings.Contains(head, "svpn_name")
}

// textOf renders a JSON scalar the way the portal shows a cell. Objects and
// arrays have no text.
func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

func intOf(v any) int {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return 0
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}
