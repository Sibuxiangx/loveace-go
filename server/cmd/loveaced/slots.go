package main

// Vault-sealed persistence for the school manager's user-session
// snapshots, the credential resolver the snapshot restores rely on, and
// the zero-credential gateway probe. (The EC slot store and the watermark
// refill loop left with the ecpool: one EC session per one UAAP identity
// needs no pooled slots to keep warm.)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// sealedUserSnapshots adapts the store to school.SnapshotStore, sealing
// every evicted session's cookies with the vault (AAD bound to the user id).
// Cold eviction never leaves plaintext cookies behind.
type sealedUserSnapshots struct {
	st store.Store
	v  *vault.Vault
}

// snapshotAAD binds a sealed snapshot to its owner: a row copied to another
// user's key fails to open.
func snapshotAAD(userID string) string { return "user-session:" + userID }

// legacySnapshotAAD is the constant domain used before the user id was bound.
const legacySnapshotAAD = "user-session"

func (s *sealedUserSnapshots) SaveUserSnapshot(_ context.Context, snap school.UserSnapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	blob, err := s.v.Encrypt(snapshotAAD(snap.UserID), payload)
	if err != nil {
		return err
	}
	return s.st.PutUserSnapshot(context.Background(), store.UserSnapshot{
		UserID: snap.UserID, Blob: blob, SavedAt: snap.SavedAt,
	})
}

func (s *sealedUserSnapshots) DeleteUserSnapshot(ctx context.Context, userID string) error {
	return s.st.DeleteUserSnapshot(ctx, userID)
}

func (s *sealedUserSnapshots) LoadUserSnapshot(ctx context.Context, userID string) (*school.UserSnapshot, error) {
	row, err := s.st.GetUserSnapshot(ctx, userID)
	if err != nil {
		return nil, err
	}
	payload, err := s.v.Decrypt(snapshotAAD(userID), row.Blob)
	if errors.Is(err, vault.ErrWrongUser) {
		// Rows sealed before the AAD carried the user id: read once, re-sealed on the next save.
		payload, err = s.v.Decrypt(legacySnapshotAAD, row.Blob)
	}
	if err != nil {
		return nil, err
	}
	var snap school.UserSnapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// vaultCredentialResolver decrypts a user's school credentials from the
// vault (same envelope the login flow wrote, AAD-bound to the user id).
// Wired into the school manager so a snapshot-restored session carries
// REAL credentials — its cookies will eventually expire and the recovery
// must be able to re-login (an empty-credential restore was a zombie).
type vaultCredentialResolver struct {
	st store.Store
	v  *vault.Vault
}

func (r vaultCredentialResolver) Resolve(ctx context.Context, userID string) (string, string, string, error) {
	cred, err := r.st.GetCredential(ctx, userID)
	if err != nil {
		return "", "", "", err
	}
	plaintext, err := r.v.Decrypt(userID, cred.Blob)
	if err != nil {
		return "", "", "", err
	}
	var payload vault.CredPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil || payload.Account == "" {
		return "", "", "", fmt.Errorf("credential payload unusable for %s", userID)
	}
	gw := payload.GatewayPassword
	if gw == "" {
		gw = payload.Password
	}
	return payload.Account, gw, payload.Password, nil
}

// gatewayProbe is the zero-credential L0 probe (§9): a plain GET of the
// gateway auth page. alive = auth page served; polluted = the page
// demands a CAPTCHA (RndImg=1); down = unreachable or unrecognizable.
func gatewayProbe(ctx context.Context) string {
	client := &http.Client{Timeout: 5 * time.Second}
	url := session.DefaultConfig().VPNServer + "/por/login_auth.csp"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return health.ProbeDown
	}
	resp, err := client.Do(req)
	if err != nil {
		return health.ProbeDown
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return health.ProbeDown
	}
	text := string(body)
	switch {
	case strings.Contains(text, "<RndImg>1</RndImg>"):
		return health.ProbePolluted
	case strings.Contains(text, "<TwfID>"):
		return health.ProbeAlive
	default:
		return health.ProbeDown
	}
}
