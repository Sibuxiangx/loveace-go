package jwc

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// freeClassroomTransport wires the five free-classroom endpoints with
// synthetic answers. The mock matches URLs only, so both campus requests to
// TeachBuildingName get the same two buildings.
func freeClassroomTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		OnRequest(transport.Post, "/freeClassroomQuery/TeachBuildingName", transport.NewResponse(200, []byte(
			`[{"id":{"teachingBuildingNumber":"36"},"teachingBuildingName":"示例教学楼","remark":""},`+
				`{"id":{"teachingBuildingNumber":"12"},"teachingBuildingName":"示例东楼（停用）","remark":""}]`))).
		OnRequest(transport.Get, "/freeClassroomQuery/custom", transport.NewResponse(200, []byte(
			`<html><div>第 8 周</div><select id="jslxdm"><option value="">不限</option><option value="32">示例普通教室</option></select></html>`))).
		OnRequest(transport.Post, "/freeClassroomQuery/search", transport.NewResponse(200, []byte(
			`[{"pageContext":{"totalCount":2},"records":[`+
				`{"id":{"campusNumber":"2","teachingBuildingNumber":"36","classroomNumber":"101"},"campusName":"西校","teachingBuildingName":"示例教学楼","classroomName":"示例教室101","classroomStatusCode":"01","classroomTypeCode":"32","classroomTypeName":"示例普通教室","classNumberOfSeats":"60"},`+
				`{"id":{"campusNumber":"2","teachingBuildingNumber":"36","classroomNumber":"102"},"campusName":"西校","teachingBuildingName":"示例教学楼","classroomName":"示例教室102","classroomStatusCode":"01","classroomTypeCode":"32","classroomTypeName":"示例普通教室","classNumberOfSeats":"48"}]}]`))).
		OnRequest(transport.Get, "/classroomCurriculum/2/36/classroomJson", transport.NewResponse(200, []byte(
			`[{"id":{"classroomNumber":"201"},"classroomName":"示例教室201","classroomStatusCode":"02","classroomTypeCode":"32","placeNum":60}]`))).
		OnRequest(transport.Get, "/freeClassroomQuery/classroomQuery", transport.NewResponse(200, []byte(
			`<html><input type="hidden" id="wsSection" value="2_2_7,2_1_7,2_1_7"></html>`)))
}

func freeClassroomService(tr *transport.MockTransport) *Service {
	return New(session.New("20000002", "gw", "school", tr))
}

// requestForm returns the decoded form fields of the first recorded request whose URL contains substr.
func requestForm(t *testing.T, tr *transport.MockTransport, substr string) url.Values {
	t.Helper()
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, substr) {
			values, err := url.ParseQuery(string(r.Body))
			if err != nil {
				t.Fatal(err)
			}
			return values
		}
	}
	t.Fatalf("no request to %s", substr)
	return nil
}

func TestFreeClassroomBuildingsReadsBothCampuses(t *testing.T) {
	tr := freeClassroomTransport(t)
	buildings, err := freeClassroomService(tr).FreeClassroomBuildings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	asked := map[string]bool{}
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, "/freeClassroomQuery/TeachBuildingName") {
			values, _ := url.ParseQuery(string(r.Body))
			asked[values.Get("codeCamListName")] = true
		}
	}
	if !asked["2"] || !asked["1"] {
		t.Fatalf("campuses asked = %v, want both 2 and 1", asked)
	}
	if len(buildings) != 4 {
		t.Fatalf("buildings = %d, want 4 (two per campus)", len(buildings))
	}
	stopped := 0
	for _, b := range buildings {
		if b.Stopped {
			stopped++
			if !strings.Contains(b.BuildingName, "停用") {
				t.Fatalf("stopped building %+v lacks the 停用 marker", b)
			}
		}
	}
	if stopped != 2 {
		t.Fatalf("stopped buildings = %d, want 2 (the 停用 building on each campus)", stopped)
	}
}

func TestFreeClassroomMetaReadsWeekAndTypes(t *testing.T) {
	meta, err := freeClassroomService(freeClassroomTransport(t)).FreeClassroomMeta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.CurrentWeek == nil || *meta.CurrentWeek != 8 {
		t.Fatalf("current week = %v, want 8", meta.CurrentWeek)
	}
	want := []FreeClassroomOption{{Code: "32", Name: "示例普通教室"}}
	if !reflect.DeepEqual(meta.Types, want) {
		t.Fatalf("types = %+v, want %+v", meta.Types, want)
	}
}

