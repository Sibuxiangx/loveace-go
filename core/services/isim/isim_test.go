package isim

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func isimTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		On("/go?", transport.NewResponse(200, []byte("<html>ok</html>")).
			WithHeader("Set-Cookie", "JSESSIONID=ISIMSESS1; Path=/")).
		On("/about/floors/", transport.NewResponse(200, []byte(
			`[{"floordm":["","F1","F2"],"floorname":["请选择","1层","2层"]}]`))).
		On("/about/rooms/", transport.NewResponse(200, []byte(
			`[{"roomdm":["","101","102"],"roomname":["请选择","101室","102室"]}]`))).
		On("/about/rebinding", transport.NewResponse(200, []byte("ok"))).
		On("/use/record", transport.NewResponse(200, []byte(`
		<div id="divRecord"><ul>
		<li><div class="item-title">2026-01-02</div><div class="item-after">3.5度</div><div class="item-subtitle">电表: M1</div></li>
		</ul></div>
		<div><span>购电：12.50 度</span><span>补助：2.00 度</span></div>`))).
		On("/pay/record", transport.NewResponse(200, []byte(`
		<div id="divRecord"><ul>
		<li><div class="item-title">2026-01-01</div><div class="item-after">-20.00元</div><div class="item-subtitle">类型: 购电</div></li>
		</ul></div>`)))
}

func TestIsimFloorsAndRooms(t *testing.T) {
	s := session.New("20000002", "gw", "school", isimTransport(t))
	sv := New(s)
	floors, err := sv.Floors(context.Background(), "B1")
	if err != nil {
		t.Fatal(err)
	}
	if len(floors) != 2 || floors[0].Code != "F1" || floors[0].Name != "1层" {
		t.Fatalf("floors = %+v", floors)
	}
	rooms, err := sv.Rooms(context.Background(), "F1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 || rooms[1].Code != "102" {
		t.Fatalf("rooms = %+v", rooms)
	}
}

func TestIsimElectricityInfo(t *testing.T) {
	s := session.New("20000002", "gw", "school", isimTransport(t))
	sv := New(s)
	info, err := sv.ElectricityInfo(context.Background(), "101", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Balance.RemainingPurchased != 12.5 || info.Balance.RemainingSubsidy != 2.0 {
		t.Fatalf("balance = %+v", info.Balance)
	}
	if info.Balance.Total() != 14.5 {
		t.Fatalf("total = %v", info.Balance.Total())
	}
	if len(info.UsageRecords) != 1 || info.UsageRecords[0].UsageAmount != 3.5 || info.UsageRecords[0].MeterName != "M1" {
		t.Fatalf("usage = %+v", info.UsageRecords)
	}
	if len(info.Payments) != 1 || info.Payments[0].Amount != -20 || info.Payments[0].PaymentType != "购电" {
		t.Fatalf("payments = %+v", info.Payments)
	}
}

func TestSplitJSArray(t *testing.T) {
	got := splitJSArray(` "a", 'b', "c" , "" , `)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got = %v", got)
	}
}

func TestNormalizeJSObject(t *testing.T) {
	if got := normalizeJSObject(`{"a":1}`); got != `{"a":1}` {
		t.Fatalf("valid json must pass through: %q", got)
	}
	if got := normalizeJSObject(`[{floordm:["F1"]}]`); !strings.Contains(got, `"floordm"`) {
		t.Fatalf("normalized = %q", got)
	}
}
