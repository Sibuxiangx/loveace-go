package api

// The database-users load test (2026-10-17, the ProxyPool 1:1 model): the
// store is seeded with N real-shaped users (vault-encrypted credentials,
// exactly the rows production writes), the test READS THEM BACK from the
// store — the same resolution the silent-relogin path uses — and drives
// concurrent logins through the full HTTP stack.
//
// What the funnel must show: every DISTINCT account costs exactly one EC
// credential submission (the /por/login_psw.csp POST) no matter how the
// run is scheduled, and every login ends 200.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

func TestBenchDatabaseUsersLoginFunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const users = 2000

	tr := successTransport(t)
	gw := &countingGateway{inner: tr}

	st := mem.New()
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	// Seed the database: one credential row per user, sealed like prod.
	ctx := context.Background()
	for i := 0; i < users; i++ {
		userID := fmt.Sprintf("bench-user-%04d", i)
		account := fmt.Sprintf("2024%05d", i)
		payload, err := json.Marshal(vault.CredPayload{Account: account, Password: "Passw0rd!123"})
		if err != nil {
			t.Fatal(err)
		}
		blob, err := v.Encrypt(userID, payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertCredential(ctx, store.Credential{UserID: userID, Blob: blob, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PasswordMaxLen: 64, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour, KEKVersion: "v1"}
	srv := New(Deps{
		Cfg:    cfg,
		Store:  st,
		Vault:  v,
		Tokens: token.NewManager(priv, cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		School: school.NewManager(func() transport.Transport { return gw }, school.WithoutLoginGate()),
		Pepper: bytes.Repeat([]byte{2}, 32),
		// The per-IP limiter would shape a synthetic single-IP burst;
		// this bench measures the SCHOOL-side funnel, so it is opened.
		LoginPerMinute: 1_000_000,
	})
	h := srv.Router()

	// Pull the users from the DATABASE, not from thin air: resolve every
	// credential row through the vault, exactly like the silent-relogin
	// and (the late) refill loop did.
	type dbUser struct{ account, password string }
	ids, err := st.ListCredentialUserIDs(ctx, users)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != users {
		t.Fatalf("database returned %d users, want %d", len(ids), users)
	}
	dbUsers := make([]dbUser, 0, len(ids))
	for _, id := range ids {
		cred, err := st.GetCredential(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := v.Decrypt(id, cred.Blob)
		if err != nil {
			t.Fatal(err)
		}
		var payload vault.CredPayload
		if err := json.Unmarshal(plaintext, &payload); err != nil || payload.Account == "" {
			t.Fatalf("credential payload unusable for %s", id)
		}
		dbUsers = append(dbUsers, dbUser{account: payload.Account, password: payload.Password})
	}

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	const workers = 64
	jobs := make(chan dbUser, workers)
	results := make([]int, users)
	var wg sync.WaitGroup
	widx := 0
	var mu sync.Mutex
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range jobs {
				req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions", mustJSON(t, map[string]any{
					"account": u.account, "password": u.password,
					"device_name": "bench", "platform": "web",
				}))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				mu.Lock()
				results[widx] = rec.Code
				widx++
				mu.Unlock()
			}
		}()
	}
	for _, u := range dbUsers {
		jobs <- u
	}
	close(jobs)
	wg.Wait()
	wall := time.Since(start)

	ok, codes := 0, map[int]int{}
	for _, c := range results {
		codes[c]++
		if c == http.StatusOK {
			ok++
		}
	}
	auths, ecPosts := gw.counts()
	t.Logf("funnel: users=%d ok=%d wall=%s ec_credential_posts=%d gateway_auths=%d codes=%v",
		users, ok, wall.Round(time.Millisecond), ecPosts, auths, codes)

	if ok != users {
		t.Fatalf("logins ok = %d/%d, codes = %v", ok, users, codes)
	}
	// THE capacity invariant of the 1:1 model: one EC credential
	// submission per distinct account, exactly.
	if ecPosts != users {
		t.Fatalf("EC credential submissions = %d, want exactly %d (one per account)", ecPosts, users)
	}
	if auths != users {
		t.Fatalf("gateway auth-page GETs = %d, want exactly %d", auths, users)
	}

	// No goroutine leak: the login burst must not strand workers.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	if now := runtime.NumGoroutine(); now > baseGoroutines+10 {
		t.Fatalf("goroutines %d → %d after the load burst", baseGoroutines, now)
	}
}

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// countingGateway wraps the mock and counts the gateway endpoints the
// login funnel touches.
type countingGateway struct {
	inner transport.Transport
	mu    sync.Mutex
	auths int
	posts int
}

func (g *countingGateway) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	if strings.Contains(r.URL, "/por/login_auth.csp") {
		g.mu.Lock()
		g.auths++
		g.mu.Unlock()
	} else if strings.Contains(r.URL, "/por/login_psw.csp") {
		g.mu.Lock()
		g.posts++
		g.mu.Unlock()
	}
	return g.inner.Execute(ctx, r)
}

func (g *countingGateway) counts() (auths, posts int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.auths, g.posts
}
