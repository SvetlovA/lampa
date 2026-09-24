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

var testLimits = Limits{MaxBodyBytes: 2 << 20, MaxSectionBytes: DefaultMaxSectionBytes, MaxDepth: DefaultMaxDepth}

// body wraps data into a schema_version 1 envelope.
func body(t *testing.T, data string) []byte {
	t.Helper()
	return []byte(`{"schema_version":1,"data":` + data + `}`)
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
			want: map[string]string{"settings": `{"a":1}`, "favorites": `{"b":[1,2]}`, "other": `{"x":"y"}`}},
		{name: "partial document", raw: body(t, `{"settings":{"torrserver_url":"http://ts"}}`),
			want: map[string]string{"settings": `{"torrserver_url":"http://ts"}`, "history": `{}`}},
		{name: "empty data", raw: body(t, `{}`), want: map[string]string{"settings": `{}`, "other": `{}`}},
		{name: "whitespace around values", raw: []byte(" {\n \"schema_version\" : 1 , \"data\" : { \"scores\" : { } } }\n"),
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
			assert.Equal(t, 1, doc.SchemaVersion)
			require.Len(t, doc.Data, len(Sections))
			for _, name := range Sections {
				assert.Contains(t, doc.Data, name)
			}
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
		{name: "truncated json", raw: []byte(`{"schema_version":1,"data":{`), code: CodeInvalidJSON},
		{name: "garbage", raw: []byte(`not json`), code: CodeInvalidJSON},
		{name: "trailing value", raw: []byte(`{"schema_version":1,"data":{}} {}`), code: CodeInvalidJSON},
		{name: "missing comma", raw: []byte(`{"schema_version":1 "data":{}}`), code: CodeInvalidJSON},
		{name: "body is an array", raw: []byte(`[1]`), code: CodeInvalidDocument},
		{name: "unknown envelope field", raw: []byte(`{"schema_version":1,"data":{},"user_id":"x"}`), code: CodeInvalidDocument},
		{name: "schema version as string", raw: []byte(`{"schema_version":"1","data":{}}`), code: CodeInvalidDocument},
		{name: "missing schema version", raw: []byte(`{"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "null schema version", raw: []byte(`{"schema_version":null,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "schema version 2", raw: []byte(`{"schema_version":2,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "schema version 0", raw: []byte(`{"schema_version":0,"data":{}}`), code: CodeUnsupportedSchemaVersion},
		{name: "missing data", raw: []byte(`{"schema_version":1}`), code: CodeInvalidDocument},
		{name: "null data", raw: body(t, `null`), code: CodeInvalidDocument},
		{name: "array data", raw: body(t, `[]`), code: CodeInvalidDocument},
		{name: "scalar data", raw: body(t, `"settings"`), code: CodeInvalidDocument},
		{name: "number data", raw: body(t, `42`), code: CodeInvalidDocument},
		{name: "unknown section", raw: body(t, `{"settings":{},"secrets":{}}`), code: CodeInvalidDocument},
		{name: "array section", raw: body(t, `{"favorites":[]}`), code: CodeInvalidDocument},
		{name: "string section", raw: body(t, `{"settings":"x"}`), code: CodeInvalidDocument},
		{name: "null section", raw: body(t, `{"history":null}`), code: CodeInvalidDocument},
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
	depthLimits := Limits{MaxBodyBytes: 1 << 20, MaxSectionBytes: 1 << 20, MaxDepth: 10}
	sectionLimits := Limits{MaxBodyBytes: 1 << 20, MaxSectionBytes: 20, MaxDepth: 32}

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
		{name: "section at limit", raw: body(t, `{"other":{"k":"`+strings.Repeat("x", 12)+`"}}`), limits: sectionLimits},
		{name: "section over limit", raw: body(t, `{"other":{"k":"`+strings.Repeat("x", 13)+`"}}`), limits: sectionLimits,
			code: CodeInvalidDocument},
		{name: "body at limit", raw: body(t, `{}`), limits: Limits{MaxBodyBytes: int64(len(body(t, `{}`))), MaxSectionBytes: 20, MaxDepth: 32}},
		{name: "body over limit", raw: body(t, `{}`), limits: Limits{MaxBodyBytes: int64(len(body(t, `{}`))) - 1, MaxSectionBytes: 20, MaxDepth: 32},
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
	limits := Limits{MaxBodyBytes: 16 << 20, MaxSectionBytes: 16 << 20, MaxDepth: DefaultMaxDepth}
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
		body(t, `{"`+secret+`":{}}`),
		body(t, `{"settings":"`+secret+`"}`),
		[]byte(`{"schema_version":"` + secret + `","data":{}}`),
		[]byte(`{"schema_version":1,"data":{},"` + secret + `":1}`),
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
		assert.Equal(t, "{SchemaVersion:1 Sections:8 UpdatedAt:2026-09-18T12:00:00Z}", s)
		assert.NotContains(t, s, "hunter2")
	}
	assert.True(t, json.Valid(doc.Data["settings"]))
}
