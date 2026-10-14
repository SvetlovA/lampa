package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
		`"jackett_key":"jk-one-value","jackett_key_two":"jk-two-value",`+
		`"prowlarr_key":"pk-one-value","prowlarr_key_two":"pk-two-value","language":"ru"}`)
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
		`"jackett_key":"jk-one-value","jackett_key_two":"jk-two-value",`+
		`"prowlarr_key":"pk-one-value","prowlarr_key_two":"pk-two-value","language":"ru","url":"<a&b>"}`)

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
			for _, secret := range []string{"admin", "s3cret-pass", "jk-one-value", "jk-two-value", "pk-one-value", "pk-two-value"} {
				assert.NotContains(t, string(raw), secret)
			}
			for _, key := range SensitiveSettings {
				assert.NotContains(t, string(raw), key)
			}
		}
		assert.NotContains(t, string(blob), "s3cret-pass")
	})

	t.Run("escaped duplicate and null sensitive keys never reach clean data", func(t *testing.T) {
		doc := docWithSettings(t, `{"jackett_key":"jk-escaped","torrserver_login":"login-first",`+
			`"torrserver_login":"login-last","jackett_key_two":null,"language":"ru"}`)
		clean, blob, err := s.Split(testUserID, doc)
		require.NoError(t, err)
		require.NotNil(t, blob)
		assert.Equal(t, map[string]any{"language": "ru"}, settingsOf(t, clean))
		for _, leaked := range []string{"jk-escaped", "login-first", "login-last", "jackett_key", `k`} {
			assert.NotContains(t, string(clean.Data["settings"]), leaked)
		}

		merged, err := s.Merge(testUserID, clean, blob)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"language": "ru", "jackett_key": "jk-escaped", "torrserver_login": "login-last", "jackett_key_two": nil,
		}, settingsOf(t, merged), "the last duplicate wins, null is restored as null")
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

func TestSealer_MergeLegacyBlob(t *testing.T) {
	// a blob sealed before the prowlarr keys joined SensitiveSettings still merges as is.
	s := newTestSealer(t, 1)
	aad, err := additionalData(testUserID)
	require.NoError(t, err)
	nonce := make([]byte, s.aead.NonceSize())
	blob := append([]byte{blobVersion}, nonce...)
	blob = s.aead.Seal(blob, nonce, []byte(`{"torrserver_login":"admin","torrserver_password":"s3cret-pass",`+
		`"jackett_key":"jk-one-value","jackett_key_two":"jk-two-value"}`), aad)

	merged, err := s.Merge(testUserID, docWithSettings(t, `{"prowlarr_url":"http://p","language":"ru"}`), blob)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"torrserver_login": "admin", "torrserver_password": "s3cret-pass",
		"jackett_key": "jk-one-value", "jackett_key_two": "jk-two-value",
		"prowlarr_url": "http://p", "language": "ru",
	}, settingsOf(t, merged))
}

// uiInputPattern matches a settings input template inside the escaped js string literals of app.min.js.
var uiInputPattern = regexp.MustCompile(`data-type=\\"input\\" data-name=\\"([a-z0-9_]+)\\"`)

// uiInputMarker starts every settings input template; uiDynamicInput is the SettingsApi one whose
// name comes from a plugin at runtime, so it has no literal name to classify.
const (
	uiInputMarker  = `data-type=\"input\"`
	uiDynamicInput = `data-type=\"input\" data-name=\"".concat(`
)

// plainUIInputs lists the settings inputs of the Lampa UI that are stored unsealed. every literal
// input must be either here or in SensitiveSettings, so a new upstream input fails the test until
// someone decides whether it holds a credential.
var plainUIInputs = []string{
	"jackett_url", "jackett_url_two",
	"prowlarr_url", "prowlarr_url_two",
	"torrserver_url",
	"player_nw_path", "device_name",
	"tmdb_proxy_api", "tmdb_proxy_image",
}

