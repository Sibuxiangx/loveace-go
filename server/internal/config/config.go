// Package config loads loveaced configuration from the environment.
//
// Required in production: LOVEACED_DATABASE_URL, LOVEACED_KEK_HEX,
// LOVEACED_PEPPER_HEX and LOVEACED_JWT_SEED_HEX. Boot refuses to start
// without them. LOVEACED_DEV=1 opts into a throwaway local run instead:
// ephemeral keys and/or the in-memory store, with warnings logged to stderr.
package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the loveaced runtime configuration.
type Config struct {
	Addr        string
	DatabaseURL string
	KEK         []byte
	KEKVersion  string
	Pepper      []byte
	JWTSeed     ed25519.PrivateKey

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	PasswordMaxLen  int

	// SessionGroupSize caps live UAAP sessions per school account (§7.5).
	SessionGroupSize int

	// Captcha 原生化:LOVEACED_CAPTCHA_MODEL 指向 beta 的 common.onnx 时
	// 启用撞验证码的自动解环(缺失 = 旧行为,挑战回退人工);
	// LOVEACED_CAPTCHA_STORE 指向判卷结果沉淀目录(空 = 不沉淀)。
	CaptchaModelPath string
	CaptchaStoreDir  string

	// TeacherEvalEnabled turns on background auto-evaluation
	// (LOVEACED_TEACHER_EVAL_ENABLED=1). Off by default: the feature stays
	// unavailable until the operator enables it.
	TeacherEvalEnabled bool

	// AdminToken guards the admin endpoints (e.g. GET /v1/admin/sessions).
	// Empty disables them entirely (404), never an open endpoint.
	AdminToken string

	// CORSOrigins are the exact browser origins allowed to call the API
	// cross-origin (LOVEACED_CORS_ORIGINS, comma-separated). Empty
	// disables CORS entirely (same-origin deployments need nothing).
	CORSOrigins []string

	// TrustedProxies are the peers whose X-Forwarded-For header is honoured
	// when deriving the client address (LOVEACED_TRUSTED_PROXIES, CIDR list).
	// Default: loopback only (the host reverse proxy). Headers from any other
	// peer are ignored, so a client cannot spoof its address for rate limits.
	TrustedProxies []*net.IPNet

	// DevMode is true for an explicit throwaway run (LOVEACED_DEV=1) that
	// uses ephemeral keys and/or the in-memory store.
	DevMode bool
}

// Load reads the environment and fills defaults. Missing key material or a
// missing database is fatal unless LOVEACED_DEV=1 is set.
func Load() (Config, error) {
	c := Config{
		Addr:               env("LOVEACED_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("LOVEACED_DATABASE_URL"),
		KEKVersion:         env("LOVEACED_KEK_VERSION", "v1"),
		AccessTokenTTL:     15 * time.Minute,
		RefreshTokenTTL:    30 * 24 * time.Hour,
		PasswordMaxLen:     64,
		SessionGroupSize:   5,
		CaptchaModelPath:   os.Getenv("LOVEACED_CAPTCHA_MODEL"),
		CaptchaStoreDir:    os.Getenv("LOVEACED_CAPTCHA_STORE"),
		TeacherEvalEnabled: os.Getenv("LOVEACED_TEACHER_EVAL_ENABLED") == "1",
	}
	devRequested := os.Getenv("LOVEACED_DEV") == "1"

	var missing []string
	if kek, err := hexBytes("LOVEACED_KEK_HEX", 32); err == nil {
		c.KEK = kek
	} else {
		missing = append(missing, "LOVEACED_KEK_HEX ("+err.Error()+")")
	}
	if pepper, err := hexBytes("LOVEACED_PEPPER_HEX", 32); err == nil {
		c.Pepper = pepper
	} else {
		missing = append(missing, "LOVEACED_PEPPER_HEX ("+err.Error()+")")
	}
	if seed, err := hexBytes("LOVEACED_JWT_SEED_HEX", 32); err == nil {
		c.JWTSeed = ed25519.NewKeyFromSeed(seed)
	} else {
		missing = append(missing, "LOVEACED_JWT_SEED_HEX ("+err.Error()+")")
	}

	// Fail closed: a production boot with missing keys would silently
	// discard credentials and tokens on restart, so it must not start.
	if len(missing) > 0 && !devRequested {
		return c, fmt.Errorf("refusing to start without key material: %s (LOVEACED_DEV=1 allows a throwaway run)",
			strings.Join(missing, "; "))
	}
	if c.DatabaseURL == "" && !devRequested {
		return c, fmt.Errorf("LOVEACED_DATABASE_URL is required (LOVEACED_DEV=1 allows an in-memory throwaway run)")
	}

	if len(missing) > 0 {
		if c.KEK == nil {
			c.KEK = randomBytes(32)
		}
		if c.Pepper == nil {
			c.Pepper = randomBytes(32)
		}
		if c.JWTSeed == nil {
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return c, fmt.Errorf("generate ephemeral jwt key: %w", err)
			}
			c.JWTSeed = priv
		}
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "loveaced: missing %s\n", m)
		}
	}
	c.DevMode = len(missing) > 0 || c.DatabaseURL == ""
	if c.DevMode {
		fmt.Fprintln(os.Stderr, "loveaced: DEV MODE (LOVEACED_DEV=1) — ephemeral keys and/or in-memory store; "+
			"stored credentials and tokens will NOT survive a restart; do not use in production")
	}

	if ttl := os.Getenv("LOVEACED_ACCESS_TTL_MINUTES"); ttl != "" {
		if minutes, err := strconv.Atoi(ttl); err == nil && minutes > 0 {
			c.AccessTokenTTL = time.Duration(minutes) * time.Minute
		}
	}
	if n := os.Getenv("LOVEACED_SESSION_GROUP_SIZE"); n != "" {
		if size, err := strconv.Atoi(n); err == nil && size > 0 {
			c.SessionGroupSize = size
		}
	}
	c.AdminToken = os.Getenv("LOVEACED_ADMIN_TOKEN")
	if origins := os.Getenv("LOVEACED_CORS_ORIGINS"); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.CORSOrigins = append(c.CORSOrigins, o)
			}
		}
	}
	trusted, err := parseCIDRs(env("LOVEACED_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"))
	if err != nil {
		return c, err
	}
	c.TrustedProxies = trusted
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hexBytes(key string, want int) ([]byte, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return nil, fmt.Errorf("not set")
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("not hex")
	}
	if len(b) != want {
		return nil, fmt.Errorf("want %d bytes, got %d", want, len(b))
	}
	return b, nil
}

func parseCIDRs(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("LOVEACED_TRUSTED_PROXIES: %q is not a CIDR: %w", raw, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return b
}
