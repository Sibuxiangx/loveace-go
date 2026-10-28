// Package vault implements the school-credential vault: reversible
// envelope encryption (DESIGN.md §6).
//
// Passwords must be replayed to the school, so a hash cannot replace
// storage. Each user gets a random 256-bit data-encryption key (DEK); the
// DEK encrypts the credential with AES-256-GCM (AAD binds the user id), and
// the key-encryption key (KEK) wraps the DEK the same way. The KEK comes
// from a KeyProvider; swapping in a KMS later only changes the provider.
//
// A separate HMAC(pepper, account‖password) fingerprint answers "has the
// school already rejected this exact password?" without touching the vault.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Stored is the at-rest representation of one credential field.
type Stored struct {
	Version    string `json:"version"`     // KEK version used to wrap the DEK
	WrappedDEK []byte `json:"wrapped_dek"` // KEK(DEK), AES-256-GCM
	Nonce      []byte `json:"nonce"`       // data-layer GCM nonce
	Ciphertext []byte `json:"ciphertext"`  // DEK(plaintext)
}

// ErrWrongUser is returned when a Stored blob is decrypted for a different
// user id than it was created for (AAD mismatch).
var ErrWrongUser = errors.New("vault: credential does not belong to this user")

// CredPayload is the plaintext of one user's credential blob: the school
// account number plus both passwords, sealed together (§6).
type CredPayload struct {
	Account         string `json:"account"`
	Password        string `json:"password"`
	GatewayPassword string `json:"gateway_password"` // empty = same as password
}

// Vault encrypts and decrypts credentials under one KEK.
type Vault struct {
	kek     []byte
	version string
}

// New builds a Vault from raw key material. The KEK must be 32 bytes.
func New(kek []byte, version string) (*Vault, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("vault: KEK must be 32 bytes, got %d", len(kek))
	}
	if version == "" {
		version = "v1"
	}
	return &Vault{kek: kek, version: version}, nil
}

// Version reports the KEK version used for new encryptions.
func (v *Vault) Version() string { return v.version }

// Encrypt seals plaintext for userID. Each call uses a fresh DEK and fresh
// nonces at both layers.
func (v *Vault) Encrypt(userID string, plaintext []byte) (Stored, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return Stored{}, fmt.Errorf("vault: dek: %w", err)
	}
	defer wipe(dek)

	wrapped, err := v.gcmSeal(v.kek, dek, dekAAD(v.version, userID))
	if err != nil {
		return Stored{}, err
	}
	data, err := v.gcmSeal(dek, plaintext, dataAAD(v.version, userID))
	if err != nil {
		return Stored{}, err
	}
	return Stored{
		Version:    v.version,
		WrappedDEK: wrapped.nonceCiphertext(),
		Nonce:      data.nonce,
		Ciphertext: data.body,
	}, nil
}

// Decrypt opens a Stored blob. It fails for blobs wrapped under a different
// user (AAD mismatch) or a different KEK.
func (v *Vault) Decrypt(userID string, s Stored) ([]byte, error) {
	if s.Version != v.version {
		return nil, fmt.Errorf("vault: unknown key version %q (have %q)", s.Version, v.version)
	}
	dek, err := v.gcmOpen(v.kek, s.WrappedDEK, dekAAD(v.version, userID))
	if err != nil {
		return nil, err
	}
	defer wipe(dek)
	return v.gcmOpen(dek, append(append([]byte{}, s.Nonce...), s.Ciphertext...), dataAAD(v.version, userID))
}

// Fingerprint computes HMAC(pepper, account‖password). It answers "has the
// school already rejected this exact pair" (§5.2) and is not reversible.
func Fingerprint(pepper []byte, account, password string) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(account))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

// AccountHash derives the keyed lookup hash for the school account number.
// The plaintext account number is never a column; it lives only inside the
// encrypted credential blob.
func AccountHash(pepper []byte, account string) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte("account:"))
	mac.Write([]byte(account))
	return hex.EncodeToString(mac.Sum(nil))
}

type sealed struct {
	nonce []byte
	body  []byte
}

func (s sealed) nonceCiphertext() []byte {
	return append(append([]byte{}, s.nonce...), s.body...)
}

func (v *Vault) gcmSeal(key, plaintext []byte, aad string) (sealed, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return sealed{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return sealed{}, fmt.Errorf("vault: nonce: %w", err)
	}
	body := gcm.Seal(nil, nonce, plaintext, []byte(aad))
	return sealed{nonce: nonce, body: body}, nil
}

func (v *Vault) gcmOpen(key, nonceCiphertext []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonceCiphertext) < gcm.NonceSize() {
		return nil, errors.New("vault: ciphertext too short")
	}
	nonce, body := nonceCiphertext[:gcm.NonceSize()], nonceCiphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, []byte(aad))
	if err != nil {
		return nil, ErrWrongUser
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm: %w", err)
	}
	return gcm, nil
}

func dekAAD(version, userID string) string  { return "loveace:" + version + ":dek:" + userID }
func dataAAD(version, userID string) string { return "loveace:" + version + ":cred:" + userID }

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
