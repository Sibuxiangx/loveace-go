package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

func snapshotFixture(t *testing.T) (*sealedUserSnapshots, store.Store, *vault.Vault) {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	st := mem.New()
	return &sealedUserSnapshots{st: st, v: v}, st, v
}

// A snapshot row copied under another user's id must not open: the sealed
// blob is bound to its owner.
func TestSnapshotRowCopiedToAnotherUserWontOpen(t *testing.T) {
	ctx := context.Background()
	snaps, st, _ := snapshotFixture(t)
	if err := snaps.SaveUserSnapshot(ctx, school.UserSnapshot{UserID: "u1", DeviceID: "d1", SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetUserSnapshot(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutUserSnapshot(ctx, store.UserSnapshot{UserID: "u2", Blob: row.Blob, SavedAt: row.SavedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := snaps.LoadUserSnapshot(ctx, "u2"); err == nil {
		t.Fatal("a snapshot copied to another user opened under that user")
	}
	if got, err := snaps.LoadUserSnapshot(ctx, "u1"); err != nil || got.UserID != "u1" {
		t.Fatalf("owner must still restore: %v", err)
	}
}

// Rows sealed before the AAD carried the user id must keep restoring; they
// are re-sealed on their next save.
func TestLegacySnapshotStillRestores(t *testing.T) {
	ctx := context.Background()
	snaps, st, v := snapshotFixture(t)
	payload, err := json.Marshal(school.UserSnapshot{UserID: "u1", DeviceID: "d1", SavedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := v.Encrypt(legacySnapshotAAD, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutUserSnapshot(ctx, store.UserSnapshot{UserID: "u1", Blob: blob, SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := snaps.LoadUserSnapshot(ctx, "u1"); err != nil || got.DeviceID != "d1" {
		t.Fatalf("legacy snapshot did not restore: %v", err)
	}
}
