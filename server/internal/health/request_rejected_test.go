package health

import (
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

// A gateway-rejected request shape (20026) is a gateway failure sample, not a
// user fault: it must degrade the gateway layer like any other gateway error.
func TestRequestRejectedCountsAsGatewayFailure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	users := []string{"u1", "u2"}
	for i := range 5 {
		at(t, r, users[i%2], events.KindGatewayAuth, "login", "request_rejected")
	}
	gw := r.Status().Layers["gateway"]
	if gw.Samples10m != 5 || gw.FailRate10m != 1 {
		t.Fatalf("gateway layer = %+v, want 5 failing samples", gw)
	}
}
