// Package auth owns identity: sealed cookies, sessions, the Keycloak client and the auth handlers.
package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Purpose is the HKDF label a cookie key is derived with. every cookie kind has its own label,
// so a value sealed for one cookie never opens as another. bumping the version suffix revokes
// every value sealed under the old label.
type Purpose string

// cookie purposes.
const (
	PurposeLogin   Purpose = "lampa-login-v1"
	PurposeDevice  Purpose = "lampa-device-v1"
	PurposeSession Purpose = "lampa-session-v1"
)

// purposes lists every label a CookieSealer derives a key for.
var purposes = []Purpose{PurposeLogin, PurposeDevice, PurposeSession}

// sealVersion is the first byte of every sealed value.
const sealVersion byte = 0x01

// tagSize is the length of the cleartext purpose tag following the version byte.
const tagSize = 4

// headerSize is the cleartext prefix, version and purpose tag, authenticated as additional data.
const headerSize = 1 + tagSize

var (
	// ErrCookieInvalid is wrapped by every error returned when a sealed value cannot be opened.
	ErrCookieInvalid = errors.New("invalid cookie")
	// ErrCookieTruncated is returned when a value is too short to hold a sealed payload.
	ErrCookieTruncated = fmt.Errorf("%w: truncated", ErrCookieInvalid)
	// ErrCookieTampered is returned when a value is not valid base64, has an unknown version or fails authentication.
	ErrCookieTampered = fmt.Errorf("%w: tampered", ErrCookieInvalid)
	// ErrCookieWrongPurpose is returned when a value was sealed for another purpose.
	ErrCookieWrongPurpose = fmt.Errorf("%w: wrong purpose", ErrCookieInvalid)
	// ErrCookieExpired is returned when the expiry sealed inside a value has passed.
	ErrCookieExpired = fmt.Errorf("%w: expired", ErrCookieInvalid)
)

// CookieSealer seals JSON payloads into cookie values with AES-256-GCM, one key per Purpose,
// each derived from the data key with HKDF-SHA256. it is named apart from storage.Sealer,
// which seals user credentials at rest with the data key itself.
type CookieSealer struct {
	aeads map[Purpose]cipher.AEAD
}

// envelope is the sealed plaintext: the caller's payload and its expiry in unix seconds.
type envelope struct {
	ExpiresAt int64           `json:"exp"`
	Data      json.RawMessage `json:"data"`
}

// NewCookieSealer derives a key for every known purpose from the 256-bit data key.
func NewCookieSealer(dataKey [32]byte) (*CookieSealer, error) {
	aeads := make(map[Purpose]cipher.AEAD, len(purposes))
	for _, purpose := range purposes {
		key, err := hkdf.Key(sha256.New, dataKey[:], nil, string(purpose), 32)
		if err != nil {
			return nil, fmt.Errorf("derive %s key: %w", purpose, err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("create %s cipher: %w", purpose, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create %s gcm: %w", purpose, err)
		}
		aeads[purpose] = aead
	}
	return &CookieSealer{aeads: aeads}, nil
}

// Seal encodes payload as JSON and seals it for purpose, valid until expiresAt.
// the result is unpadded base64url, safe as a cookie value.
func (s *CookieSealer) Seal(purpose Purpose, payload any, expiresAt time.Time) (string, error) {
	aead, ok := s.aeads[purpose]
	if !ok {
		return "", fmt.Errorf("unknown purpose %q", purpose)
	}
	if expiresAt.IsZero() {
		return "", errors.New("expiry is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	plaintext, err := json.Marshal(envelope{ExpiresAt: expiresAt.Unix(), Data: data})
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}

	header := purposeHeader(purpose)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := make([]byte, 0, headerSize+len(nonce)+len(plaintext)+aead.Overhead())
	sealed = append(sealed, header...)
	sealed = append(sealed, nonce...)
	sealed = aead.Seal(sealed, nonce, plaintext, header)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open verifies value was sealed for purpose and has not expired at now, then decodes its payload into out.
// failures wrap ErrCookieInvalid through ErrCookieTruncated, ErrCookieTampered, ErrCookieWrongPurpose
// or ErrCookieExpired. error messages never contain the value or the payload.
func (s *CookieSealer) Open(purpose Purpose, value string, now time.Time, out any) error {
	aead, ok := s.aeads[purpose]
	if !ok {
		return fmt.Errorf("unknown purpose %q", purpose)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return fmt.Errorf("decode base64: %w", ErrCookieTampered)
	}
	nonceSize := aead.NonceSize()
	if len(sealed) < headerSize+nonceSize+aead.Overhead() {
		return fmt.Errorf("%d bytes: %w", len(sealed), ErrCookieTruncated)
	}
	if sealed[0] != sealVersion {
		return fmt.Errorf("unknown version: %w", ErrCookieTampered)
	}
	header := sealed[:headerSize]
	if !bytes.Equal(header, purposeHeader(purpose)) {
		return fmt.Errorf("expected %s: %w", purpose, ErrCookieWrongPurpose)
	}
	nonce, ciphertext := sealed[headerSize:headerSize+nonceSize], sealed[headerSize+nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return fmt.Errorf("open: %w", ErrCookieTampered)
	}

	var env envelope
	if err := json.Unmarshal(plaintext, &env); err != nil || env.ExpiresAt == 0 {
		return fmt.Errorf("decode envelope: %w", ErrCookieTampered)
	}
	if !now.Before(time.Unix(env.ExpiresAt, 0)) {
		return ErrCookieExpired
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	return nil
}

// purposeHeader is the version byte followed by the first tagSize bytes of SHA-256(purpose).
// the tag only tells a wrong-purpose value apart from a tampered one; the key separation itself
// comes from HKDF, and the header is authenticated as additional data.
func purposeHeader(purpose Purpose) []byte {
	sum := sha256.Sum256([]byte(purpose))
	return append([]byte{sealVersion}, sum[:tagSize]...)
}
