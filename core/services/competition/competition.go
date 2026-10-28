// Package competition ports the competition (创新创业) service operations.
//
// Source: desktop/lib/services/competition/competition_service.dart;
// mirrored by iOS/CompetitionService.swift and Android/CompetitionService.kt.
// Implements the two-step ASP.NET __VIEWSTATE flow and pagination.
package competition

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const awardsPath = "/xsXmMain.aspx"

const defaultEventTarget = "ctl00$ContentPlaceHolder1$ContentPlaceHolder2$DataList1$ctl01$LinkButton1"

var viewstateNames = []string{
	"__VIEWSTATE",
	"__VIEWSTATEGENERATOR",
	"__EVENTVALIDATION",
	"__EVENTTARGET",
	"__EVENTARGUMENT",
	"__LASTFOCUS",
}

// Service gives access to competition operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// AwardProject is one award row.
type AwardProject struct {
	ProjectID          string  `json:"project_id"`
	ProjectName        string  `json:"project_name"`
	Level              string  `json:"level"`
	Grade              string  `json:"grade"`
	AwardDate          string  `json:"award_date"`
	ApplicantID        string  `json:"applicant_id"`
	ApplicantName      string  `json:"applicant_name"`
	Order              int64   `json:"order"`
	Credits            float64 `json:"credits"`
	Bonus              float64 `json:"bonus"`
	Status             string  `json:"status"`
	VerificationStatus string  `json:"verification_status"`
}

// CreditsSummary is the per-category credit summary.
type CreditsSummary struct {
	DisciplineCompetitionCredits   *float64 `json:"discipline_competition_credits"`
	ScientificResearchCredits      *float64 `json:"scientific_research_credits"`
	TransferableCompetitionCredits *float64 `json:"transferable_competition_credits"`
	InnovationPracticeCredits      *float64 `json:"innovation_practice_credits"`
	AbilityCertificationCredits    *float64 `json:"ability_certification_credits"`
	OtherProjectCredits            *float64 `json:"other_project_credits"`
}

// TotalCredits sums all categories (nil treated as 0).
func (c CreditsSummary) TotalCredits() float64 {
	total := 0.0
	for _, v := range []*float64{
		c.DisciplineCompetitionCredits,
		c.ScientificResearchCredits,
		c.TransferableCompetitionCredits,
		c.InnovationPracticeCredits,
		c.AbilityCertificationCredits,
		c.OtherProjectCredits,
	} {
		if v != nil {
			total += *v
		}
	}
	return total
}

// FullResponse is the aggregated competition info.
type FullResponse struct {
	StudentID        string          `json:"student_id"`
	TotalAwardsCount int64           `json:"total_awards_count"`
	Awards           []AwardProject  `json:"awards"`
	CreditsSummary   *CreditsSummary `json:"credits_summary"`
}

func (sv *Service) url() string {
	return config.JoinURL(sv.s.Config().CompetitionBase, awardsPath)
}

