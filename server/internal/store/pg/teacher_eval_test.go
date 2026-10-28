package pg

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// TestTeacherEval covers the auto-evaluation tables (migration 0006) on real
// Postgres: task+course round trip, the one-active-task
// invariant, the 200-row log cap and limit semantics. Skipped unless
// LOVEACE_TEST_PG holds a postgres URL (see TestStoreRoundtrip).
func TestTeacherEval(t *testing.T) {
	url := os.Getenv("LOVEACE_TEST_PG")
	if url == "" {
		t.Skip("LOVEACE_TEST_PG not set; pg integration test skipped")
	}
	ctx := context.Background()
	s, err := New(ctx, url)
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	defer s.Close()

	// Re-runnable: unique user per run (the container keeps earlier rows).
	userID := "u-" + store.NewID()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Task + courses: courses inserted out of order come back by Position.
	task1 := store.TeacherEvalTask{ID: store.NewID(), UserID: userID, DeviceID: store.NewID(),
		Status: "queued", CourseTotal: 3, CreatedAt: now}
	courses := []store.TeacherEvalTaskCourse{
		{TaskID: task1.ID, CourseKey: "c-2", Name: "C", Teacher: "t", Position: 2, Status: "pending", UpdatedAt: now},
		{TaskID: task1.ID, CourseKey: "c-0", Name: "A", Teacher: "t", Position: 0, Status: "pending", UpdatedAt: now},
		{TaskID: task1.ID, CourseKey: "c-1", Name: "B", Teacher: "t", Position: 1, Status: "pending", UpdatedAt: now},
	}
	if err := s.CreateTeacherEvalTask(ctx, task1, courses); err != nil {
		t.Fatalf("create task: %v", err)
	}
	got, err := s.GetTeacherEvalTask(ctx, task1.ID)
	if err != nil || got.Status != "queued" || got.CourseTotal != 3 || got.DeviceID != task1.DeviceID ||
		!got.CreatedAt.Equal(now) || !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() {
		t.Fatalf("task roundtrip: %+v err=%v", got, err)
	}
	gotCourses, err := s.ListTeacherEvalCourses(ctx, task1.ID)
	if err != nil || len(gotCourses) != 3 {
		t.Fatalf("courses: %+v err=%v", gotCourses, err)
	}
	for i, c := range gotCourses {
		if c.Position != i || c.CourseKey != fmt.Sprintf("c-%d", i) || !c.SubmittedAt.IsZero() {
			t.Fatalf("course %d out of order or wrong: %+v", i, c)
		}
	}

	// One active task per user.
	task2 := store.TeacherEvalTask{ID: store.NewID(), UserID: userID, Status: "queued", CreatedAt: now}
	if err := s.CreateTeacherEvalTask(ctx, task2, nil); err != store.ErrTeacherEvalTaskActive {
		t.Fatalf("second active create: err=%v, want ErrTeacherEvalTaskActive", err)
	}
	if active, err := s.ActiveTeacherEvalTask(ctx, userID); err != nil || active.ID != task1.ID {
		t.Fatalf("active task: %+v err=%v", active, err)
	}
	task1.Status = "completed"
	task1.FinishedAt = now
	if err := s.UpdateTeacherEvalTask(ctx, task1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveTeacherEvalTask(ctx, userID); err != store.ErrNotFound {
		t.Fatalf("no active task: err=%v, want not found", err)
	}
	if err := s.CreateTeacherEvalTask(ctx, task2, nil); err != nil {
		t.Fatalf("create after completion: %v", err)
	}
	list, err := s.ListTeacherEvalTasks(ctx, userID, 0)
	if err != nil || len(list) != 2 || list[0].ID != task2.ID || list[1].ID != task1.ID {
		t.Fatalf("list tasks newest first: %+v err=%v", list, err)
	}
	recoverable, err := s.ListRecoverableTeacherEvalTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recoverable {
		if r.ID == task1.ID {
			t.Fatal("completed task listed as recoverable")
		}
		found = found || r.ID == task2.ID
	}
	if !found {
		t.Fatal("queued task missing from recoverable list")
	}

	// Course update.
	courses[0].Status = "success"
	courses[0].SubmitAttempts = 1
	courses[0].SubmittedAt = now
	if err := s.UpdateTeacherEvalCourse(ctx, courses[0]); err != nil {
		t.Fatal(err)
	}
	if gotCourses, _ := s.ListTeacherEvalCourses(ctx, task1.ID); gotCourses[0].Status != "success" || gotCourses[0].SubmitAttempts != 1 {
		t.Fatalf("course update: %+v", gotCourses[0])
	}

	// Replace the course set: 2 courses -> 1 course, ordered by Position.
	replacement := []store.TeacherEvalTaskCourse{
		{TaskID: task2.ID, CourseKey: "r-0", Name: "R", Teacher: "t", Position: 0, Status: "pending", UpdatedAt: now},
	}
	if err := s.SetTeacherEvalCourses(ctx, task2.ID, []store.TeacherEvalTaskCourse{
		{CourseKey: "x-1", Name: "X1", Position: 1, Status: "pending", UpdatedAt: now},
		{CourseKey: "x-0", Name: "X0", Position: 0, Status: "pending", UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeacherEvalCourses(ctx, task2.ID, replacement); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.ListTeacherEvalCourses(ctx, task2.ID); err != nil || len(cs) != 1 || cs[0].CourseKey != "r-0" || cs[0].TaskID != task2.ID {
		t.Fatalf("replaced courses: %+v err=%v", cs, err)
	}
	if err := s.SetTeacherEvalCourses(ctx, store.NewID(), nil); err != store.ErrNotFound {
		t.Fatalf("set courses on missing task: err=%v, want not found", err)
	}

	// ScoreMode round trip on a separate user: create "max", read, update "random", read.
	otherUser := "u-" + store.NewID()
	taskC := store.TeacherEvalTask{ID: store.NewID(), UserID: otherUser, Status: "queued",
		ScoreMode: "max", CreatedAt: now}
	if err := s.CreateTeacherEvalTask(ctx, taskC, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTeacherEvalTask(ctx, taskC.ID); err != nil || got.ScoreMode != "max" {
		t.Fatalf("score mode after create: %+v err=%v", got, err)
	}
	taskC.ScoreMode = "random"
	if err := s.UpdateTeacherEvalTask(ctx, taskC); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTeacherEvalTask(ctx, taskC.ID); err != nil || got.ScoreMode != "random" {
		t.Fatalf("score mode after update: %+v err=%v", got, err)
	}

	// Log cap: 205 appends keep the newest 200; limit<=0 reads 50.
	for i := range 205 {
		if err := s.AppendTeacherEvalLog(ctx, task1.ID, now, fmt.Sprintf("m-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := s.ListTeacherEvalLogs(ctx, task1.ID, 0)
	if err != nil || len(logs) != 50 || logs[0].Message != "m-155" || logs[49].Message != "m-204" {
		t.Fatalf("default log limit: n=%d err=%v", len(logs), err)
	}
	for i := 1; i < len(logs); i++ {
		if logs[i].Seq <= logs[i-1].Seq {
			t.Fatalf("logs not oldest-first by seq: %+v", logs)
		}
	}
	logs, err = s.ListTeacherEvalLogs(ctx, task1.ID, 1000)
	if err != nil || len(logs) != 200 || logs[0].Message != "m-5" || logs[199].Message != "m-204" {
		t.Fatalf("capped log retention: n=%d err=%v", len(logs), err)
	}
	if err := s.AppendTeacherEvalLog(ctx, store.NewID(), now, "orphan"); err != store.ErrNotFound {
		t.Fatalf("log for missing task: err=%v, want not found", err)
	}
}
