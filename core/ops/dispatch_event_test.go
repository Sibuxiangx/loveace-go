package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// TestDispatchEmitsOneOperationObservation pins §9's sample unit: one
// logical operation emits exactly one KindOperation observation — with the
// outcome spelled "success" or "failure:<code>" — regardless of internal
// retries.
func TestDispatchEmitsOneOperationObservation(t *testing.T) {
	tr := academicMock(t)
	sink := events.NewMemory()
	s := session.New("20000002", "gw", "school", tr)
	s.SetEventSink(sink)
	ctx := context.Background()

	if _, err := Dispatch(ctx, s, "jwc.academic_info", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Dispatch(ctx, s, "jwc.academic_info", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// A failing op (unknown route → parse_error or http error) still emits.
	_, _ = Dispatch(ctx, s, "jwc.terms", json.RawMessage(`{}`))

	ops := 0
	for _, o := range sink.Snapshot() {
		if o.Kind == events.KindOperation {
			ops++
			if o.Outcome != "success" && !jsonOutFailure(o.Outcome) {
				t.Fatalf("outcome spelling = %q", o.Outcome)
			}
			if o.Operation != "jwc.academic_info" && o.Operation != "jwc.terms" {
				t.Fatalf("operation = %q", o.Operation)
			}
		}
	}
	if ops != 3 {
		t.Fatalf("emitted %d operation observations, want exactly 3 (one per logical op)", ops)
	}
}

func jsonOutFailure(outcome string) bool {
	const prefix = "failure:"
	return len(outcome) > len(prefix) && outcome[:len(prefix)] == prefix
}