// Info fetches award projects and credit summary.
func (sv *Service) Info(caller context.Context) (*FullResponse, error) {
	target := sv.url()
	indexResp, err := sv.s.SendRead(caller, transport.NewRequest(transport.Get, target))
	if err != nil {
		return nil, err
	}
	index := indexResp.BodyText()
	if err := checkCompetitionSession(index); err != nil {
		return nil, err
	}

	form := extractCompetitionForm(index)
	allEmpty := true
	for _, kv := range form {
		if kv[1] != "" {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		return nil, errs.NewParse("competition: __VIEWSTATE missing")
	}

	first, err := sv.post(caller, target, form)
	if err != nil {
		return nil, err
	}
	result, err := parseCompetitionPage(first)
	if err != nil {
		return nil, err
	}
	currentHTML := first
	totalPages := parseTotalPages(currentHTML)
	for page := int64(2); page <= totalPages && page <= 100; page++ {
		nextForm := extractNextPageForm(currentHTML, page)
		viewstateEmpty := true
		for _, kv := range nextForm {
			if kv[0] == "__VIEWSTATE" {
				viewstateEmpty = kv[1] == ""
				break
			}
		}
		if viewstateEmpty {
			break
		}
		nextHTML, err := sv.post(caller, target, nextForm)
		if err != nil {
			return nil, err
		}
		if nextHTML == "" {
			break
		}
		pageResult, err := parseCompetitionPage(nextHTML)
		if err != nil {
			return nil, err
		}
		result.Awards = append(result.Awards, pageResult.Awards...)
		currentHTML = nextHTML
	}
	result.TotalAwardsCount = int64(len(result.Awards))
	return result, nil
}

func (sv *Service) post(caller context.Context, target string, form [][2]string) (string, error) {
	request := transport.NewRequest(transport.Post, target).
		Header("Content-Type", "application/x-www-form-urlencoded").
		WithBody([]byte(config.FormEncode(form)))
	response, err := sv.s.SendRead(caller, request)
	if err != nil {
		return "", err
	}
	return response.BodyText(), nil
}

func checkCompetitionSession(page string) error {
	if strings.Contains(page, "var g_lines = []") ||
		strings.Contains(page, "selectline_timeout") ||
		(strings.Contains(page, "Your browser does not support JavaScript") &&
			strings.Contains(page, "<script>") &&
			len(page) < 10_000) {
		return errs.NewAuthRequiredSimple()
	}
	if strings.Contains(page, "用户登录") ||
		(strings.Contains(page, "login") && strings.Contains(page, "password")) {
		return errs.NewAuthRequiredSimple()
	}
	return nil
}

var (
	hiddenTagRe = regexp.MustCompile(`(?is)<input[^>]*name=["']` + "PAGE_NAME" + `["'][^>]*>`)
	hiddenValRe = regexp.MustCompile(`(?is)\bvalue=["']([^"']*)["']`)
)

func hiddenInput(page, name string) string {
	tagRe, err := regexp.Compile(`(?is)<input[^>]*name=["']` + regexp.QuoteMeta(name) + `["'][^>]*>`)
	if err != nil {
		return ""
	}
	tag := tagRe.FindString(page)
	if tag == "" {
		return ""
	}
	if m := hiddenValRe.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

func extractCompetitionForm(page string) [][2]string {
	form := make([][2]string, 0, len(viewstateNames)+4)
	for _, name := range viewstateNames {
		form = append(form, [2]string{name, hiddenInput(page, name)})
	}
	// Force the "已申报奖项" tab when the page left __EVENTTARGET blank.
	for i := range form {
		if form[i][0] == "__EVENTTARGET" && form[i][1] == "" {
			form[i][1] = defaultEventTarget
		}
	}
	form = append(form,
		[2]string{"ctl00$ContentPlaceHolder1$ContentPlaceHolder2$ddlSslb", "%"},
		[2]string{"ctl00$ContentPlaceHolder1$ContentPlaceHolder2$txtSsmc", ""},
		[2]string{"ctl00$ContentPlaceHolder1$ContentPlaceHolder2$gvSb$ctl28$txtNewPageIndex", "1"},
	)
	return form
}

var postbackRe = regexp.MustCompile(`__doPostBack\('([^']+)'`)
var pageIndexInputRe = regexp.MustCompile(`(?is)<input[^>]*id=["'][^"']*txtNewPageIndex["'][^>]*>`)
var inputNameRe = regexp.MustCompile(`(?is)\bname=["']([^"']+)["']`)

func extractNextPageForm(page string, targetPage int64) [][2]string {
	var eventTarget string
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return extractCompetitionForm(page)
	}
	for _, selector := range []string{`a[id*="LinkButtonNextPage"]`, `a[id*="btnGo"]`} {
		link := doc.Find(selector).First()
		if link.Length() == 0 {
			continue
		}
		href, _ := link.Attr("href")
		if m := postbackRe.FindStringSubmatch(href); m != nil {
			eventTarget = m[1]
			break
		}
	}
	form := extractCompetitionForm(page)
	for i := range form {
		if form[i][0] == "__EVENTTARGET" {
			form[i][1] = eventTarget
		}
	}
	// Honour a page-index input if present.
	if tag := pageIndexInputRe.FindString(page); tag != "" {
		if m := inputNameRe.FindStringSubmatch(tag); m != nil {
			form = append(form, [2]string{m[1], strconv.FormatInt(targetPage, 10)})
		}
	}
	return form
}

var pageCountTextRe = regexp.MustCompile(`[^0-9]`)

func parseTotalPages(page string) int64 {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return 1
	}
	text := strings.TrimSpace(doc.Find(`span[id*="gvHj_LabelPageCount"]`).First().Text())
	if n, err := strconv.ParseInt(text, 10, 64); err == nil && n >= 1 {
		return n
	}
	return 1
}

