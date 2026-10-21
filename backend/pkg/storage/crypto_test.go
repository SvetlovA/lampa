package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
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

// TestTorrserverTwoURLs_matchBundle guards the two-link settings and separate timecode removal.
// Runtime request tests live in testdata/torrserver-one-link.test.cjs.
func TestTorrserverTwoURLs_matchBundle(t *testing.T) {
	bundle := readBundle(t)
	text := string(bundle)
	for _, fragment := range []string{"torrserver_tracktimecode", "'/viewed'", "Torserver.viewed"} {
		assert.NotContains(t, text, fragment, "removed TrackTimecode code %q is back", fragment)
	}
	for _, descr := range []string{"'Торрент будет добавлен в базу'", "'The torrent will be added to the database'"} {
		assert.Contains(t, text, "settings_server_base_descr: "+descr, "save-to-database description must not name TorrServer")
	}

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
	for _, key := range []string{"torrserver_url", "torrserver_url_two", "torrserver_use_link"} {
		assert.Equal(t, 1, strings.Count(serverTemplate, `data-name="`+key+`"`), "settings control %s must occur once", key)
	}
	assert.Contains(t, text, "select('torrserver_use_link', {\n    'one': '#{settings_param_link_use_one}',\n    'two': '#{settings_param_link_use_two}'\n  }, 'one');")
	assert.Contains(t, text, "select('torrserver_url_two', '', '');")
	assert.Equal(t, strings.Count(serverTemplate, "<div"), strings.Count(serverTemplate, "</div>"), "template divs must balance")
	assert.Contains(t, serverTemplate, "#{settings_server_link_one}")
	assert.Contains(t, serverTemplate, "#{settings_server_link_two}")
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
	"account_timeline_sync",  // Tracker appends the active CUB profile id.
	"account_bookmarks_sync", // CUB's IndexedDB bookmark version tracker.
	"content_rows",           // SettingsApi and Storage append each installed row's name.
	"ser_clarifys",           // Legacy key still visited by clear-cache.
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

var storageScopeNames = []string{"user", "user_connection", "user_optional", "device", "connection", "external", "cache"}
var storageScopeEntryPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+\*?$`)

type storageScopeFile struct {
	Version int                 `json:"version"`
	Scopes  map[string][]string `json:"scopes"`
	Notes   map[string]string   `json:"notes"`
	// encryption policy belongs to the frontend; this guard only inventories scopes.
	Encrypt json.RawMessage `json:"encrypt"`
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
		return scope, fmt.Errorf("storage scope must contain all %d scopes", len(storageScopeNames))
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

func storageScopeDrift(bundle []byte, entries map[string]string) []string {
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
	problems := storageScopeDrift(bundle, entries)
	assert.Empty(t, problems, "storage scope drift (audit read/write sites before classifying):\n%s", strings.Join(problems, "\n"))
	// Portable settings must not absorb local engines, credentials owned by other
	// services, executable plugins, or generated CUB cursors via broad prefixes.
	for _, tc := range []struct{ key, scope string }{
		{"language", "user"}, {"tmdb_lang", "user"}, {"favorite", "user"},
		{"mine_reactions", "user"}, {"person_subscribes_id", "user"},
		{"file_view", "user_optional"}, {"file_view_42", "external"},
		{"online_watched_last", "user_optional"}, {"torrents_view", "user_optional"},
		{"online_view", "user_optional"}, {"online_last_balanser", "user_optional"},
		{"search_history", "user_optional"}, {"user_clarifys", "user_optional"},
		{"torrserver_url", "user_connection"}, {"torrserver_auth", "user_connection"},
		{"torrserver_login", "user_connection"}, {"torrserver_password", "user_connection"},
		{"torrserver_url_two", "user_connection"}, {"torrserver_use_link", "device"}, {"torrserver_savedb", "connection"},
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
	valid := `{"version":1,"scopes":{"user":[],"user_connection":[],"user_optional":[],"device":[],"connection":[],"external":[],"cache":[]},"notes":{}}`
	for _, tc := range []struct{ name, data, problem string }{
		{"valid empty inventory", valid, ""},
		{"encryption list", strings.Replace(valid, `"version":1`, `"version":1,"encrypt":["custom.secret","missing"]`, 1), ""},
		{"encryption policy not audited", strings.Replace(valid, `"version":1`, `"version":1,"encrypt":["plugins","not a storage key",42]`, 1), ""},
		{"optional inventory", strings.Replace(valid, `"user_optional":[]`, `"user_optional":["history","progress_*"]`, 1), ""},
		{"malformed", `{`, "decode"},
		{"empty", ``, "decode"},
		{"unsupported version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "version"},
		{"missing version", strings.Replace(valid, `"version":1,`, "", 1), "version"},
		{"missing scopes", `{"version":1}`, "all 7 scopes"},
		{"missing optional scope", strings.Replace(valid, `"user_optional":[],`, "", 1), "all 7 scopes"},
		{"missing scope", strings.Replace(valid, `"user":[],`, "", 1), "all 7 scopes"},
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
		name    string
		bundle  string
		entries map[string]string
		want    []string
	}{
		{name: "empty allowlist inventory", bundle: `Storage.get('b'); Storage.get('a');`, entries: map[string]string{}, want: []string{"unclassified: a", "unclassified: b"}},
		{name: "optional", bundle: `Storage.get('history');`, entries: map[string]string{"history": "user_optional"}},
		{name: "exact", bundle: `Storage.get('a');`, entries: map[string]string{"a": "user"}},
		{name: "stale exact", bundle: `var a = 'a';`, entries: map[string]string{"a": "user"}, want: []string{"stale exact: a"}},
		{name: "prefix", bundle: `Storage.get('file_view'); var name = 'file_view_' + id;`, entries: map[string]string{"file_view": "user", "file_view_*": "user"}},
		{name: "stale prefix", bundle: `Storage.get('key_value');`, entries: map[string]string{"key_*": "device"}, want: []string{"stale prefix: key_*"}},
		{name: "variable prefix literal", bundle: `var base = 'content_rows';`, entries: map[string]string{"content_rows*": "device"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, storageScopeDrift([]byte(tc.bundle), tc.entries))
		})
	}
}

func TestSealer_ListedRoundTrip(t *testing.T) {
	s := newTestSealer(t, 1)
	for _, value := range []string{`"secret-value"`, `null`, `true`, `12345678901234567890`, `[1,"x",null]`, `{"url":"<a&b>","nested":{}}`} {
		t.Run(value, func(t *testing.T) {
			data := map[string]json.RawMessage{"custom.secret": json.RawMessage(value), "plain": json.RawMessage(`"unlisted-secret"`)}
			sealed, err := s.sealListed(testUserID, data, []string{"custom.secret", "missing", "custom.secret"})
			require.NoError(t, err)
			assert.Equal(t, value, string(data["custom.secret"]), "input is not mutated")
			require.Len(t, sealed, 2)
			assert.Contains(t, string(sealed["custom.secret"]), `"$sealed"`)
			assert.NotEqual(t, value, string(sealed["custom.secret"]))
			assert.Equal(t, data["plain"], sealed["plain"])
			var logs bytes.Buffer
			opened, err := s.openAll(testUserID, sealed, log.New(&logs, "", 0))
			require.NoError(t, err)
			assert.Equal(t, data, opened)
			assert.Contains(t, string(sealed["custom.secret"]), `"$sealed"`, "stored map is not mutated")
			assert.Empty(t, logs.String())
		})
	}
}

func TestSealer_NoListedValues(t *testing.T) {
	s := newTestSealer(t, 1)
	data := map[string]json.RawMessage{"plain": json.RawMessage(`{"nested":true}`)}
	for _, encrypt := range [][]string{nil, {}, {"missing"}} {
		sealed, err := s.sealListed(testUserID, data, encrypt)
		require.NoError(t, err)
		assert.Equal(t, data, sealed)
	}
	_, err := s.sealListed("invalid", data, nil)
	require.ErrorIs(t, err, ErrInvalidUserID)
	_, err = s.openAll("invalid", data, log.New(io.Discard, "", 0))
	require.ErrorIs(t, err, ErrInvalidUserID)
}

func TestSealer_UnreadableOmitted(t *testing.T) {
	s := newTestSealer(t, 1)
	data := map[string]json.RawMessage{"secret": json.RawMessage(`"private-value"`), "plain": json.RawMessage(`{"x":1}`)}
	sealed, err := s.sealListed(testUserID, data, []string{"secret"})
	require.NoError(t, err)
	var wrapper map[string]string
	require.NoError(t, json.Unmarshal(sealed["secret"], &wrapper))
	blob, err := base64.StdEncoding.DecodeString(wrapper["$sealed"])
	require.NoError(t, err)
	badVersion := bytes.Clone(blob)
	badVersion[0]++
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 1
	aad, err := additionalData(testUserID)
	require.NoError(t, err)
	invalidJSON, err := s.seal([]byte(`not json`), aad)
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		reader *Sealer
		user   string
		raw    json.RawMessage
	}{
		{name: "wrong key", reader: newTestSealer(t, 2), user: testUserID, raw: sealed["secret"]},
		{name: "wrong user", reader: s, user: otherUserID, raw: sealed["secret"]},
		{name: "not a string", reader: s, user: testUserID, raw: json.RawMessage(`{"$sealed":42}`)},
		{name: "invalid base64", reader: s, user: testUserID, raw: json.RawMessage(`{"$sealed":"!!!"}`)},
		{name: "short blob", reader: s, user: testUserID, raw: json.RawMessage(`{"$sealed":"AQ=="}`)},
		{name: "unknown version", reader: s, user: testUserID, raw: sealedWrapper(t, badVersion)},
		{name: "tampered", reader: s, user: testUserID, raw: sealedWrapper(t, tampered)},
		{name: "invalid json", reader: s, user: testUserID, raw: sealedWrapper(t, invalidJSON)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			stored := map[string]json.RawMessage{"secret": tc.raw, "plain": data["plain"]}
			opened, err := tc.reader.openAll(tc.user, stored, log.New(&logs, "", 0))
			require.NoError(t, err)
			assert.Equal(t, map[string]json.RawMessage{"plain": data["plain"]}, opened)
			assert.Contains(t, logs.String(), "[WARN] omit unreadable sealed value")
			assert.NotContains(t, logs.String(), "private-value")
			assert.NotContains(t, logs.String(), string(tc.raw))
		})
	}
}

func sealedWrapper(t *testing.T, blob []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"$sealed": base64.StdEncoding.EncodeToString(blob)})
	require.NoError(t, err)
	return raw
}

func TestSealer_RandomNonces(t *testing.T) {
	s := newTestSealer(t, 1)
	data := map[string]json.RawMessage{"key": json.RawMessage(`"secret"`)}
	first, err := s.sealListed(testUserID, data, []string{"key"})
	require.NoError(t, err)
	second, err := s.sealListed(testUserID, data, []string{"key"})
	require.NoError(t, err)
	assert.NotEqual(t, first["key"], second["key"])
}
