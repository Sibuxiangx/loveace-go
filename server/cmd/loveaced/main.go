// Command loveaced is the LoveACE backend server (M1 skeleton).
//
// Development: run without environment and it uses ephemeral keys plus an
// in-memory store (data does not survive restarts). Production needs:
//
//	LOVEACED_DATABASE_URL   postgres://... (migrations apply automatically)
//	LOVEACED_KEK_HEX        32-byte hex master key for the credential vault
//	LOVEACED_PEPPER_HEX     32-byte hex pepper for account/fingerprint HMACs
//	LOVEACED_JWT_SEED_HEX   32-byte hex Ed25519 seed for access tokens
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/captcha"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/api"
	"github.com/Sibuxiangx/loveace-go/server/internal/captchastore"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/stats"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/pg"
	"github.com/Sibuxiangx/loveace-go/server/internal/teachereval"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loveaced:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	var st store.Store
	if cfg.DatabaseURL != "" {
		pgs, err := pg.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		st = pgs
	} else {
		fmt.Fprintln(os.Stderr, "loveaced: LOVEACED_DATABASE_URL not set; using the in-memory store (dev only)")
		st = mem.New()
	}
	defer st.Close()

	v, err := vault.New(cfg.KEK, cfg.KEKVersion)
	if err != nil {
		return err
	}
	tm := token.NewManager(cfg.JWTSeed, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	// proxyURL empty = direct egress (the tests); non-empty = the session
	// rides that proxy exit for its whole life (the EC gateway binds a
	// TWFID to its birth IP).
	trFactory := func() transport.Transport {
		return transport.NewHTTPTransport(8)
	}
	// Passive health + usage statistics share the event stream: one
	// fan-out sink per user (§9/§10/§11.4).
	reg := health.New()
	usage := stats.New()
	fan := func(userID string) events.Sink {
		return events.Multi{reg.SinkFor(userID), usage.SinkFor(userID)}
	}
	// 验证码原生化(M1/M3):模型在场 → 撞验证码自动"取图→解→提交";
	// 沉淀目录在场 → 每次有结论的提交(金标/困难负样本)落 zstd 日切。
	// 二者缺失都只降级,绝不阻断启动。
	var captchaOpts []school.Option
	if model := cfg.CaptchaModelPath; model != "" {
		if solver, err := captcha.New(model, ""); err != nil {
			log.Printf("loveaced: captcha solver unavailable (%v); falling back to interactive challenges", err)
		} else {
			captchaOpts = append(captchaOpts, school.WithCaptchaSolver(solver))
			fmt.Fprintf(os.Stderr, "loveaced: captcha solver ready (%s)\n", model)
		}
	}
	if dir := cfg.CaptchaStoreDir; dir != "" {
		cs, err := captchastore.Open(dir)
		if err != nil {
			log.Printf("loveaced: captcha store unavailable (%v)", err)
		} else {
			defer cs.Close()
			captchaOpts = append(captchaOpts, school.WithCaptchaRecorder(cs))
			fmt.Fprintf(os.Stderr, "loveaced: captcha store at %s\n", dir)
		}
	}
	smOpts := []school.Option{
		school.WithGroupSize(cfg.SessionGroupSize),
		school.WithSinkFactory(fan),
		school.WithSnapshotStore(&sealedUserSnapshots{st: st, v: v}),
		school.WithCredentialResolver(vaultCredentialResolver{st: st, v: v}.Resolve),
		school.WithInvalidCredentialsHook(func(ctx context.Context, userID string) {
			_ = st.SetCredentialState(ctx, userID, store.CredInvalid)
			_ = st.AppendAudit(ctx, store.AuditEvent{
				At: time.Now(), UserID: userID, Event: "relogin.invalid_credentials",
			})
		}),
	}
	smOpts = append(smOpts, captchaOpts...)
	sm := school.NewManager(trFactory, smOpts...)
	// Session-internal debug traces (the mint walk, recovery steps) —
	// hosts and statuses only, never cookies or tickets.
	session.DebugHook = func(format string, args ...any) {
		log.Printf(format, args...)
	}
	// §9.1 janitor: cold evictions + dormant snapshot deletion.
	sm.StartCooling(ctx, time.Minute)

	// M5 janitor: idempotency records older than a day leave the store.
	// The same loop prunes rotated refresh-token rows older than 30 days
	// (reuse detection stays available for the tokens' whole lifetime).
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = st.CleanupIdempotency(ctx, time.Now().Add(-24*time.Hour))
				_ = st.PruneRotatedRefreshTokens(ctx, time.Now().Add(-30*24*time.Hour))
			}
		}
	}()

	// §9: zero-credential L0 probing — only while users are active,
	// fully stopped when idle. The probe is a plain GET of the gateway
	// auth page: no account, no submission, no failure budget.
	go reg.RunProber(ctx, gatewayProbe, time.Minute, 10*time.Minute)

	teachPool := teachereval.New(teachereval.Config{Enabled: cfg.TeacherEvalEnabled}, st, teachereval.SchoolFactory{School: sm}, nil)
	if err := teachPool.Start(ctx); err != nil {
		return fmt.Errorf("teacher eval recovery: %w", err)
	}
	defer teachPool.Shutdown()

	srv := api.New(api.Deps{
		Cfg:    cfg,
		Store:  st,
		Vault:  v,
		Tokens: tm,
		School: sm,
		Health: reg,
		Stats:  usage,
		Pepper: cfg.Pepper,
		Teach:  teachPool,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      150 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()
	fmt.Fprintf(os.Stderr, "loveaced: listening on %s (dev=%v)\n", cfg.Addr, cfg.DevMode)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sig:
		fmt.Fprintln(os.Stderr, "loveaced: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}