func TestSensitiveSettings_matchUI(t *testing.T) {
	bundle := readBundle(t)

	matches := uiInputPattern.FindAllSubmatch(bundle, -1)
	require.NotEmpty(t, matches, "no settings inputs found, the template format changed")
	require.Len(t, matches, bytes.Count(bundle, []byte(uiInputMarker))-bytes.Count(bundle, []byte(uiDynamicInput)),
		"some settings inputs do not match uiInputPattern, the template format changed")

	inputs := map[string]bool{}
	for _, m := range matches {
		name := string(m[1])
		inputs[name] = true
		assert.NotEqual(t, slices.Contains(SensitiveSettings, name), slices.Contains(plainUIInputs, name),
			"ui input %q must be listed in exactly one of SensitiveSettings and plainUIInputs", name)
	}
	for _, key := range SensitiveSettings {
		assert.True(t, inputs[key], "sealed key %q is no longer a ui input", key)
	}
	for _, key := range plainUIInputs {
		assert.True(t, inputs[key], "plain key %q is no longer a ui input", key)
	}
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

// TestTorrserverSingleURL_matchBundle guards the fork patch when upstream rebuilds the bundle.
// Runtime migration/request tests live in testdata/torrserver-one-link.test.cjs.
func TestTorrserverSingleURL_matchBundle(t *testing.T) {
	bundle := readBundle(t)
	text := string(bundle)
	start := strings.Index(text, "// svtlv:begin one-link migration")
	end := strings.Index(text, "// svtlv:end one-link migration")
	require.GreaterOrEqual(t, start, 0, "one-link migration was lost")
	require.Greater(t, end, start, "one-link migration end marker was lost")
	runtime := text[:start] + text[end:]
	for _, key := range []string{"torrserver_url_two", "torrserver_use_link"} {
		assert.NotContains(t, runtime, key, "legacy key %q must only be read by migration", key)
	}
	assert.Contains(t, text, "migrateSvtlvTorrserverUrl();\n        next();", "migration must precede boot consumers")

	var serverTemplate string
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "  var ") && strings.Contains(line, `data-name=\"torrserver_url\"`) {
			_, literal, found := strings.Cut(line, " = ")
			require.True(t, found)
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(literal, ";")), &serverTemplate))
			break
		}
	}
	require.NotEmpty(t, serverTemplate, "TorrServer settings template was lost")
	assert.Equal(t, 1, strings.Count(serverTemplate, `data-name="torrserver_url"`))
	assert.Equal(t, strings.Count(serverTemplate, "<div"), strings.Count(serverTemplate, "</div>"), "template divs must balance")
	assert.Contains(t, serverTemplate, "#{settings_main_torrserver}")
	for _, key := range []string{"jackett_url_two", "prowlarr_url_two", "parser_use_link"} {
		assert.Contains(t, text, `data-name=\"`+key+`\"`, "unrelated parser controls must remain")
	}

	assembly, err := os.ReadFile(filepath.Join("..", "..", "..", "assembly.json"))
	require.NoError(t, err)
	var stamp struct {
		AppVersion string `json:"app_version"`
		CSSVersion string `json:"css_version"`
		AppDigital int    `json:"app_digital"`
		CSSDigital int    `json:"css_digital"`
		Time       int64  `json:"time"`
		Hash       string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(assembly, &stamp))
	for _, version := range []struct {
		field   string
		version string
		digital int
	}{
		{field: "app_version", version: stamp.AppVersion, digital: stamp.AppDigital},
		{field: "css_version", version: stamp.CSSVersion, digital: stamp.CSSDigital},
	} {
		assert.Contains(t, text, version.field+": '"+version.version+"'")
		digital, parseErr := strconv.Atoi(strings.ReplaceAll(version.version, ".", ""))
		require.NoError(t, parseErr)
		assert.Equal(t, digital, version.digital)
	}
	assert.Positive(t, stamp.Time)
	assert.Regexp(t, `^[a-f0-9]{32}$`, stamp.Hash)
	assert.Contains(t, text, "console.log('App', 'hash', '"+stamp.Hash+"')")
}

