package config

import (
	"net"
	"strings"
	"testing"
)

// Throwaway fixtures: deterministic, never used outside tests.
const (
	testKEK    = "1111111111111111111111111111111111111111111111111111111111111111"
	testPepper = "2222222222222222222222222222222222222222222222222222222222222222"
	testSeed   = "3333333333333333333333333333333333333333333333333333333333333333"
	testDBURL  = "postgres://loveace@db:5432/loveace?sslmode=disable"
)

// clearEnv makes every variable this package reads explicit, so a developer's
// shell cannot leak into the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LOVEACED_KEK_HEX", "LOVEACED_PEPPER_HEX", "LOVEACED_JWT_SEED_HEX",
		"LOVEACED_DATABASE_URL", "LOVEACED_DEV", "LOVEACED_TRUSTED_PROXIES",
	} {
		t.Setenv(k, "")
	}
}

func setProduction(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("LOVEACED_KEK_HEX", testKEK)
	t.Setenv("LOVEACED_PEPPER_HEX", testPepper)
	t.Setenv("LOVEACED_JWT_SEED_HEX", testSeed)
	t.Setenv("LOVEACED_DATABASE_URL", testDBURL)
}

func TestLoadRefusesToStartWithoutKeysOrDatabase(t *testing.T) {
	clearEnv(t)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("empty environment: err = %v, want a refusal to start", err)
	}
}

func TestLoadRefusesMissingDatabaseEvenWithKeys(t *testing.T) {
	setProduction(t)
	t.Setenv("LOVEACED_DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("keys present but no database: boot must still refuse")
	}
}

func TestLoadDevOptInAllowsThrowawayRun(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOVEACED_DEV", "1")
	c, err := Load()
	if err != nil {
		t.Fatalf("LOVEACED_DEV=1: %v", err)
	}
	if !c.DevMode || len(c.KEK) != 32 || len(c.Pepper) != 32 || c.JWTSeed == nil {
		t.Fatalf("dev run should carry ephemeral keys and DevMode; got DevMode=%v", c.DevMode)
	}
}

func TestLoadProductionIsNotDevMode(t *testing.T) {
	setProduction(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DevMode {
		t.Fatal("complete production environment must not run in DevMode")
	}
}

func TestTrustedProxiesDefaultToLoopbackOnly(t *testing.T) {
	setProduction(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	trusted := func(ip string) bool {
		for _, n := range c.TrustedProxies {
			if n.Contains(net.ParseIP(ip)) {
				return true
			}
		}
		return false
	}
	if !trusted("127.0.0.1") || trusted("10.0.0.1") || trusted("203.0.113.9") {
		t.Fatalf("default trusted set must be loopback only; got %v", c.TrustedProxies)
	}
}

func TestTrustedProxiesRejectsNonCIDR(t *testing.T) {
	setProduction(t)
	t.Setenv("LOVEACED_TRUSTED_PROXIES", "127.0.0.1")
	if _, err := Load(); err == nil {
		t.Fatal("a bare address is not a CIDR and must be rejected")
	}
}
