package crypto

import (
	"crypto/des"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
)

func TestMD5MatchesKnownVectors(t *testing.T) {
	if got := MD5Hex(""); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("MD5Hex(\"\") = %s", got)
	}
	if got := MD5Hex("abc"); got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("MD5Hex(abc) = %s", got)
	}
}

func TestDESEde3IsDeterministicAndPadded(t *testing.T) {
	a, err := DESEde3ECBPKCS7Base64("secret", "ltvalue1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DESEde3ECBPKCS7Base64("secret", "ltvalue1")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("3DES ECB must be deterministic")
	}
	// 6 bytes plaintext -> 8 bytes ciphertext -> 12 base64 chars.
	if len(a) != 12 {
		t.Fatalf("ciphertext base64 length = %d, want 12", len(a))
	}
}

func TestDESEde3DecryptsRoundTrip(t *testing.T) {
	// Cross-check: decrypt our own ciphertext with an independent ECB+PKCS7
	// decryptor to prove the padding and block order are standard.
	msg := "unified-auth-password-2026"
	ct, err := DESEde3ECBPKCS7Base64(msg, "LT-abc123")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := []byte("LT-abc123")
	if len(keyBytes) > 8 {
		keyBytes = keyBytes[:8]
	}
	for len(keyBytes) < 8 {
		keyBytes = append(keyBytes, 0)
	}
	triple := make([]byte, 0, 24)
	for i := 0; i < 3; i++ {
		triple = append(triple, keyBytes...)
	}
	block, err := des.NewTripleDESCipher(triple)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 0, len(raw))
	buf := make([]byte, 8)
	for off := 0; off < len(raw); off += 8 {
		block.Decrypt(buf, raw[off:off+8])
		plain = append(plain, buf...)
	}
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 8 {
		t.Fatalf("invalid padding byte %d", pad)
	}
	plain = plain[:len(plain)-pad]
	if string(plain) != msg {
		t.Fatalf("round trip = %q, want %q", plain, msg)
	}
}

func TestRSARejectsBadExponent(t *testing.T) {
	_, err := RSAEncryptPKCS1v15("x", "0d", "not-a-number")
	var e *errs.Error
	if err == nil || !asErr(err, &e) || e.Code != errs.Parse {
		t.Fatalf("want parse error, got %v", err)
	}
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
	}
	return ok
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestRSAEncryptMatchesStdlibDecryption(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	modulus := hex.EncodeToString(key.PublicKey.N.Bytes())
	exponent := itoa(key.PublicKey.E)

	got, err := RSAEncryptPKCS1v15("gateway_pw_csrf", modulus, exponent)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rsa.DecryptPKCS1v15(rand.Reader, key, raw)
	if err != nil {
		t.Fatalf("stdlib decrypt of our ciphertext failed: %v", err)
	}
	if string(plain) != "gateway_pw_csrf" {
		t.Fatalf("decrypted = %q", plain)
	}
}

func TestRSAEncryptOutputShape(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RSAEncryptPKCS1v15("x", hex.EncodeToString(key.PublicKey.N.Bytes()), itoa(key.PublicKey.E))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 256 {
		t.Fatalf("ciphertext hex length = %d, want 256 (128 bytes)", len(got))
	}
	if got != strings.ToLower(got) {
		t.Fatal("output must be lowercase hex")
	}
}

func TestRSAShortKeyStillAccepted(t *testing.T) {
	// The school gateway historically uses a 512-bit modulus. crypto/rsa
	// (Go >= 1.24) refuses it; our implementation must not.
	// Derive a syntactically valid 512-bit modulus from two 256-bit factors
	// (primality is irrelevant: we only exercise padding and size math).
	p, _ := new(big.Int).SetString(
		"e2212f5b7d4a3c8f9e1b2a3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f71", 16)
	q, _ := new(big.Int).SetString(
		"c9b1a3f5e7d9c1b3a5f7e9d1c3b5a7f9e2d4c6a8b0d2f4a6c8e0b2d4f6a8c0d3", 16)
	if p == nil || q == nil {
		t.Fatal("bad hex")
	}
	n := new(big.Int).Mul(p, q)
	got, err := RSAEncryptPKCS1v15("pw_csrf", hex.EncodeToString(n.Bytes()), "65537")
	if err != nil {
		t.Fatalf("512-bit modulus must be accepted: %v", err)
	}
	if len(got) != 128 {
		t.Fatalf("ciphertext hex length = %d, want 128", len(got))
	}
}

func TestRSAMessageTooLong(t *testing.T) {
	// rsa.GenerateKey refuses <1024-bit keys, so build a syntactically valid
	// short modulus from two 256-bit factors to exercise the size check.
	p, _ := new(big.Int).SetString(
		"e2212f5b7d4a3c8f9e1b2a3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f71", 16)
	q, _ := new(big.Int).SetString(
		"c9b1a3f5e7d9c1b3a5f7e9d1c3b5a7f9e2d4c6a8b0d2f4a6c8e0b2d4f6a8c0d3", 16)
	if p == nil || q == nil {
		t.Fatal("bad hex")
	}
	n := new(big.Int).Mul(p, q)
	long := strings.Repeat("x", 64) // exceeds 64-11 usable bytes for a 512-bit modulus
	if _, err := RSAEncryptPKCS1v15(long, hex.EncodeToString(n.Bytes()), "65537"); err == nil {
		t.Fatal("oversized message must fail")
	}
}
