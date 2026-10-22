package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLimits = Limits{MaxBodyBytes: 4 << 20, MaxDepth: DefaultMaxDepth}

// body wraps data into a schema_version 2 envelope.
func body(t *testing.T, data string) []byte {
	t.Helper()
	return []byte(`{"schema_version":2,"data":` + data + `}`)
}

// nested returns a data section value nested n levels deep: {"a":{"a":...{}}}.
func nested(t *testing.T, n int) string {
	t.Helper()
	return strings.Repeat(`{"a":`, n-1) + `{}` + strings.Repeat(`}`, n-1)
}

// requireCode asserts err is a *ValidationError with the given code.
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var verr *ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, code, verr.Code)
}

func TestParseDocument_valid(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want map[string]string
	}{
		{name: "full document", raw: body(t, `{"settings":{"a":1},"favorites":{"b":[1,2]},"bookmarks":{},"scores":{},`+
			`"subscriptions":{},"progress":{},"history":{},"other":{"x":"y"}}`),
			want: map[string]string{"settings": `{"a":1}`, "favorites": `{"b":[1,2]}`, "other": `{"x":"y"}`, "bookmarks": `{}`, "scores": `{}`, "subscriptions": `{}`, "progress": `{}`, "history": `{}`}},
		{name: "partial document", raw: body(t, `{"settings":{"torrserver_url":"http://ts"}}`),
			want: map[string]string{"settings": `{"torrserver_url":"http://ts"}`}},
		{name: "empty data", raw: body(t, `{}`), want: map[string]string{}},
		{name: "whitespace around values", raw: []byte(" {\n \"schema_version\" : 2 , \"data\" : { \"scores\" : { } } }\n"),
			want: map[string]string{"scores": `{ }`}},
		{name: "escaped backslash before u0000 text", raw: body(t, `{"other":{"path":"C:\\u0000"}}`),
			want: map[string]string{"other": `{"path":"C:\\u0000"}`}},
		{name: "paired surrogate escape", raw: body(t, `{"other":{"emoji":"\ud83d\ude00"}}`),
			want: map[string]string{"other": `{"emoji":"\ud83d\ude00"}`}},
		{name: "non-ascii utf-8", raw: body(t, `{"other":{"title":"Матрица"}}`),
			want: map[string]string{"other": `{"title":"Матрица"}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := ParseDocument(tc.raw, testLimits)
			require.NoError(t, err)
			assert.Equal(t, 2, doc.SchemaVersion)
			require.Len(t, doc.Data, len(tc.want))
			for name, want := range tc.want {
				assert.Equal(t, want, string(doc.Data[name]), name)
			}
		})
	}
}

func TestParseDocument_invalid(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		code string
	}{
		{name: "empty body", raw: nil, code: CodeInvalidJSON},
		{name: "truncated json", raw: []byte(`{"schema_version":2,"data":{`), code: CodeInvalidJSON},
		{name: "garbage", raw: []byte(`not json`), code: CodeInvalidJSON},
		{name: "trailing value", raw: []byte(`{"schema_version":2,"data":{}} {}`), code: CodeInvalidJSON},
		{name: "missing comma", raw: []byte(`{"schema_version":2 "data":{}}`), code: CodeInvalidJSON},
		{name: "body is an array", raw: []byte(`[1]`), code: CodeInvalidDocument},
		{name: "unknown envelope field", raw: []byte(`{"schema_version":2,"data":{},"user_id":"x"}`), code: CodeInvalidDocument},
		{name: "schema version as string", raw: []byte(`{"schema_version":"1","data":{}}`), code: CodeInvalidDocument},
		{name: "missing schema version", raw: []byte(`{"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "null schema version", raw: []byte(`{"schema_version":null,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "schema version 1", raw: []byte(`{"schema_version":1,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "schema version 0", raw: []byte(`{"schema_version":0,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "missing data", raw: []byte(`{"schema_version":2}`), code: CodeInvalidDocument},
		{name: "null data", raw: body(t, `null`), code: CodeInvalidDocument},
		{name: "array data", raw: body(t, `[]`), code: CodeInvalidDocument},
		{name: "scalar data", raw: body(t, `"settings"`), code: CodeInvalidDocument},
		{name: "number data", raw: body(t, `42`), code: CodeInvalidDocument},
		{name: "invalid key", raw: body(t, `{"bad/key":1}`), code: CodeInvalidDocument},
		{name: "empty key", raw: body(t, `{"":null}`), code: CodeInvalidDocument},
		{name: "long key", raw: body(t, `{"`+strings.Repeat("x", 129)+`":1}`), code: CodeInvalidDocument},
		{name: "non-ascii key", raw: body(t, `{"é":1}`), code: CodeInvalidDocument},
		{name: "newline key", raw: body(t, `{"x\n":1}`), code: CodeInvalidDocument},
		{name: "reserved object", raw: body(t, `{"key":{"$sealed":"input"}}`), code: CodeInvalidDocument},
		{name: "reserved null", raw: body(t, `{"key":{"$sealed":null,"plain":1}}`), code: CodeInvalidDocument},
		{name: "nested reserved object", raw: body(t, `{"key":[{"nested":{"$sealed":"x"}}]}`), code: CodeInvalidDocument},
		{name: "invalid utf-8", raw: body(t, "{\"other\":{\"t\":\"\xff\xfe\"}}"), code: CodeInvalidDocument},
		{name: "u0000 escape in value", raw: body(t, `{"other":{"t":"a\u0000b"}}`), code: CodeInvalidDocument},
		{name: "u0000 escape in key", raw: body(t, `{"other":{"\u0000":1}}`), code: CodeInvalidDocument},
		{name: "u0000 escape after escaped backslash", raw: body(t, `{"other":{"t":"\\\u0000"}}`), code: CodeInvalidDocument},
		{name: "lone high surrogate", raw: body(t, `{"other":{"t":"\ud83d"}}`), code: CodeInvalidDocument},
		{name: "high surrogate followed by non-surrogate", raw: body(t, `{"other":{"t":"\ud83d\u0041"}}`), code: CodeInvalidDocument},
		{name: "lone low surrogate", raw: body(t, `{"other":{"t":"\ude00"}}`), code: CodeInvalidDocument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDocument(tc.raw, testLimits)
			requireCode(t, err, tc.code)
		})
	}
}

func TestParseDocument_limits(t *testing.T) {
	// envelope is depth 1, data 2, the section 3, so a section nested n deep reaches depth n+2
	depthLimits := Limits{MaxBodyBytes: 1 << 20, MaxDepth: 10}

	tests := []struct {
		name   string
		raw    []byte
		limits Limits
		code   string // empty means the document is accepted
	}{
		{name: "depth at limit", raw: body(t, `{"other":`+nested(t, 8)+`}`), limits: depthLimits},
		{name: "depth over limit", raw: body(t, `{"other":`+nested(t, 9)+`}`), limits: depthLimits, code: CodeInvalidDocument},
		{name: "arrays count toward depth", raw: body(t, `{"other":{"a":[[[[[[[[1]]]]]]]]}}`), limits: depthLimits,
			code: CodeInvalidDocument},
		{name: "body at limit", raw: body(t, `{}`), limits: Limits{MaxBodyBytes: int64(len(body(t, `{}`))), MaxDepth: 32}},
		{name: "body over limit", raw: body(t, `{}`), limits: Limits{MaxBodyBytes: int64(len(body(t, `{}`))) - 1, MaxDepth: 32},
			code: CodeTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDocument(tc.raw, tc.limits)
			if tc.code == "" {
				require.NoError(t, err)
				return
			}
			requireCode(t, err, tc.code)
		})
	}
}

func TestParseDocument_deepNestingDoesNotBlowStack(t *testing.T) {
	const depth = 1_000_000
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "balanced arrays", raw: body(t, `{"other":{"a":`+strings.Repeat("[", depth)+strings.Repeat("]", depth)+`}}`)},
		{name: "unterminated objects", raw: []byte(strings.Repeat(`{"a":`, depth))},
	}
	limits := Limits{MaxBodyBytes: 16 << 20, MaxDepth: DefaultMaxDepth}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDocument(tc.raw, limits)
			requireCode(t, err, CodeInvalidDocument)
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	var err error = invalid(CodeInvalidDocument, "data must be an object")
	require.EqualError(t, err, "invalid_document: data must be an object")

	wrapped := fmt.Errorf("replace: %w", err)
	var verr *ValidationError
	require.ErrorAs(t, wrapped, &verr)
	assert.Equal(t, CodeInvalidDocument, verr.Code)
}

