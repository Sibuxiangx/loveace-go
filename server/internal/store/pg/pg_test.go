package pg

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// TestStoreRoundtrip exercises the pg store end-to-end. It is skipped
// unless LOVEACE_TEST_PG holds a postgres URL, e.g. a podman container on
// the test server:
//
//	podman run -d --name loveace-pg -p 5432:5432 \
//	  -e POSTGRES_USER=loveace -e POSTGRES_PASSWORD=test -e POSTGRES_DB=loveace docker.io/library/postgres:16
//	LOVEACE_TEST_PG="postgres://loveace:test@host:5432/loveace" go test ./server/internal/store/pg/
func TestStoreRoundtrip(t *testing.T) {
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

	// Re-runnable: every run uses unique identifiers (the container keeps
	// data from previous runs).
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	hash := "hash-" + run
	u, created, err := s.GetOrCreateUser(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first create should return created=true")
	}
	u2, created2, err := s.GetOrCreateUser(ctx, hash)
	if err != nil || created2 || u2.ID != u.ID {
		t.Fatalf("second lookup: created=%v same=%v err=%v", created2, u2.ID == u.ID, err)
	}

	v, err := vault.New(make([]byte, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := v.Encrypt(u.ID, []byte(`{"account":"20000003","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredential(ctx, store.Credential{UserID: u.ID, Blob: blob, Fingerprint: "fp", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredential(ctx, u.ID)
	if err != nil || got.Fingerprint != "fp" {
		t.Fatalf("credential roundtrip: %+v err=%v", got, err)
	}

	d, err := s.CreateDevice(ctx, u.ID, "phone", "ios")
	if err != nil {
		t.Fatal(err)
	}
	_, rhash, err := token.NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRefreshToken(ctx, store.RefreshToken{Hash: rhash, UserID: u.ID, DeviceID: d.ID, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRefreshToken(ctx, rhash); err != nil {
		t.Fatal(err)
	}
	if err := s.RotateRefreshToken(ctx, rhash, rhash+"-new", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The successor row MUST be queryable by the new hash — a live bug
	// (2026-10-15) shipped rotation without the INSERT, so every first
	// refresh in production 401'd and forced a re-login.
	nt, err := s.GetRefreshToken(ctx, rhash+"-new")
	if err != nil {
		t.Fatalf("successor token after rotate: %v", err)
	}
	if nt.RotatedTo != "" || nt.DeviceID != d.ID {
		t.Fatalf("successor token state: %+v", nt)
	}
	if err := s.RevokeDeviceTokens(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRefreshToken(ctx, rhash)
	if err != nil || !r.Revoked || r.RotatedTo == "" {
		t.Fatalf("token state after rotate+revoke: %+v err=%v", r, err)
	}
	if err := s.RevokeDevice(ctx, u.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDevice(ctx, u.ID, d.ID); err != nil {
		t.Fatal(err)
	}

	// Pool slot snapshots (migration 0002): upsert, list, delete.
	slotID := "slot-" + run
	slotBlob, err := v.Encrypt("ecpool-slot", []byte(`{"id":"`+slotID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutPoolSlot(ctx, store.PoolSlot{
		ID: slotID, Blob: slotBlob,
		DepositedAt: time.Now(), LastVerifiedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPoolSlot(ctx, store.PoolSlot{
		ID: slotID, Blob: slotBlob,
		DepositedAt: time.Now(), LastVerifiedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	slots, err := s.ListPoolSlots(ctx)
	if err != nil || len(slots) != 1 || slots[0].ID != slotID {
		t.Fatalf("pool slot roundtrip: %+v err=%v", slots, err)
	}
	if payload, err := v.Decrypt("ecpool-slot", slots[0].Blob); err != nil || string(payload) != `{"id":"`+slotID+`"}` {
		t.Fatalf("pool slot blob roundtrip: %q err=%v", payload, err)
	}
	if err := s.DeletePoolSlot(ctx, slotID); err != nil {
		t.Fatal(err)
	}
	if slots, err := s.ListPoolSlots(ctx); err != nil || len(slots) != 0 {
		t.Fatalf("pool slot delete: %+v err=%v", slots, err)
	}

	// User session snapshots (migration 0003, §9.1 cold eviction).
	snapBlob, err := v.Encrypt("user-session", []byte(`{"cookies":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutUserSnapshot(ctx, store.UserSnapshot{UserID: u.ID, Blob: snapBlob, SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetUserSnapshot(ctx, u.ID); err != nil || got.UserID != u.ID {
		t.Fatalf("user snapshot roundtrip: %+v err=%v", got, err)
	}
	if err := s.DeleteUserSnapshot(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserSnapshot(ctx, u.ID); err != store.ErrNotFound {
		t.Fatalf("snapshot delete: err=%v, want not found", err)
	}

	// Announcements + acks (§11.2).
	ann := store.Announcement{ID: store.NewID(), Level: "info", BodyMD: "hi",
		Audience: "both", Active: true, StartsAt: time.Now()}
	if err := s.CreateAnnouncement(ctx, ann); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListAnnouncements(ctx, true); err != nil || len(list) != 1 {
		t.Fatalf("announcement list: %+v err=%v", list, err)
	}
	if err := s.AckAnnouncement(ctx, u.ID, ann.ID); err != nil {
		t.Fatal(err)
	}
	if acks, err := s.ListAcks(ctx, u.ID); err != nil || len(acks) != 1 {
		t.Fatalf("acks: %v err=%v", acks, err)
	}
	if err := s.DeactivateAnnouncement(ctx, ann.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListAnnouncements(ctx, true); err != nil || len(list) != 0 {
		t.Fatalf("deactivated still listed: %+v", list)
	}

	// Calendar terms + rules (§11.1). Unique term id: the container keeps
	// rows from earlier runs, so version assertions need a fresh term.
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	termID := "term-" + run
	if err := s.UpsertCalendarTerm(ctx, store.CalendarTerm{ID: termID, FirstWeekMonday: day.AddDate(0, 0, -24), Weeks: 20}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCalendarRule(ctx, store.CalendarRule{ID: store.NewID(), TermID: termID, Day: day, Kind: "holiday", Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	if rules, err := s.ListCalendarRules(ctx, termID, "draft"); err != nil || len(rules) != 1 {
		t.Fatalf("calendar rules: %+v err=%v", rules, err)
	}
	// Publish promotes the draft and bumps the version.
	if v, err := s.PublishCalendar(ctx, termID); err != nil || v != 1 {
		t.Fatalf("publish: v=%d err=%v", v, err)
	}
	if v, _ := s.CalendarVersion(ctx, termID); v != 1 {
		t.Fatalf("calendar version = %d, want 1", v)
	}
	if rules, err := s.ListCalendarRules(ctx, termID, "published"); err != nil || len(rules) != 1 || rules[0].Version != 1 {
		t.Fatalf("published rules: %+v err=%v", rules, err)
	}
	if err := s.DeleteCalendarRule(ctx, store.NewID()); err != store.ErrNotFound {
		t.Fatalf("deleting a published rule must be not-found, got %v", err)
	}
}

// TestIdempotencyKeys covers the M5 write-idempotency round trip on real
// Postgres: fresh reserve, replay read-back, finalize, cleanup. Skipped
// unless LOVEACE_TEST_PG holds a postgres URL (see TestStoreRoundtrip).
func TestIdempotencyKeys(t *testing.T) {
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

	rec := store.IdempotencyRecord{
		Key: "idem-" + store.NewID(), UserID: "u-" + store.NewID(),
		Op: "ykt.utility_pay", RequestHash: "h1", Status: "pending",
	}
	if _, existed, err := s.ReserveIdempotency(ctx, rec); err != nil || existed {
		t.Fatalf("fresh reserve: existed=%v err=%v", existed, err)
	}
	prev, existed, err := s.ReserveIdempotency(ctx, rec)
	if err != nil || !existed || prev.Status != "pending" || prev.RequestHash != "h1" {
		t.Fatalf("second reserve: existed=%v prev=%+v err=%v", existed, prev, err)
	}
	if err := s.FinalizeIdempotency(ctx, rec.Key, rec.UserID, "done", []byte(`{"status":200,"body":{"ok":true}}`)); err != nil {
		t.Fatal(err)
	}
	prev, _, err = s.ReserveIdempotency(ctx, store.IdempotencyRecord{Key: rec.Key, UserID: rec.UserID, Op: rec.Op, RequestHash: "h1", Status: "pending"})
	if err != nil || prev.Status != "done" || string(prev.Response) != `{"status":200,"body":{"ok":true}}` {
		t.Fatalf("after finalize: prev=%+v err=%v", prev, err)
	}
	if err := s.CleanupIdempotency(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, existed, _ := s.ReserveIdempotency(ctx, rec); existed {
		t.Fatal("cleanup left the record behind")
	}
}
