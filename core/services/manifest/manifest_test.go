package manifest

import (
	"context"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/session"
)

const validManifest = `{
  "schema_version": 2,
  "revision": "r1",
  "generated_at": "2026-01-01T00:00:00Z",
  "announcements": [
    {"id":"a1","title":"t","platforms":["all"],"surfaces":["app"]},
    {"id":"a2","platforms":["ios"],"surfaces":["web"]},
    {"id":"a3","platforms":["all"],"surfaces":["app"],"expires_at":"2000-01-01T00:00:00Z"}
  ],
  "platforms": {"ios": {"minimum_supported_build": 10, "releases": []}}
}`

func TestFetchV2AndFilter(t *testing.T) {
	tr := transport.NewMockTransport().
		On("manifest_v2.json", transport.NewResponse(200, []byte(validManifest)))
	s := session.New("20000002", "gw", "school", tr)
	sv := New(s)
	m, err := sv.FetchV2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 2 || m.Revision != "r1" {
		t.Fatalf("manifest = %+v", m)
	}
	visible := m.VisibleAnnouncements("ios")
	if len(visible) != 1 || visible[0].ID != "a1" {
		t.Fatalf("visible = %+v", visible)
	}
}

func TestFetchFallsBackToSecondURL(t *testing.T) {
	tr := transport.NewMockTransport().
		On("loveace.linota.cn", transport.NewResponse(500, []byte("nope"))).
		On("release.loveace.top", transport.NewResponse(200, []byte(validManifest)))
	s := session.New("20000002", "gw", "school", tr)
	sv := New(s)
	if _, err := sv.FetchV2(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRejectsWrongSchema(t *testing.T) {
	tr := transport.NewMockTransport().
		On("manifest_v2.json", transport.NewResponse(200, []byte(`{"schema_version":1}`)))
	s := session.New("20000002", "gw", "school", tr)
	sv := New(s)
	_, err := sv.FetchV2(context.Background())
	if err == nil {
		t.Fatal("expected schema error")
	}
}

func TestNoticeFilterRules(t *testing.T) {
	future := "2999-01-01T00:00:00Z"
	past := "2000-01-01T00:00:00Z"
	n := Notice{ID: "a1", Platforms: []string{"all"}, Surfaces: []string{"app"}}
	if !n.IsVisibleInApp("ios") {
		t.Fatal("all/app must be visible")
	}
	expired := n
	expired.ExpiresAt = &past
	if expired.IsVisibleInApp("ios") {
		t.Fatal("expired must not be visible")
	}
	unexpired := n
	unexpired.ExpiresAt = &future
	if !unexpired.IsVisibleInApp("ios") {
		t.Fatal("unexpired must be visible")
	}
	wrongSurface := n
	wrongSurface.Surfaces = []string{"web"}
	if wrongSurface.IsVisibleInApp("ios") {
		t.Fatal("wrong surface must not be visible")
	}
	noID := Notice{Platforms: []string{"all"}, Surfaces: []string{"app"}}
	if noID.IsVisibleInApp("ios") {
		t.Fatal("empty id must not be visible")
	}
	_ = time.Now
}