func parseCompetitionPage(page string) (*FullResponse, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, errs.NewParse("competition page parse failed: " + err.Error())
	}
	var awards []AwardProject
	doc.Find(`table[id*="gvHj"]`).Each(func(_ int, table *goquery.Selection) {
		table.Find("tr").Each(func(_ int, row *goquery.Selection) {
			var cells []string
			row.Find("td").Each(func(_ int, cell *goquery.Selection) {
				cells = append(cells, strings.TrimSpace(cell.Text()))
			})
			if len(cells) < 12 {
				return
			}
			projectID := cells[0]
			if _, err := strconv.ParseInt(projectID, 10, 64); err != nil {
				return
			}
			if strings.Contains(projectID, "当前第") {
				return
			}
			order, _ := strconv.ParseInt(cells[7], 10, 64)
			credits, _ := strconv.ParseFloat(cells[8], 64)
			bonus, _ := strconv.ParseFloat(cells[9], 64)
			awards = append(awards, AwardProject{
				ProjectID:          projectID,
				ProjectName:        cells[1],
				Level:              cells[2],
				Grade:              cells[3],
				AwardDate:          cells[4],
				ApplicantID:        cells[5],
				ApplicantName:      cells[6],
				Order:              order,
				Credits:            credits,
				Bonus:              bonus,
				Status:             cells[10],
				VerificationStatus: cells[11],
			})
		})
	})

	total := int64(len(awards))
	studentID := parseStudentID(doc)
	credits := parseCreditsSummary(doc)
	return &FullResponse{
		StudentID:        studentID,
		TotalAwardsCount: total,
		Awards:           awards,
		CreditsSummary:   credits,
	}, nil
}

func parseStudentID(doc *goquery.Document) string {
	selectors := []string{"span#ContentPlaceHolder1_lblXM", `span[id*="lblXM"]`, `span[id*="lblXh"]`}
	for _, selector := range selectors {
		text := strings.TrimSpace(doc.Find(selector).First().Text())
		if text == "" {
			continue
		}
		if idx := strings.Index(text, "："); idx >= 0 {
			return strings.TrimSpace(text[idx+len("："):])
		}
		return text
	}
	return ""
}

func parseCreditsSummary(doc *goquery.Document) *CreditsSummary {
	credit := func(id string) *float64 {
		text := strings.TrimSpace(doc.Find(`span[id="` + id + `"]`).First().Text())
		if text == "" || text == "无" {
			return nil
		}
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil
		}
		return &v
	}
	return &CreditsSummary{
		DisciplineCompetitionCredits:   credit("ContentPlaceHolder1_ContentPlaceHolder2_lblXkjsxf"),
		ScientificResearchCredits:      credit("ContentPlaceHolder1_ContentPlaceHolder2_lblKyxf"),
		TransferableCompetitionCredits: credit("ContentPlaceHolder1_ContentPlaceHolder2_lblKzjslxf"),
		InnovationPracticeCredits:      credit("ContentPlaceHolder1_ContentPlaceHolder2_lblCxcyxf"),
		AbilityCertificationCredits:    credit("ContentPlaceHolder1_ContentPlaceHolder2_lblNlzgxf"),
		OtherProjectCredits:            credit("ContentPlaceHolder1_ContentPlaceHolder2_lblQtxf"),
	}
}
