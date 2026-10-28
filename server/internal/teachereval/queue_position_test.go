package teachereval

import "testing"

// Task views report queue_position from the pool, so the position must be
// right while the task waits, must read 0 once it runs, and the queued task
// must run to completion after the slot frees.
func TestQueuePositionIsReported(t *testing.T) {
	h := newHarness(t, Config{Enabled: true, Capacity: 1})
	for _, id := range []string{"a", "b"} {
		h.school.user(id)
	}
	first := h.create("a", ScoreRandom)
	queued := h.create("b", ScoreRandom)

	if got := h.pool.QueuePosition(queued.ID); got != 1 {
		t.Fatalf("queued position = %d, want 1", got)
	}
	if got := h.pool.QueuePosition(first.ID); got != 0 {
		t.Fatalf("running task position = %d, want 0", got)
	}

	h.drive(h.finished(first.ID))
	if got := h.pool.QueuePosition(queued.ID); got != 0 {
		t.Fatalf("after the slot freed, position = %d, want 0", got)
	}
	h.drive(h.finished(queued.ID))
	if got := h.status(queued.ID); got.Status != "completed" {
		t.Fatalf("queued task after its turn = %s, want completed", got.Status)
	}
}