// readBundle skips only backend-only checkouts; missing classification data must fail.
func readBundle(t *testing.T) []byte {
	t.Helper()
	bundle, err := os.ReadFile(filepath.Join("..", "..", "..", "app.min.js"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("app.min.js is not checked out next to backend/")
	}
	require.NoError(t, err)
	return bundle
}

const storageKeyLiteral = `(?:'([a-zA-Z0-9_.:-]+)'|"([a-zA-Z0-9_.:-]+)")`

var storageKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bStorage\.(?:get|set|field|cache|add|remove|value|sync)\s*\(\s*` + storageKeyLiteral),
	regexp.MustCompile(`\b(?:local|session)Storage\.(?:getItem|setItem|removeItem)\s*\(\s*` + storageKeyLiteral),
	regexp.MustCompile(`(?m)^\s*(?:Params\.)?(?:select|trigger|sync)\s*\(\s*` + storageKeyLiteral),
	regexp.MustCompile(`data-name=(?:\\?"([a-zA-Z0-9_.:-]+)\\?"|\\?'([a-zA-Z0-9_.:-]+)\\?')`),
	regexp.MustCompile(`(?:'([a-zA-Z0-9_.:-]+_)'|"([a-zA-Z0-9_.:-]+_)")\s*\+`),
}

// Variable-built keys require a read/write audit. These literal guards inventory
// their known bases, not arbitrary values flowing through Storage(name). Plugin
// and remote-configuration keys remain local unless explicitly classified.
var auditedStorageLiterals = []string{
	"account_timeline_sync",         // Tracker appends the active CUB profile id.
	"account_bookmarks_sync",        // CUB's IndexedDB bookmark version tracker.
	"content_rows",                  // SettingsApi and Storage append each installed row's name.
	"svtlv_torrserver_legacy_links", // The migration uses a variable for its journal key.
	"ser_clarifys",                  // Legacy key still visited by clear-cache.
}

func bundleHasLiteral(bundle []byte, literal string) bool {
	return bytes.Contains(bundle, []byte("'"+literal+"'")) || bytes.Contains(bundle, []byte(`"`+literal+`"`))
}

func extractStorageKeys(bundle []byte) []string {
	keys := map[string]bool{}
	for _, pattern := range storageKeyPatterns {
		for _, match := range pattern.FindAllSubmatch(bundle, -1) {
			for _, capture := range match[1:] {
				if len(capture) != 0 {
					keys[string(capture)] = true
				}
			}
		}
	}
	for _, literal := range auditedStorageLiterals {
		if bundleHasLiteral(bundle, literal) {
			keys[literal] = true
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

var storageScopeNames = []string{"user", "user_connection", "device", "connection", "external", "cache"}
var storageScopeEntryPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+\*?$`)

type storageScopeFile struct {
	Version int                 `json:"version"`
	Scopes  map[string][]string `json:"scopes"`
	Notes   map[string]string   `json:"notes"`
}

func parseStorageScope(data []byte) (storageScopeFile, error) {
	var scope storageScopeFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return scope, fmt.Errorf("decode storage scope: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return scope, errors.New("storage scope must contain one JSON document")
	}
	if scope.Version != 1 {
		return scope, fmt.Errorf("unsupported storage scope version %d", scope.Version)
	}
	if len(scope.Scopes) != len(storageScopeNames) {
		return scope, errors.New("storage scope must contain all six scopes")
	}
	for _, name := range storageScopeNames {
		if entries, ok := scope.Scopes[name]; !ok || entries == nil {
			return scope, fmt.Errorf("missing storage scope array %q", name)
		}
	}
	if _, err := storageScopeEntries(scope.Scopes); err != nil {
		return scope, err
	}
	return scope, nil
}

// storageScopeEntries rejects real duplicates; overlapping prefixes are deliberate.
func storageScopeEntries(scopes map[string][]string) (map[string]string, error) {
	entries := map[string]string{}
	names := make([]string, 0, len(scopes))
	for name := range scopes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(storageScopeNames, name) {
			return nil, fmt.Errorf("unknown storage scope %q", name)
		}
		for _, entry := range scopes[name] {
			if !storageScopeEntryPattern.MatchString(entry) {
				return nil, fmt.Errorf("invalid storage scope entry %q", entry)
			}
			if previous, exists := entries[entry]; exists {
				return nil, fmt.Errorf("duplicate storage scope entry %q in %s and %s", entry, previous, name)
			}
			entries[entry] = name
		}
	}
	return entries, nil
}

func resolveStorageScope(entries map[string]string, key string) string {
	if scope, exists := entries[key]; exists {
		return scope
	}
	var scope string
	longest := -1
	for entry, name := range entries {
		prefix, wildcard := strings.CutSuffix(entry, "*")
		if wildcard && len(prefix) > longest && strings.HasPrefix(key, prefix) {
			longest, scope = len(prefix), name
		}
	}
	return scope
}

func storageScopeDrift(bundle []byte, entries map[string]string, sensitive []string) []string {
	keys := extractStorageKeys(bundle)
	var problems []string
	for _, key := range keys {
		if resolveStorageScope(entries, key) == "" {
			problems = append(problems, "unclassified: "+key)
		}
	}
	for entry := range entries {
		prefix, wildcard := strings.CutSuffix(entry, "*")
		if wildcard {
			if !slices.Contains(keys, prefix) && !bundleHasLiteral(bundle, prefix) {
				problems = append(problems, "stale prefix: "+entry)
			}
		} else if !slices.Contains(keys, entry) {
			problems = append(problems, "stale exact: "+entry)
		}
	}
	for _, key := range sensitive {
		scope := resolveStorageScope(entries, key)
		if scope != "user_connection" && scope != "connection" {
			problems = append(problems, "sensitive key outside connection scopes: "+key)
		}
	}
	slices.Sort(problems)
	return problems
}

func TestStorageScope_matchBundle(t *testing.T) {
	bundle := readBundle(t)
	keys := extractStorageKeys(bundle)
	require.NotEmpty(t, keys, "no storage keys extracted; bundle format changed")
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "svtlv", "storage-scope.json"))
	require.NoError(t, err, "storage-scope.json must be checked out with app.min.js")
	scope, err := parseStorageScope(data)
	require.NoError(t, err)
	entries, err := storageScopeEntries(scope.Scopes)
	require.NoError(t, err)
	problems := storageScopeDrift(bundle, entries, SensitiveSettings)
	assert.Empty(t, problems, "storage scope drift (audit read/write sites before classifying):\n%s", strings.Join(problems, "\n"))
	// Portable settings must not absorb local engines, credentials owned by other
	// services, executable plugins, or generated CUB cursors via broad prefixes.
	for _, tc := range []struct{ key, scope string }{
		{"language", "user"}, {"tmdb_lang", "user"}, {"file_view_42", "user"},
		{"online_watched_last", "user"}, {"torrents_view", "user"},
		{"torrserver_url", "user_connection"}, {"torrserver_auth", "user_connection"},
		{"torrserver_url_two", "device"}, {"torrserver_use_link", "device"},
		{"svtlv_torrserver_legacy_links", "device"}, {"torrserver_savedb", "connection"},
		{"plugins", "device"}, {"plugins_blacklist", "device"}, {"player", "device"},
		{"menu_sort", "device"}, {"menu_hide", "device"}, {"start_page", "device"},
		{"content_rows_online", "device"}, {"parental_control_pin", "device"},
		{"account", "external"}, {"account_timeline_sync_42", "external"},
		{"account_bookmarks_sync", "external"}, {"cloud_token", "external"},
		{"storage_search_history_update_time", "cache"}, {"lampa_uid", "cache"},
		{"torrents_filter_data", "cache"}, {"unclassified_plugin_setting", ""},
		{"svtlv_torrserver_guest_hashes", ""}, {"svtlv_account_signed_in", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.scope, resolveStorageScope(entries, tc.key))
		})
	}
	assert.ElementsMatch(t, []string{"torrserver_url", "torrserver_auth", "torrserver_login", "torrserver_password"},
		scope.Scopes["user_connection"], "only the sole TorrServer connection belongs in Plan 4")
	t.Logf("audited %d extracted storage keys/fragments and %d scope entries", len(keys), len(entries))
}

