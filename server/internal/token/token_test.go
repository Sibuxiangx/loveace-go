package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(priv, 15*time.Minute, 30*24*time.Hour)
}

func TestAccessTokenRoundtrip(t *testing.T) {
	m := testManager(t)
	now := time.Now()
	signed, exp, err := m.NewAccessToken("u1", "d1", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.VerifyAccessToken(signed)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "u1" || claims.DeviceID != "d1" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.Exp.Unix() != exp.Unix() { // JWT exp is second-precision
		t.Fatalf("exp mismatch: %v vs %v", claims.Exp, exp)
	}
}

func TestAccessTokenRejectsTamperedAndExpired(t *testing.T) {
	m := testManager(t)
	now := time.Now()
	signed, _, err := m.NewAccessToken("u1", "d1", now.Add(-time.Hour)) // exp in the past
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.VerifyAccessToken(signed); err == nil {
		t.Fatal("expired token accepted")
	}
	// Tampered payload.
	signed2, _, _ := m.NewAccessToken("u1", "d1", now)
	if _, err := m.VerifyAccessToken(signed2 + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestRefreshTokensUniqueAndHashed(t *testing.T) {
	s1, h1, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	s2, h2, _ := NewRefreshToken()
	if s1 == s2 || h1 == h2 {
		t.Fatal("refresh secrets are not unique")
	}
	if HashRefreshToken(s1) != h1 {
		t.Fatal("hash mismatch")
	}
	if s1 == h1 {
		t.Fatal("hash equals the secret")
	}
}