// Weeks and slots are sent sorted; the school reads them as one comma-joined list.
func TestFreeClassroomSearchSendsSortedWeeksAndSlots(t *testing.T) {
	tr := freeClassroomTransport(t)
	rooms, err := freeClassroomService(tr).FreeClassroomSearch(context.Background(), FreeClassroomQuery{
		Weeks:        []string{"8", "2"},
		Slots:        []string{"5/1", "2/7"},
		CampusNumber: "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	form := requestForm(t, tr, "/freeClassroomQuery/search")
	if form.Get("weeks") != "2,8" || form.Get("wSection") != "2/7,5/1" {
		t.Fatalf("weeks = %q, wSection = %q", form.Get("weeks"), form.Get("wSection"))
	}
	if form.Get("codeCampusListNumber") != "2" || form.Get("pageSize") != "200" {
		t.Fatalf("campus = %q, pageSize = %q", form.Get("codeCampusListNumber"), form.Get("pageSize"))
	}
	if len(rooms) != 2 || rooms[0].ClassroomName != "示例教室101" || rooms[0].Seats == nil || *rooms[0].Seats != 60 {
		t.Fatalf("rooms = %+v", rooms)
	}
}

// The school's total ends paging: one request answers two rows of two.
func TestFreeClassroomSearchStopsAtSchoolTotal(t *testing.T) {
	tr := freeClassroomTransport(t)
	if _, err := freeClassroomService(tr).FreeClassroomSearch(context.Background(), FreeClassroomQuery{
		Weeks: []string{"8"}, Slots: []string{"2/7"},
	}); err != nil {
		t.Fatal(err)
	}
	searches := 0
	for _, r := range tr.Recorded() {
		if strings.Contains(r.URL, "/freeClassroomQuery/search") {
			searches++
		}
	}
	if searches != 1 {
		t.Fatalf("search requests = %d, want 1", searches)
	}
}

// A query without weeks or slots, or with out-of-range values, fails before
// any request: the school answers those with unreliable rows.
func TestFreeClassroomSearchRejectsBadArgumentsWithoutNetwork(t *testing.T) {
	cases := map[string]FreeClassroomQuery{
		"no weeks":      {Slots: []string{"2/7"}},
		"no slots":      {Weeks: []string{"8"}},
		"week 25":       {Weeks: []string{"25"}, Slots: []string{"2/7"}},
		"weekday 8":     {Weeks: []string{"8"}, Slots: []string{"8/1"}},
		"section 11":    {Weeks: []string{"8"}, Slots: []string{"2/11"}},
		"slot not a/b":  {Weeks: []string{"8"}, Slots: []string{"2-7"}},
		"non-numeric w": {Weeks: []string{"x"}, Slots: []string{"2/7"}},
	}
	for name, query := range cases {
		tr := transport.NewMockTransport()
		_, err := freeClassroomService(tr).FreeClassroomSearch(context.Background(), query)
		if errs.CodeOf(err) != errs.InvalidArgument {
			t.Fatalf("%s: code = %v, want invalid_argument", name, errs.CodeOf(err))
		}
		if n := len(tr.Recorded()); n != 0 {
			t.Fatalf("%s: %d requests reached the network", name, n)
		}
	}
}

func TestFreeClassroomRoomsMapStatusAndSeats(t *testing.T) {
	rooms, err := freeClassroomService(freeClassroomTransport(t)).FreeClassroomRooms(context.Background(), "2", "36", "示例教学楼")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 {
		t.Fatalf("rooms = %d, want 1", len(rooms))
	}
	got := rooms[0]
	if got.ClassroomNumber != "201" || got.StatusCode != "02" || got.CampusName != "西校" || got.BuildingName != "示例教学楼" {
		t.Fatalf("room = %+v", got)
	}
	if got.Seats == nil || *got.Seats != 60 {
		t.Fatalf("seats = %v, want 60", got.Seats)
	}
}

// Campus and building numbers go into the URL path, so only digits are allowed.
func TestFreeClassroomRoomsRejectsPathCharacters(t *testing.T) {
	tr := transport.NewMockTransport()
	_, err := freeClassroomService(tr).FreeClassroomRooms(context.Background(), "2/../x", "36", "示例教学楼")
	if errs.CodeOf(err) != errs.InvalidArgument {
		t.Fatalf("code = %v, want invalid_argument", errs.CodeOf(err))
	}
	if n := len(tr.Recorded()); n != 0 {
		t.Fatalf("%d requests reached the network", n)
	}
}

func TestFreeClassroomOccupancyReadsSortedUniqueTokens(t *testing.T) {
	tokens, err := freeClassroomService(freeClassroomTransport(t)).FreeClassroomOccupancy(context.Background(), "2", "36", "示例教学楼", "101")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2_1_7", "2_2_7"}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
}

func TestFreeClassroomOccupancyWithoutTokenIsParseError(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "/freeClassroomQuery/classroomQuery", transport.NewResponse(200, []byte(`<html></html>`)))
	_, err := freeClassroomService(tr).FreeClassroomOccupancy(context.Background(), "2", "36", "示例教学楼", "101")
	if errs.CodeOf(err) != errs.Parse {
		t.Fatalf("code = %v, want parse", errs.CodeOf(err))
	}
}
