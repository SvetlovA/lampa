package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// SensitiveSettings lists the settings keys holding connection credentials. they are never stored
// in the data column, Sealer moves them into an encrypted blob.
var SensitiveSettings = []string{"torrserver_login", "torrserver_password", "jackett_key", "jackett_key_two"}

// blobVersion is the first byte of every sealed blob and of its additional authenticated data.
const blobVersion byte = 0x01

const settingsSection = "settings"

var (
	// ErrConnectionsUnreadable is returned when a sealed blob cannot be decrypted or decoded.
	ErrConnectionsUnreadable = errors.New("connections unreadable")
	// ErrInvalidUserID is returned when a user id is not a canonical uuid.
	ErrInvalidUserID = errors.New("invalid user id")
)

// Sealer encrypts connection credentials with AES-256-GCM, bound to the owning user id.
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

// Split moves the sensitive settings keys of doc into a sealed blob and returns the document without them.
// blob is nil when doc has none of them. doc itself is not modified.
func (s *Sealer) Split(userID string, doc Document) (clean Document, blob []byte, err error) {
	aad, err := additionalData(userID)
	if err != nil {
		return Document{}, nil, err
	}
	raw, ok := doc.Data[settingsSection]
	if !ok {
		return doc, nil, nil
	}
	settings, err := decodeObject(raw)
	if err != nil {
		return Document{}, nil, fmt.Errorf("decode settings: %w", err)
	}

	sensitive := map[string]json.RawMessage{}
	for _, key := range SensitiveSettings {
		if value, found := settings[key]; found {
			sensitive[key] = value
			delete(settings, key)
		}
	}
	if len(sensitive) == 0 {
		return doc, nil, nil
	}

	plaintext, err := encodeObject(sensitive)
	if err != nil {
		return Document{}, nil, fmt.Errorf("encode connections: %w", err)
	}
	cleanSettings, err := encodeObject(settings)
	if err != nil {
		return Document{}, nil, fmt.Errorf("encode settings: %w", err)
	}

	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Document{}, nil, fmt.Errorf("generate nonce: %w", err)
	}
	blob = make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	blob = append(blob, blobVersion)
	blob = append(blob, nonce...)
	blob = s.aead.Seal(blob, nonce, plaintext, aad)

	return withSection(doc, settingsSection, cleanSettings), blob, nil
}

// Merge decrypts blob and puts the connection credentials back into the settings of doc.
// a nil blob means the user has no stored credentials and returns doc unchanged.
// any blob that cannot be opened for userID fails with ErrConnectionsUnreadable.
func (s *Sealer) Merge(userID string, doc Document, blob []byte) (Document, error) {
	aad, err := additionalData(userID)
	if err != nil {
		return Document{}, err
	}
	if blob == nil {
		return doc, nil
	}

	nonceSize := s.aead.NonceSize()
	if len(blob) < 1+nonceSize+s.aead.Overhead() {
		return Document{}, fmt.Errorf("blob too short: %w", ErrConnectionsUnreadable)
	}
	if blob[0] != blobVersion {
		return Document{}, fmt.Errorf("unknown blob version: %w", ErrConnectionsUnreadable)
	}
	nonce, ciphertext := blob[1:1+nonceSize], blob[1+nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return Document{}, fmt.Errorf("open blob: %w", ErrConnectionsUnreadable)
	}
	sensitive, err := decodeObject(plaintext)
	if err != nil {
		return Document{}, fmt.Errorf("decode connections: %w", ErrConnectionsUnreadable)
	}

	settings := map[string]json.RawMessage{}
	if raw, ok := doc.Data[settingsSection]; ok {
		if settings, err = decodeObject(raw); err != nil {
			return Document{}, fmt.Errorf("decode settings: %w", err)
		}
	}
	for key, value := range sensitive {
		if slices.Contains(SensitiveSettings, key) {
			settings[key] = value
		}
	}
	merged, err := encodeObject(settings)
	if err != nil {
		return Document{}, fmt.Errorf("encode settings: %w", err)
	}
	return withSection(doc, settingsSection, merged), nil
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

// withSection returns a copy of doc whose data has section replaced by raw.
func withSection(doc Document, section string, raw []byte) Document {
	data := make(map[string]json.RawMessage, len(doc.Data))
	maps.Copy(data, doc.Data)
	data[section] = raw
	doc.Data = data
	return doc
}
