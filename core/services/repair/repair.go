// Package repair ports the repair (零星维修) service operations.
//
// Source: ios/loveaceios/Service/RepairService.swift and
// android/.../data/service/RepairService.kt (Android also implements image
// upload). The desktop client has no repair service; the union of iOS+Android
// is implemented here.
package repair

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

const basePath = "/index.php/index"

// Service gives access to repair operations for one session.
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// Order is one repair order.
type Order struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	OrderNumber string `json:"order_number"`
	WorkHours   string `json:"work_hours"`
	Reporter    string `json:"reporter"`
	Location    string `json:"location"`
	CreateTime  string `json:"create_time"`
	Status      int64  `json:"status"`
	StatusText  string `json:"status_text"`
}

// IsPending mirrors the Rust helper.
func (o Order) IsPending() bool { return o.Status == 0 || o.Status == 1 }

// IsCompleted mirrors the Rust helper.
func (o Order) IsCompleted() bool { return o.Status == 2 || o.Status == 3 }

// OrderSummary groups orders into pending/completed.
type OrderSummary struct {
	Pending   []Order `json:"pending"`
	Completed []Order `json:"completed"`
}

// TotalCount returns pending+completed.
func (s OrderSummary) TotalCount() int { return len(s.Pending) + len(s.Completed) }

// Progress is one progress stage.
type Progress struct {
	Stage       string `json:"stage"`
	Time        string `json:"time"`
	Description string `json:"description"`
}

// Settlement is one settlement row.
type Settlement struct {
	ServiceName string `json:"service_name"`
	Material    string `json:"material"`
	WorkPoints  string `json:"work_points"`
}

// OrderDetail is the repair order detail.
type OrderDetail struct {
	TaskID        string       `json:"task_id"`
	FaultArea     string       `json:"fault_area"`
	RepairProject string       `json:"repair_project"`
	Phone         string       `json:"phone"`
	FaultAddress  string       `json:"fault_address"`
	Description   string       `json:"description"`
	Progress      []Progress   `json:"progress"`
	Settlements   []Settlement `json:"settlements"`
}

// Item is a form list item.
type Item struct {
	ItemID string `json:"item_id"`
	Name   string `json:"name"`
}

// Group is a named group of items.
type Group struct {
	GroupName string `json:"group_name"`
	Items     []Item `json:"items"`
}

// FormData carries areas and projects.
type FormData struct {
	Areas    []Group `json:"areas"`
	Projects []Group `json:"projects"`
}

// SubmitRequest is a repair submission.
type SubmitRequest struct {
	AreaID      string  `json:"area_id"`
	AreaName    string  `json:"area_name"`
	ProjectID   string  `json:"project_id"`
	ProjectName string  `json:"project_name"`
	Phone       string  `json:"phone"`
	Address     string  `json:"address"`
	Description string  `json:"description"`
	PicURLs     *string `json:"pic_urls"`
}

// ToFormData renders the wire form fields.
func (r SubmitRequest) ToFormData() [][2]string {
	pic := ""
	if r.PicURLs != nil {
		pic = *r.PicURLs
	}
	return [][2]string{
		{"areaid", r.AreaID},
		{"areaname", r.AreaName},
		{"projectid", r.ProjectID},
		{"projectname", r.ProjectName},
		{"telphone", r.Phone},
		{"address", r.Address},
		{"worddesc", r.Description},
		{"pic", pic},
		{"voiceid", ""},
		{"taskType", "1"},
	}
}

func (sv *Service) url(path string) string {
	return config.JoinURL(sv.s.Config().RepairBase, path)
}

// EnsureSession warms the repair PHP session.
func (sv *Service) EnsureSession(caller context.Context) error {
	_, err := sv.s.SendRead(caller, transport.NewRequest(transport.Get, sv.url(basePath+"/user")))
	return err
}

// Orders returns all repair orders grouped into pending/completed.
func (sv *Service) Orders(caller context.Context) (*OrderSummary, error) {
	if err := sv.EnsureSession(caller); err != nil {
		return nil, err
	}
	var pending, completed []Order
	for status := int64(0); status <= 3; status++ {
		orders, err := sv.OrderList(caller, status)
		if err != nil {
			return nil, err
		}
		if status <= 1 {
			pending = append(pending, orders...)
		} else {
			completed = append(completed, orders...)
		}
	}
	return &OrderSummary{Pending: pending, Completed: completed}, nil
}

