package repair

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const ordersHTML = `<!DOCTYPE html><html><body>
<a class="weui_media_box weui_media_appmsg" href="/index.php/index/showrepair/taskid/123">
<h4 class="weui_media_title">灯管坏了</h4>
<p class="weui_media_desc">工时：2.5 20260102080112345</p>
<p class="weui_media_desc">张三 东区X1公寓101</p>
<p class="weui_media_desc">01-02 08:01</p>
</a>
</body></html>`

const detailHTML = `<!DOCTYPE html><html><body>
<input class="weui_input" id="Area_Name" value="东区"/>
<input class="weui_input" id="Project_Name" value="灯具维修"/>
<input class="weui_input" id="telphone" value="13800000000"/>
<input class="weui_input" id="Address" value="X1-101"/>
<div id="WordDesc">灯管闪烁</div>
<div class="progressCon">
<div class="conLeft"><li>派单</li><li>完工</li></div>
<div class="conRight"><li><span>2026-01-02</span><p>已派单</p></li><li><span>2026-01-03</span><p>已完工</p></li></div>
</div>
<table class="wxcl"><tbody>
<tr><td>人工费</td><td>无</td><td>2.5</td></tr>
<tr><td>灯管</td><td>1根</td><td>0.5</td></tr>
</tbody></table>
</body></html>`

const formHTML = `<!DOCTYPE html><html><body>
<div id="segmentedControls">
<a class="mui-control-item">东区</a><a class="mui-control-item">西区</a>
</div>
<div id="segmentedControlContents">
<div class="mui-control-content"><li class="mui-table-view-cell" data-val="A1">教学楼</li></div>
<div class="mui-control-content"><li class="mui-table-view-cell" data-val="A2">图书馆</li></div>
</div>
</body></html>`

func repairTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		On("/index.php/index/user", transport.NewResponse(200, []byte("<html>ok</html>"))).
		On("/myorder/staue/0", transport.NewResponse(200, []byte(ordersHTML))).
		On("/myorder/staue/1", transport.NewResponse(200, []byte("<html>none</html>"))).
		On("/myorder/staue/2", transport.NewResponse(200, []byte("<html>none</html>"))).
		On("/myorder/staue/3", transport.NewResponse(200, []byte("<html>none</html>"))).
		On("/showrepair/taskid/", transport.NewResponse(200, []byte(detailHTML))).
		On("/arealist", transport.NewResponse(200, []byte(formHTML))).
		On("/projectlist", transport.NewResponse(200, []byte(formHTML)))
}

func TestRepairOrders(t *testing.T) {
	s := session.New("20000002", "gw", "school", repairTransport(t))
	sv := New(s)
	summary, err := sv.Orders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCount() != 1 || len(summary.Pending) != 1 || len(summary.Completed) != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	order := summary.Pending[0]
	if order.TaskID != "123" || order.Title != "灯管坏了" || order.StatusText != "待接单" {
		t.Fatalf("order = %+v", order)
	}
	if order.WorkHours != "2.5" || order.OrderNumber != "20260102080112345" {
		t.Fatalf("work/order = %q %q", order.WorkHours, order.OrderNumber)
	}
	if order.Reporter != "张三" || order.Location != "东区X1公寓101" {
		t.Fatalf("reporter/location = %q %q", order.Reporter, order.Location)
	}
	if order.CreateTime != "01-02 08:01" {
		t.Fatalf("create time = %q", order.CreateTime)
	}
	if !order.IsPending() || order.IsCompleted() {
		t.Fatal("status classification")
	}
}

func TestRepairOrderDetail(t *testing.T) {
	s := session.New("20000002", "gw", "school", repairTransport(t))
	sv := New(s)
	detail, err := sv.OrderDetail(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if detail.FaultArea != "东区" || detail.RepairProject != "灯具维修" || detail.Phone != "13800000000" || detail.FaultAddress != "X1-101" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Description != "灯管闪烁" {
		t.Fatalf("description = %q", detail.Description)
	}
	if len(detail.Progress) != 2 || detail.Progress[0].Stage != "派单" || detail.Progress[1].Time != "2026-01-03" {
		t.Fatalf("progress = %+v", detail.Progress)
	}
	if len(detail.Settlements) != 2 || detail.Settlements[0].ServiceName != "人工费" {
		t.Fatalf("settlements = %+v", detail.Settlements)
	}
}

func TestRepairFormData(t *testing.T) {
	s := session.New("20000002", "gw", "school", repairTransport(t))
	sv := New(s)
	form, err := sv.FormData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(form.Areas) != 2 || form.Areas[0].GroupName != "东区" || form.Areas[0].Items[0].ItemID != "A1" {
		t.Fatalf("areas = %+v", form.Areas)
	}
	if len(form.Projects) != 2 || form.Projects[1].Items[0].Name != "图书馆" {
		t.Fatalf("projects = %+v", form.Projects)
	}
}

func TestRepairSubmitSuccessAndFailure(t *testing.T) {
	tr := testutil.LoginTransport(t).
		On("/index.php/index/user", transport.NewResponse(200, []byte("ok"))).
		On("/repair?sf_request_type=ajax", transport.NewResponse(200, []byte(`{"isok":true,"msg":"已收到"}`)))
	cfg := session.DefaultConfig()
	cfg.AllowWrites = true
	s := session.NewWithConfig("20000002", "gw", "school", tr, cfg)
	sv := New(s)
	result, err := sv.Submit(context.Background(), &SubmitRequest{AreaID: "A1"})
	if err != nil || result != "报修提交成功" {
		t.Fatalf("result = %q %v", result, err)
	}

	tr2 := testutil.LoginTransport(t).
		On("/index.php/index/user", transport.NewResponse(200, []byte("ok"))).
		On("/repair?sf_request_type=ajax", transport.NewResponse(200, []byte(`{"isok":false,"msg":"手机号无效"}`)))
	s2 := session.NewWithConfig("20000002", "gw", "school", tr2, cfg)
	sv2 := New(s2)
	_, err = sv2.Submit(context.Background(), &SubmitRequest{AreaID: "A1"})
	if err == nil || !strings.Contains(err.Error(), "手机号无效") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubmitRequestFormFields(t *testing.T) {
	request := SubmitRequest{AreaID: "A1", Description: "desc"}
	form := request.ToFormData()
	found := map[string]string{}
	for _, kv := range form {
		found[kv[0]] = kv[1]
	}
	if found["areaid"] != "A1" || found["taskType"] != "1" || found["pic"] != "" {
		t.Fatalf("form = %v", found)
	}
}
