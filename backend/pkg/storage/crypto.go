package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// blobVersion is the first byte of every sealed blob and of its additional authenticated data.
const blobVersion byte = 0x01

var (
	// ErrConnectionsUnreadable is returned when a sealed value cannot be decrypted or decoded.
	ErrConnectionsUnreadable = errors.New("connections unreadable")
	// ErrInvalidUserID is returned when a user id is not a canonical uuid.
	ErrInvalidUserID = errors.New("invalid user id")
)

// Sealer encrypts client-selected values with AES-256-GCM, bound to the owning user id.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer creates a Sealer for the given 256-bit key.
func NewSealer(key [32]byte) (*Sealer, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// sealListed returns a copy of data with listed, present values sealed in place.
func (s *Sealer) sealListed(userID string, data map[string]json.RawMessage, encrypt []string) (map[string]json.RawMessage, error) {
	aad, err := additionalData(userID)
	if err != nil {
		return nil, err
	}
	sealed := maps.Clone(data)
	for key, value := range data {
		if !slices.Contains(encrypt, key) {
			continue
		}
		blob, err := s.seal(value, aad)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(map[string]string{"$sealed": base64.StdEncoding.EncodeToString(blob)})
		if err != nil {
			return nil, fmt.Errorf("encode sealed value: %w", err)
		}
		sealed[key] = raw
	}
	return sealed, nil
}

func (s *Sealer) seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	blob := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	blob = append(blob, blobVersion)
	blob = append(blob, nonce...)
	return s.aead.Seal(blob, nonce, plaintext, aad), nil
}

// openAll opens sealed top-level values. unreadable values are omitted and logged without content.
func (s *Sealer) openAll(userID string, data map[string]json.RawMessage, logger Logger) (map[string]json.RawMessage, error) {
	aad, err := additionalData(userID)
	if err != nil {
		return nil, err
	}
	opened := maps.Clone(data)
	for key, value := range data {
		if !isObject(value) {
			continue
		}
		obj, err := decodeObject(value)
		if err != nil {
			return nil, fmt.Errorf("decode stored object: %w", err)
		}
		raw, found := obj["$sealed"]
		if !found {
			continue
		}
		plaintext, err := s.open(raw, aad)
		if err != nil {
			delete(opened, key)
			logger.Printf("[WARN] omit unreadable sealed value for user %s key %q: %v", userID, key, err)
			continue
		}
		opened[key] = plaintext
	}
	return opened, nil
}

func (s *Sealer) open(raw json.RawMessage, aad []byte) (json.RawMessage, error) {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, ErrConnectionsUnreadable
	}
	blob, err := base64.StdEncoding.DecodeString(encoded)
	nonceSize := s.aead.NonceSize()
	if err != nil || len(blob) < 1+nonceSize+s.aead.Overhead() || blob[0] != blobVersion {
		return nil, ErrConnectionsUnreadable
	}
	nonce, ciphertext := blob[1:1+nonceSize], blob[1+nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil || !json.Valid(plaintext) {
		return nil, ErrConnectionsUnreadable
	}
	return plaintext, nil
}

// additionalData binds a blob to its owner: format version followed by the 16 raw uuid bytes.
func additionalData(userID string) ([]byte, error) {
	id, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	return append([]byte{blobVersion}, id[:]...), nil
}

// parseUUID decodes a canonical 8-4-4-4-12 uuid in any letter case.
func parseUUID(s string) ([16]byte, error) {
	var id [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, ErrInvalidUserID
	}
	digits := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if _, err := hex.Decode(id[:], []byte(digits)); err != nil {
		return id, ErrInvalidUserID
	}
	return id, nil
}

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unmarshal object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("not an object")
	}
	return obj, nil
}

// encodeObject marshals obj without html escaping, so stored values keep their original form.
func encodeObject(obj map[string]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, fmt.Errorf("marshal object: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
