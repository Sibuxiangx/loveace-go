package events

import "testing"

func TestMemorySink(t *testing.T) {
	s := NewMemory()
	s.Record(Observation{Kind: KindLogin, Operation: "login", Outcome: "success", DurationMS: 5})
	s.Record(Observation{Kind: KindReconnect, Outcome: "unavailable"})
	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot length = %d", len(snap))
	}
	if snap[0].Kind != KindLogin || snap[1].Outcome != "unavailable" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestAsyncNeverBlocksAndCloses(t *testing.T) {
	mem := NewMemory()
	a := NewAsync(mem, 4)
	for i := 0; i < 1000; i++ {
		a.Record(Observation{Outcome: "x"})
	}
	a.Close()
	if len(mem.Snapshot()) == 0 {
		t.Fatal("dispatcher should have recorded some events")
	}
	// After Close, further Record must not panic.
	a.Record(Observation{Outcome: "after-close"})
}