func TestValidationError_reasonNeverEchoesInput(t *testing.T) {
	const secret = "s3cret-value"
	inputs := [][]byte{
		body(t, `{"bad/`+secret+`":{}}`),
		body(t, `{"key":{"$sealed":"`+secret+`"}}`),
		[]byte(`{"schema_version":"` + secret + `","data":{}}`),
		[]byte(`{"schema_version":2,"data":{},"` + secret + `":1}`),
		[]byte(secret),
	}
	for _, raw := range inputs {
		_, err := ParseDocument(raw, testLimits)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestDocument_String(t *testing.T) {
	doc, err := ParseDocument(body(t, `{"settings":{"torrserver_password":"hunter2"}}`), testLimits)
	require.NoError(t, err)
	doc.UpdatedAt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	for _, s := range []string{doc.String(), fmt.Sprintf("%+v", doc)} {
		assert.Equal(t, "{SchemaVersion:2 Keys:1 UpdatedAt:2026-09-18T12:00:00Z}", s)
		assert.NotContains(t, s, "hunter2")
	}
	assert.True(t, json.Valid(doc.Data["settings"]))
}

func TestParseDocument_anyValues(t *testing.T) {
	const data = `{"plain":"$sealed","number":12345678901234567890,"bool":true,"nothing":null,"array":[1,"x",false,null],"object":{"nested":[{}]},"a.b:c-_09":{},"` +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `":0}`
	doc, err := ParseDocument(body(t, data), testLimits)
	require.NoError(t, err)
	encoded, err := encodeObject(doc.Data)
	require.NoError(t, err)
	assert.JSONEq(t, data, string(encoded))
	assert.Nil(t, doc.Encrypt)
}

func TestParseDocument_encrypt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{name: "empty", raw: `[]`, valid: true},
		{name: "valid and missing keys", raw: `["a.b:c-_09","missing"]`, valid: true},
		{name: "64 keys", raw: `[` + strings.Repeat(`"a",`, 63) + `"a"]`, valid: true},
		{name: "65 keys", raw: `[` + strings.Repeat(`"a",`, 64) + `"a"]`},
		{name: "null", raw: `null`},
		{name: "object", raw: `{}`},
		{name: "string", raw: `"key"`},
		{name: "number", raw: `[1]`},
		{name: "null key", raw: `[null]`},
		{name: "invalid key", raw: `["bad/key"]`},
		{name: "empty key", raw: `[""]`},
		{name: "long key", raw: `["` + strings.Repeat("x", 129) + `"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := ParseDocument([]byte(`{"schema_version":2,"data":{},"encrypt":`+tc.raw+`}`), testLimits)
			if !tc.valid {
				requireCode(t, err, CodeInvalidDocument)
				return
			}
			require.NoError(t, err)
			var want []string
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &want))
			assert.Equal(t, want, doc.Encrypt)
		})
	}
}

func TestParseDocument_bodyLimitOnly(t *testing.T) {
	// a single value larger than the former 1 MiB section limit is valid.
	raw := body(t, `{"large":"`+strings.Repeat("x", 3<<20)+`"}`)
	_, err := ParseDocument(raw, testLimits)
	require.NoError(t, err)
	_, err = ParseDocument(body(t, `{"large":"`+strings.Repeat("x", 4<<20)+`"}`), testLimits)
	requireCode(t, err, CodeTooLarge)
}
