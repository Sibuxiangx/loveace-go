package vault

import (
	"bytes"
	"testing"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	v, err := New(bytes.Repeat([]byte{0x42}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	v := testVault(t)
	blob, err := v.Encrypt("user-1", []byte("secret-password"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Decrypt("user-1", blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "secret-password" {
		t.Fatalf("roundtrip = %q", got)
	}
}

func TestDecryptWrongUserFails(t *testing.T) {
	v := testVault(t)
	blob, err := v.Encrypt("user-1", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Decrypt("user-2", blob); err != ErrWrongUser {
		t.Fatalf("err = %v, want ErrWrongUser", err)
	}
}

func TestCiphertextIsRandomized(t *testing.T) {
	v := testVault(t)
	a, _ := v.Encrypt("user-1", []byte("same"))
	b, _ := v.Encrypt("user-1", []byte("same"))
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Fatal("two encryptions of the same plaintext are identical; nonces/DEKs are not random")
	}
}

func TestFingerprintStableAndDistinct(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	a := Fingerprint(pepper, "20000003", "pw1")
	b := Fingerprint(pepper, "20000003", "pw1")
	c := Fingerprint(pepper, "20000003", "pw2")
	d := Fingerprint(pepper, "20000007", "pw1")
	if a != b {
		t.Fatal("fingerprint not stable")
	}
	if a == c || a == d {
		t.Fatal("fingerprint collides on different input")
	}
}

func TestAccountHashDistinctFromPassword(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	if AccountHash(pepper, "20000003") == Fingerprint(pepper, "20000003", "") {
		t.Fatal("account hash and fingerprint share a domain")
	}
}
