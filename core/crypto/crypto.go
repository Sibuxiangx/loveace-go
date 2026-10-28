// Package crypto ports the cryptography used by the reference clients.
//
//   - EC gateway login: RSA PKCS#1 v1.5 encryption of "{password}_{csrf}"
//     where the modulus is hex and the exponent decimal, output lowercase hex.
//   - UAAP CAS login: Triple-DES ECB with PKCS#7 padding, then Base64.
//   - MD5 is used for non-reversible account-fingerprint hashing only.
package crypto

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"

	"crypto/des"

	"github.com/Sibuxiangx/loveace-go/core/errs"
)

// RSAEncryptPKCS1v15 RSA-encrypts plaintext with the gateway public key and
// returns lowercase hex.
//
// Mirrors AUFEConnection._rsaEncrypt (PKCS1Encoding + RSAEngine) and
// CryptoHelper.rsaEncrypt (SecKeyCreateEncryptedData with .rsaEncryptionPKCS1).
//
// crypto/rsa.EncryptPKCS1v15 is deliberately NOT used: since Go 1.24 it
// rejects moduli smaller than 1024 bits and is deprecated. The padding is
// built by hand (type 2: 0x00 0x02 || PS || 0x00 || message) and encrypted
// with big.Int modular exponentiation, matching the reference behaviour for
// any modulus size.
func RSAEncryptPKCS1v15(plaintext, modulusHex, exponentDecimal string) (string, error) {
	modulusBytes, err := hex.DecodeString(trimSpace(modulusHex))
	if err != nil {
		return "", errs.NewParse(fmt.Sprintf("invalid RSA modulus hex: %v", err))
	}
	modulus := new(big.Int).SetBytes(modulusBytes)
	exponent, ok := new(big.Int).SetString(trimSpace(exponentDecimal), 10)
	if !ok {
		return "", errs.NewParse("invalid RSA exponent: " + exponentDecimal)
	}
	if modulus.Sign() <= 0 || exponent.Sign() < 0 {
		return "", errs.NewParse("invalid RSA public key")
	}

	k := len(modulusBytes)
	if k < 11 {
		return "", errs.NewParse("invalid RSA public key: modulus too small")
	}
	// EM = 0x00 || 0x02 || PS || 0x00 || M
	em := make([]byte, k)
	em[0] = 0x00
	em[1] = 0x02
	psLen := k - len(plaintext) - 3
	if psLen < 8 {
		return "", errs.NewParse("RSA encryption failed: message too long")
	}
	ps := em[2 : 2+psLen]
	seen := make(map[byte]bool, psLen)
	for filled := 0; filled < psLen; {
		buf := make([]byte, psLen-filled)
		if _, err := rand.Read(buf); err != nil {
			return "", &errs.Error{Code: errs.Other, Msg: fmt.Sprintf("RSA encryption failed: %v", err)}
		}
		for _, b := range buf {
			if b == 0 || seen[b] {
				// Non-zero pseudo-random octets must differ; retry on
				// duplicates too (tiny key sizes make collisions plausible).
				continue
			}
			seen[b] = true
			ps[filled] = b
			filled++
			if filled == psLen {
				break
			}
		}
	}
	em[2+psLen] = 0x00
	copy(em[3+psLen:], plaintext)

	m := new(big.Int).SetBytes(em)
	c := new(big.Int).Exp(m, exponent, modulus)
	// Output length equals the modulus length, zero-padded on the left.
	out := make([]byte, k)
	c.FillBytes(out)
	return hex.EncodeToString(out), nil
}

// DESEde3ECBPKCS7Base64 encrypts plaintext with Triple-DES (EDE3) ECB +
// PKCS#7 padding, Base64 encoded.
//
// Mirrors AUFEConnection._desEncrypt: the supplied key is truncated to 8
// bytes (or zero-padded), then repeated three times to form the 24-byte key.
func DESEde3ECBPKCS7Base64(plaintext, key string) (string, error) {
	keyBytes := []byte(key)
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
		return "", &errs.Error{Code: errs.Other, Msg: fmt.Sprintf("invalid 3DES key: %v", err)}
	}

	data := []byte(plaintext)
	pad := 8 - len(data)%8
	padded := make([]byte, len(data)+pad)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(pad)
	}

	buf := make([]byte, 8)
	dst := make([]byte, len(padded))
	for off := 0; off < len(padded); off += 8 {
		copy(buf, padded[off:off+8])
		block.Encrypt(dst[off:off+8], buf)
	}
	return base64.StdEncoding.EncodeToString(dst), nil
}

// MD5Hex returns the lowercase hex MD5 digest.
func MD5Hex(input string) string {
	sum := md5.Sum([]byte(input))
	return hex.EncodeToString(sum[:])
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