// OrderList returns orders for a single status code.
func (sv *Service) OrderList(caller context.Context, status int64) ([]Order, error) {
	response, err := sv.s.SendRead(caller,
		transport.NewRequest(transport.Get, sv.url(basePath+"/myorder/staue/"+strconv.FormatInt(status, 10))))
	if err != nil {
		return nil, err
	}
	return parseOrderList(response.BodyText(), status), nil
}

// OrderDetail returns the repair order detail.
func (sv *Service) OrderDetail(caller context.Context, taskID string) (*OrderDetail, error) {
	if err := sv.EnsureSession(caller); err != nil {
		return nil, err
	}
	response, err := sv.s.SendRead(caller,
		transport.NewRequest(transport.Get, sv.url(basePath+"/showrepair/taskid/"+taskID)))
	if err != nil {
		return nil, err
	}
	return parseOrderDetail(response.BodyText(), taskID), nil
}

// FormData returns area/project form data.
func (sv *Service) FormData(caller context.Context) (*FormData, error) {
	if err := sv.EnsureSession(caller); err != nil {
		return nil, err
	}
	areasResp, err := sv.s.SendRead(caller,
		transport.NewRequest(transport.Get, sv.url(basePath+"/arealist")))
	if err != nil {
		return nil, err
	}
	projectsResp, err := sv.s.SendRead(caller,
		transport.NewRequest(transport.Get, sv.url(basePath+"/projectlist")))
	if err != nil {
		return nil, err
	}
	return &FormData{
		Areas:    parseGroups(areasResp.BodyText()),
		Projects: parseGroups(projectsResp.BodyText()),
	}, nil
}

// UploadImage uploads a base64-encoded picture and returns the server URL (write).
func (sv *Service) UploadImage(caller context.Context, base64Data string) (string, error) {
	if err := sv.EnsureSession(caller); err != nil {
		return "", err
	}
	referer := sv.url(basePath + "/repair")
	response, err := sv.s.SendWrite(caller,
		transport.NewRequest(transport.Post, sv.url(basePath+"/uploadpic?sf_request_type=ajax")).
			Header("Content-Type", "application/x-www-form-urlencoded").
			Header("X-Requested-With", "XMLHttpRequest").
			Header("Referer", referer).
			WithBody([]byte(config.FormEncode([][2]string{{"data", base64Data}}))))
	if err != nil {
		return "", err
	}
	body := response.BodyText()
	m := uploadURLRe.FindStringSubmatch(body)
	if m == nil {
		return "", errs.NewParse("repair upload: url missing")
	}
	return m[1], nil
}

// Submit submits a repair order (write).
func (sv *Service) Submit(caller context.Context, request *SubmitRequest) (string, error) {
	if err := sv.EnsureSession(caller); err != nil {
		return "", err
	}
	referer := sv.url(basePath + "/repair")
	response, err := sv.s.SendWrite(caller,
		transport.NewRequest(transport.Post, sv.url(basePath+"/repair?sf_request_type=ajax")).
			Header("Content-Type", "application/x-www-form-urlencoded").
			Header("X-Requested-With", "XMLHttpRequest").
			Header("Referer", referer).
			WithBody([]byte(config.FormEncode(request.ToFormData()))))
	if err != nil {
		return "", err
	}
	body := response.BodyText()
	if strings.Contains(body, `"isok"`) && strings.Contains(body, "true") {
		return "报修提交成功", nil
	}
	if m := submitMsgRe.FindStringSubmatch(body); m != nil {
		message := m[1]
		if message == "" {
			message = "提交失败"
		}
		return "", errs.NewServer(-1, message)
	}
	return "", errs.NewServer(-1, "提交失败")
}

// --- parsers ---

var (
	uploadURLRe = regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`)
	submitMsgRe = regexp.MustCompile(`"msg"\s*:\s*"([^"]+)"`)
)

var statusTexts = []string{"待接单", "待完工", "待评价", "已完成"}

var (
	taskRe  = regexp.MustCompile(`taskid[/%2F]+(\d+)`)
	workRe  = regexp.MustCompile(`工时[：:]\s*(\S+)`)
	orderRe = regexp.MustCompile(`\d{12,}`)
	timeRe  = regexp.MustCompile(`\d{1,2}-\d{1,2}\s+\d{1,2}:\d{2}`)
	dateRe  = regexp.MustCompile(`\d{4}-\d{1,2}-\d{1,2}`)
)

