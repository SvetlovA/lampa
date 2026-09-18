package storage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUserID  = "5f0c7a2e-3b1d-4c8a-9e6f-1a2b3c4d5e6f"
	otherUserID = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
)

func testKey(fill byte) [32]byte {
	var key [32]byte
	for i := range key {
		key[i] = fill
	}
	return key
}

func newTestSealer(t *testing.T, fill byte) *Sealer {
	t.Helper()
	s, err := NewSealer(testKey(fill))
	require.NoError(t, err)
	return s
}

func docWithSettings(t *testing.T, settings string) Document {
	t.Helper()
	doc, err := ParseDocument([]byte(`{"schema_version":1,"data":{"settings":`+settings+`,"favorites":{"a":1}}}`),
		Limits{MaxBodyBytes: 1 << 20, MaxSectionBytes: DefaultMaxSectionBytes, MaxDepth: DefaultMaxDepth})
	require.NoError(t, err)
	return doc
}

// sealedDoc returns a document with every sensitive key set and its sealed blob.
func sealedDoc(t *testing.T, s *Sealer) (clean Document, blob []byte) {
	t.Helper()
	doc := docWithSettings(t, `{"torrserver_login":"admin","torrserver_password":"s3cret-pass",`+
		`"jackett_key":"jk-one-value","jackett_key_two":"jk-two-value","language":"ru"}`)
	clean, blob, err := s.Split(testUserID, doc)
	require.NoError(t, err)
	require.NotNil(t, blob)
	return clean, blob
}

func settingsOf(t *testing.T, doc Document) map[string]any {
	t.Helper()
	var settings map[string]any
	require.NoError(t, json.Unmarshal(doc.Data["settings"], &settings))
	return settings
}

func TestSealer_RoundTrip(t *testing.T) {
	s := newTestSealer(t, 1)
	doc := docWithSettings(t, `{"torrserver_login":"admin","torrserver_password":"s3cret-pass",`+
		`"jackett_key":"jk-one-value","jackett_key_two":"jk-two-value","language":"ru","url":"<a&b>"}`)

	clean, blob, err := s.Split(testUserID, doc)
	require.NoError(t, err)
	require.NotNil(t, blob)
	assert.Equal(t, map[string]any{"language": "ru", "url": "<a&b>"}, settingsOf(t, clean))
	assert.Contains(t, string(clean.Data["settings"]), `"<a&b>"`, "html characters are not escaped")
	assert.JSONEq(t, `{"a":1}`, string(clean.Data["favorites"]))
	assert.Len(t, clean.Data, len(Sections))

	merged, err := s.Merge(testUserID, clean, blob)
	require.NoError(t, err)
	assert.Equal(t, settingsOf(t, doc), settingsOf(t, merged))
	assert.JSONEq(t, `{"a":1}`, string(merged.Data["favorites"]))
}

func TestSealer_Split(t *testing.T) {
	s := newTestSealer(t, 1)

	t.Run("no sensitive keys gives nil blob", func(t *testing.T) {
		doc := docWithSettings(t, `{"language":"ru"}`)
		clean, blob, err := s.Split(testUserID, doc)
		require.NoError(t, err)
		assert.Nil(t, blob)
		assert.Equal(t, doc, clean)
	})

	t.Run("missing settings section gives nil blob", func(t *testing.T) {
		doc := Document{SchemaVersion: 1, Data: map[string]json.RawMessage{"other": json.RawMessage(`{}`)}}
		clean, blob, err := s.Split(testUserID, doc)
		require.NoError(t, err)
		assert.Nil(t, blob)
		assert.Equal(t, doc, clean)
	})

	t.Run("clean data never contains plaintext values", func(t *testing.T) {
		clean, blob := sealedDoc(t, s)
		for _, raw := range clean.Data {
			for _, secret := range []string{"admin", "s3cret-pass", "jk-one-value", "jk-two-value"} {
				assert.NotContains(t, string(raw), secret)
			}
			for _, key := range SensitiveSettings {
				assert.NotContains(t, string(raw), key)
			}
		}
		assert.NotContains(t, string(blob), "s3cret-pass")
	})

	t.Run("input document is not modified", func(t *testing.T) {
		doc := docWithSettings(t, `{"jackett_key":"jk-one-value"}`)
		before := string(doc.Data["settings"])
		_, _, err := s.Split(testUserID, doc)
		require.NoError(t, err)
		assert.Equal(t, before, string(doc.Data["settings"]))
	})

	t.Run("fresh nonce per call", func(t *testing.T) {
		_, first := sealedDoc(t, s)
		_, second := sealedDoc(t, s)
		assert.NotEqual(t, first, second)
	})

	t.Run("blob layout", func(t *testing.T) {
		_, blob := sealedDoc(t, s)
		assert.Equal(t, blobVersion, blob[0])
	})

	t.Run("invalid user id", func(t *testing.T) {
		_, _, err := s.Split("not-a-uuid", docWithSettings(t, `{"jackett_key":"x"}`))
		require.ErrorIs(t, err, ErrInvalidUserID)
	})

	t.Run("non-object settings", func(t *testing.T) {
		doc := Document{SchemaVersion: 1, Data: map[string]json.RawMessage{"settings": json.RawMessage(`[1]`)}}
		_, _, err := s.Split(testUserID, doc)
		require.Error(t, err)
	})
}

