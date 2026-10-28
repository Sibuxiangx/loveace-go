// Package token issues and verifies the server's own credentials:
// short-lived Ed25519-signed access tokens (JWT) and opaque rotating
// refresh tokens (DESIGN.md §5). School credentials never enter this
// package.
package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessClaims are the verified contents of an access token.
type AccessClaims struct {
	UserID   string
	DeviceID string
	Exp      time.Time
}

// Manager signs and verifies access tokens and mints refresh secrets.
type Manager struct {
	key        ed25519.PrivateKey
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewManager builds a Manager from an Ed25519 private key.
func NewManager(key ed25519.PrivateKey, accessTTL, refreshTTL time.Duration) *Manager {
	return &Manager{key: key, accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// AccessTTL reports the access-token lifetime.
func (m *Manager) AccessTTL() time.Duration { return m.accessTTL }

// RefreshTTL reports the refresh-token lifetime.
func (m *Manager) RefreshTTL() time.Duration { return m.refreshTTL }

// NewAccessToken signs a short-lived access token bound to one device.
func (m *Manager) NewAccessToken(userID, deviceID string, now time.Time) (string, time.Time, error) {
	exp := now.Add(m.accessTTL)
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": "loveaced",
		"sub": userID,
		"did": deviceID,
		"typ": "access",
		"iat": now.Unix(),
		"exp": exp.Unix(),
	})
	signed, err := t.SignedString(m.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: sign: %w", err)
	}
	return signed, exp, nil
}

// VerifyAccessToken checks signature, method and expiry.
func (m *Manager) VerifyAccessToken(s string) (AccessClaims, error) {
	parsed, err := jwt.Parse(s, func(t *jwt.Token) (any, error) {
		return m.key.Public(), nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return AccessClaims{}, fmt.Errorf("token: invalid access token")
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return AccessClaims{}, fmt.Errorf("token: unexpected claims")
	}
	sub, _ := mc["sub"].(string)
	did, _ := mc["did"].(string)
	if sub == "" || did == "" {
		return AccessClaims{}, fmt.Errorf("token: missing sub/did claim")
	}
	expTime, err := mc.GetExpirationTime()
	if err != nil || expTime == nil {
		return AccessClaims{}, fmt.Errorf("token: missing exp claim")
	}
	return AccessClaims{UserID: sub, DeviceID: did, Exp: expTime.Time}, nil
}

// NewRefreshToken mints a fresh opaque refresh secret and its at-rest hash.
// Only the hash is ever stored (DESIGN.md §5).
func NewRefreshToken() (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("token: refresh entropy: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	return secret, HashRefreshToken(secret), nil
}

// HashRefreshToken derives the at-rest hash of a refresh secret.
func HashRefreshToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