func parseOrderList(page string, status int64) []Order {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil
	}
	statusText := statusTexts[clamp(int(status), 0, 3)]
	var orders []Order
	doc.Find("a.weui_media_box.weui_media_appmsg").Each(func(_ int, item *goquery.Selection) {
		href, _ := item.Attr("href")
		m := taskRe.FindStringSubmatch(href)
		if m == nil {
			return
		}
		taskID := m[1]
		title := strings.TrimSpace(item.Find("h4.weui_media_title").First().Text())
		var workHours, orderNumber, reporter, location, createTime string
		item.Find("p.weui_media_desc").Each(func(_ int, desc *goquery.Selection) {
			text := strings.TrimSpace(desc.Text())
			if strings.Contains(text, "工时") {
				if c := workRe.FindStringSubmatch(text); c != nil {
					workHours = c[1]
				}
				if om := orderRe.FindString(text); om != "" {
					orderNumber = om
				}
			}
			if strings.Contains(text, "校区") || strings.Contains(text, "公寓") || strings.Contains(text, "楼") {
				normalized := strings.Join(strings.Fields(text), " ")
				parts := strings.SplitN(normalized, " ", 2)
				reporter = parts[0]
				if len(parts) > 1 {
					location = parts[1]
				}
			}
			if timeRe.MatchString(text) {
				if tm := timeRe.FindString(text); tm != "" {
					createTime = tm
				}
			} else if createTime == "" {
				if dm := dateRe.FindString(text); dm != "" {
					createTime = dm
				}
			}
		})
		orders = append(orders, Order{
			TaskID:      taskID,
			Title:       title,
			OrderNumber: orderNumber,
			WorkHours:   workHours,
			Reporter:    reporter,
			Location:    location,
			CreateTime:  createTime,
			Status:      status,
			StatusText:  statusText,
		})
	})
	return orders
}

func parseOrderDetail(page, taskID string) *OrderDetail {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return &OrderDetail{TaskID: taskID}
	}
	detail := &OrderDetail{TaskID: taskID}
	doc.Find("input.weui_input").Each(func(_ int, input *goquery.Selection) {
		id, _ := input.Attr("id")
		value, _ := input.Attr("value")
		switch id {
		case "Area_Name":
			detail.FaultArea = value
		case "Project_Name":
			detail.RepairProject = value
		case "telphone":
			detail.Phone = value
		case "Address":
			detail.FaultAddress = value
		}
	})
	if sel := doc.Find("#WordDesc").First(); sel.Length() > 0 {
		detail.Description = strings.TrimSpace(sel.Text())
	}
	if container := doc.Find(".progressCon").First(); container.Length() > 0 {
		left := container.Find(".conLeft li")
		right := container.Find(".conRight li")
		left.Each(func(i int, stageEl *goquery.Selection) {
			stage := strings.TrimSpace(stageEl.Text())
			var t, description string
			if i < right.Length() {
				item := right.Eq(i)
				t = strings.TrimSpace(item.Find("span").First().Text())
				description = strings.TrimSpace(item.Find("p").First().Text())
			}
			detail.Progress = append(detail.Progress, Progress{Stage: stage, Time: t, Description: description})
		})
	}
	if table := doc.Find("table.wxcl").First(); table.Length() > 0 {
		table.Find("tbody tr").Each(func(_ int, row *goquery.Selection) {
			var cells []string
			row.Find("td").Each(func(_ int, cell *goquery.Selection) {
				cells = append(cells, strings.TrimSpace(cell.Text()))
			})
			if len(cells) >= 3 {
				detail.Settlements = append(detail.Settlements, Settlement{
					ServiceName: cells[0], Material: cells[1], WorkPoints: cells[2],
				})
			}
		})
	}
	return detail
}

func parseGroups(page string) []Group {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil
	}
	tabs := doc.Find("#segmentedControls a.mui-control-item")
	contents := doc.Find("#segmentedControlContents > div.mui-control-content")
	var groups []Group
	if tabs.Length() == contents.Length() && tabs.Length() > 0 {
		tabs.EachWithBreak(func(i int, tab *goquery.Selection) bool {
			groupName := strings.TrimSpace(tab.Text())
			items := collectItems(contents.Eq(i))
			if len(items) > 0 {
				groups = append(groups, Group{GroupName: groupName, Items: items})
			}
			return true
		})
	}
	if len(groups) == 0 {
		contents.Each(func(_ int, content *goquery.Selection) {
			items := collectItems(content)
			if len(items) > 0 {
				groups = append(groups, Group{GroupName: "", Items: items})
			}
		})
	}
	return groups
}

func collectItems(content *goquery.Selection) []Item {
	var items []Item
	content.Find("li.mui-table-view-cell").Each(func(_ int, li *goquery.Selection) {
		id, _ := li.Attr("data-val")
		name := strings.TrimSpace(li.Text())
		if id != "" && name != "" {
			items = append(items, Item{ItemID: id, Name: name})
		}
	})
	return items
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