func TestSealer_Merge(t *testing.T) {
	s := newTestSealer(t, 1)
	clean, blob := sealedDoc(t, s)

	t.Run("nil blob returns document unchanged", func(t *testing.T) {
		doc := docWithSettings(t, `{"language":"ru"}`)
		merged, err := s.Merge(testUserID, doc, nil)
		require.NoError(t, err)
		assert.Equal(t, doc, merged)
	})

	t.Run("same uuid in upper case decrypts", func(t *testing.T) {
		merged, err := s.Merge(strings.ToUpper(testUserID), clean, blob)
		require.NoError(t, err)
		assert.Equal(t, "s3cret-pass", settingsOf(t, merged)["torrserver_password"])
	})

	t.Run("missing settings section is recreated", func(t *testing.T) {
		doc := Document{SchemaVersion: 1, Data: map[string]json.RawMessage{"other": json.RawMessage(`{}`)}}
		merged, err := s.Merge(testUserID, doc, blob)
		require.NoError(t, err)
		assert.Equal(t, "admin", settingsOf(t, merged)["torrserver_login"])
		assert.Len(t, settingsOf(t, merged), len(SensitiveSettings))
	})

	t.Run("invalid user id", func(t *testing.T) {
		_, err := s.Merge("", clean, blob)
		require.ErrorIs(t, err, ErrInvalidUserID)
	})

	t.Run("wrong key fails", func(t *testing.T) {
		_, err := newTestSealer(t, 2).Merge(testUserID, clean, blob)
		require.ErrorIs(t, err, ErrConnectionsUnreadable)
	})

	t.Run("non-object settings fails", func(t *testing.T) {
		doc := Document{SchemaVersion: 1, Data: map[string]json.RawMessage{"settings": json.RawMessage(`"x"`)}}
		_, err := s.Merge(testUserID, doc, blob)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrConnectionsUnreadable)
	})

	corrupt := func(mutate func([]byte) []byte) []byte {
		return mutate(append([]byte(nil), blob...))
	}
	tests := []struct {
		name   string
		userID string
		blob   []byte
	}{
		{name: "another user id", userID: otherUserID, blob: blob},
		{name: "empty blob", userID: testUserID, blob: []byte{}},
		{name: "version byte only", userID: testUserID, blob: []byte{blobVersion}},
		{name: "truncated blob", userID: testUserID, blob: blob[:len(blob)-1]},
		{name: "truncated below minimum", userID: testUserID, blob: blob[:20]},
		{name: "unknown version byte", userID: testUserID, blob: corrupt(func(b []byte) []byte { b[0] = 0x02; return b })},
		{name: "tampered nonce", userID: testUserID, blob: corrupt(func(b []byte) []byte { b[1] ^= 0xff; return b })},
		{name: "tampered ciphertext", userID: testUserID, blob: corrupt(func(b []byte) []byte { b[20] ^= 0x01; return b })},
		{name: "tampered tag", userID: testUserID, blob: corrupt(func(b []byte) []byte { b[len(b)-1] ^= 0x01; return b })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Merge(tc.userID, clean, tc.blob)
			require.ErrorIs(t, err, ErrConnectionsUnreadable)
			assert.NotContains(t, err.Error(), "s3cret-pass")
		})
	}
}

func TestSealer_MergeRejectsNonObjectPlaintext(t *testing.T) {
	s := newTestSealer(t, 1)
	aad, err := additionalData(testUserID)
	require.NoError(t, err)
	nonce := make([]byte, s.aead.NonceSize())
	blob := append([]byte{blobVersion}, nonce...)
	blob = s.aead.Seal(blob, nonce, []byte(`["not","an","object"]`), aad)

	_, err = s.Merge(testUserID, docWithSettings(t, `{}`), blob)
	require.ErrorIs(t, err, ErrConnectionsUnreadable)
}

func TestSealer_MergeIgnoresUnknownSealedKeys(t *testing.T) {
	s := newTestSealer(t, 1)
	aad, err := additionalData(testUserID)
	require.NoError(t, err)
	nonce := make([]byte, s.aead.NonceSize())
	blob := append([]byte{blobVersion}, nonce...)
	blob = s.aead.Seal(blob, nonce, []byte(`{"jackett_key":"k","language":"en"}`), aad)

	merged, err := s.Merge(testUserID, docWithSettings(t, `{"language":"ru"}`), blob)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"jackett_key": "k", "language": "ru"}, settingsOf(t, merged))
}

func TestParseUUID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{name: "lower case", in: testUserID, ok: true},
		{name: "upper case", in: strings.ToUpper(testUserID), ok: true},
		{name: "empty", in: ""},
		{name: "no hyphens", in: strings.ReplaceAll(testUserID, "-", "")},
		{name: "braced", in: "{" + testUserID + "}"},
		{name: "misplaced hyphen", in: "5f0c7a2e3-b1d-4c8a-9e6f-1a2b3c4d5e6f"},
		{name: "non-hex digit", in: "5f0c7a2e-3b1d-4c8a-9e6f-1a2b3c4d5e6g"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseUUID(tc.in)
			if !tc.ok {
				require.ErrorIs(t, err, ErrInvalidUserID)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, byte(0x5f), id[0])
			assert.Equal(t, byte(0x6f), id[15])
		})
	}
}