func TestExtractStorageKeys(t *testing.T) {
	for _, method := range []string{"get", "set", "field", "cache", "add", "remove", "value", "sync"} {
		for _, quote := range []string{"'", `"`} {
			t.Run("Storage/"+method+quote, func(t *testing.T) {
				assert.Equal(t, []string{"some_key"}, extractStorageKeys([]byte("Lampa.Storage."+method+" ( "+quote+"some_key"+quote+", value)")))
			})
		}
	}
	for _, store := range []string{"localStorage", "sessionStorage"} {
		for _, method := range []string{"getItem", "setItem", "removeItem"} {
			for _, quote := range []string{"'", `"`} {
				t.Run(store+"/"+method+quote, func(t *testing.T) {
					assert.Equal(t, []string{"direct_key"}, extractStorageKeys([]byte("window."+store+"."+method+"(\n"+quote+"direct_key"+quote+")")))
				})
			}
		}
	}
	for _, method := range []string{"select", "trigger", "sync", "Params.select", "Params.trigger"} {
		for _, quote := range []string{"'", `"`} {
			t.Run(method+quote, func(t *testing.T) {
				assert.Equal(t, []string{"registered"}, extractStorageKeys([]byte("\n  "+method+" ( "+quote+"registered"+quote+", value)")))
			})
		}
	}
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "escaped double template", text: `data-name=\"template_key\"`, want: []string{"template_key"}},
		{name: "escaped single template", text: `data-name=\'template_key\'`, want: []string{"template_key"}},
		{name: "double template", text: `data-name="template_key"`, want: []string{"template_key"}},
		{name: "single template", text: `data-name='template_key'`, want: []string{"template_key"}},
		{name: "single concatenation", text: `'dynamic_' + name`, want: []string{"dynamic_"}},
		{name: "double concatenation", text: `"dynamic_" + name`, want: []string{"dynamic_"}},
		{name: "punctuated key", text: `Storage.get("new-key.v1:setting");`, want: []string{"new-key.v1:setting"}},
		{name: "dedup and sort", text: `Storage.get('z'); Storage.set("a"); localStorage.removeItem('z');`, want: []string{"a", "z"}},
		{name: "variable argument", text: `Storage.get(name); localStorage.getItem(key);`, want: []string{}},
		{name: "unrelated names", text: `OtherStorage.get('no'); mylocalStorage.setItem('no'); object.select('no');`, want: []string{}},
		{name: "unanchored registration", text: `var value = select('no');`, want: []string{}},
		{name: "mismatched quotes", text: `Storage.get('no");`, want: []string{}},
		{name: "template placeholder", text: `data-name=\"{name}\"`, want: []string{}},
	}
	for _, literal := range auditedStorageLiterals {
		for _, quote := range []string{"'", `"`} {
			t.Run(literal+quote, func(t *testing.T) {
				assert.Equal(t, []string{literal}, extractStorageKeys([]byte("var name = "+quote+literal+quote)))
			})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractStorageKeys([]byte(tc.text)))
		})
	}
}

func TestResolveStorageScope(t *testing.T) {
	entries, err := storageScopeEntries(map[string][]string{
		"external": {"account*"}, "cache": {"account_timeline*"},
		"user": {"account_timeline_progress"}, "device": {"account_timeline_sync*"},
	})
	require.NoError(t, err)
	for _, tc := range []struct{ key, scope string }{
		{"account", "external"}, {"account_token", "external"},
		{"account_timeline_progress", "user"}, {"account_timeline_other", "cache"},
		{"account_timeline_sync_12", "device"}, {"unknown", ""}, {"xaccount", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.scope, resolveStorageScope(entries, tc.key))
		})
	}
}

func TestStorageScopeEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes map[string][]string
		error  string
	}{
		{name: "exact duplicate in one scope", scopes: map[string][]string{"user": {"key", "key"}}, error: "duplicate"},
		{name: "exact duplicate across scopes", scopes: map[string][]string{"user": {"key"}, "device": {"key"}}, error: "duplicate"},
		{name: "prefix duplicate", scopes: map[string][]string{"user": {"key*"}, "device": {"key*"}}, error: "duplicate"},
		{name: "overlap permitted", scopes: map[string][]string{"user": {"key*", "key_sub*", "key_exact"}}},
		{name: "unknown scope", scopes: map[string][]string{"users": {"key"}}, error: "unknown"},
		{name: "empty entry", scopes: map[string][]string{"user": {""}}, error: "invalid"},
		{name: "punctuated entry", scopes: map[string][]string{"device": {"new-key.v1:setting"}}},
		{name: "catch all", scopes: map[string][]string{"user": {"*"}}, error: "invalid"},
		{name: "embedded wildcard", scopes: map[string][]string{"user": {"key*tail"}}, error: "invalid"},
		{name: "double wildcard", scopes: map[string][]string{"user": {"key**"}}, error: "invalid"},
		{name: "whitespace", scopes: map[string][]string{"user": {" key"}}, error: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := storageScopeEntries(tc.scopes)
			if tc.error != "" {
				require.ErrorContains(t, err, tc.error)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestParseStorageScope(t *testing.T) {
	valid := `{"version":1,"scopes":{"user":[],"user_connection":[],"device":[],"connection":[],"external":[],"cache":[]},"notes":{}}`
	for _, tc := range []struct{ name, data, problem string }{
		{"valid empty inventory", valid, ""},
		{"malformed", `{`, "decode"},
		{"empty", ``, "decode"},
		{"unsupported version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "version"},
		{"missing version", strings.Replace(valid, `"version":1,`, "", 1), "version"},
		{"missing scopes", `{"version":1}`, "six scopes"},
		{"missing scope", strings.Replace(valid, `"user":[],`, "", 1), "six scopes"},
		{"unknown scope", strings.Replace(valid, `"user":[]`, `"users":[]`, 1), "missing"},
		{"null array", strings.Replace(valid, `"user":[]`, `"user":null`, 1), "missing"},
		{"wrong array type", strings.Replace(valid, `"user":[]`, `"user":"key"`, 1), "decode"},
		{"duplicate rule", strings.Replace(valid, `"user":[]`, `"user":["key","key"]`, 1), "duplicate"},
		{"unknown field", strings.Replace(valid, `"version":1`, `"version":1,"extra":true`, 1), "unknown field"},
		{"second document", valid + valid, "one JSON document"},
		{"trailing garbage", valid + "x", "one JSON document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseStorageScope([]byte(tc.data))
			if tc.problem != "" {
				require.ErrorContains(t, err, tc.problem)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestStorageScopeDrift(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bundle    string
		entries   map[string]string
		sensitive []string
		want      []string
	}{
		{name: "empty allowlist inventory", bundle: `Storage.get('b'); Storage.get('a');`, entries: map[string]string{}, want: []string{"unclassified: a", "unclassified: b"}},
		{name: "exact", bundle: `Storage.get('a');`, entries: map[string]string{"a": "user"}},
		{name: "stale exact", bundle: `var a = 'a';`, entries: map[string]string{"a": "user"}, want: []string{"stale exact: a"}},
		{name: "prefix", bundle: `Storage.get('file_view'); var name = 'file_view_' + id;`, entries: map[string]string{"file_view": "user", "file_view_*": "user"}},
		{name: "stale prefix", bundle: `Storage.get('key_value');`, entries: map[string]string{"key_*": "device"}, want: []string{"stale prefix: key_*"}},
		{name: "variable prefix literal", bundle: `var base = 'content_rows';`, entries: map[string]string{"content_rows*": "device"}},
		{name: "secret user connection", bundle: `Storage.get('secret');`, entries: map[string]string{"secret": "user_connection"}, sensitive: []string{"secret"}},
		{name: "secret connection", bundle: `Storage.get('secret');`, entries: map[string]string{"secret": "connection"}, sensitive: []string{"secret"}},
		{name: "secret exact overrides unsafe prefix", bundle: `Storage.get('secret_key'); var name = 'secret_' + id;`, entries: map[string]string{"secret_*": "user", "secret_key": "connection"}, sensitive: []string{"secret_key"}},
		{name: "unsafe secret", bundle: `Storage.get('secret');`, entries: map[string]string{"secret": "user"}, sensitive: []string{"secret"}, want: []string{"sensitive key outside connection scopes: secret"}},
		{name: "unclassified secret", bundle: `Storage.get('secret');`, entries: map[string]string{}, sensitive: []string{"secret"}, want: []string{"sensitive key outside connection scopes: secret", "unclassified: secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, storageScopeDrift([]byte(tc.bundle), tc.entries, tc.sensitive))
		})
	}
}
