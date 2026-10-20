// Package storage keeps one validated JSON document per user in PostgreSQL.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

// SchemaVersion is the only document schema version accepted by ParseDocument.
const SchemaVersion = 2

// DefaultMaxDepth bounds object/array nesting in incoming documents.
const DefaultMaxDepth = 32

var dataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// validation error codes, returned to API clients as-is
const (
	CodeInvalidJSON              = "invalid_json"
	CodeInvalidDocument          = "invalid_document"
	CodeUnsupportedSchemaVersion = "unsupported_schema_version"
	CodeTooLarge                 = "request_too_large"
)

// ValidationError reports why a document was rejected. Reason is a fixed short string
// and never contains input data, so it is safe to log and to return to the client.
type ValidationError struct {
	Code   string // one of the Code* constants
	Reason string // fixed human-readable explanation
}

// Error returns the code and the reason.
func (e *ValidationError) Error() string {
	return e.Code + ": " + e.Reason
}

func invalid(code, reason string) *ValidationError {
	return &ValidationError{Code: code, Reason: reason}
}

// Limits bounds the size and shape of an incoming document.
type Limits struct {
	MaxBodyBytes int64 // whole request body, bytes
	MaxDepth     int   // object/array nesting of the whole body, the envelope object counts as 1
}

// Document is the logical per-user document with arbitrary JSON values.
type Document struct {
	SchemaVersion int
	Data          map[string]json.RawMessage
	ETag          string   // sha256 of canonical plaintext data, without quotes
	Encrypt       []string // keys selected by the client for encryption at rest
	UpdatedAt     time.Time
}

// envelope is the wire shape of a PUT body.
type envelope struct {
	SchemaVersion *int            `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
	Encrypt       json.RawMessage `json:"encrypt"`
}

// ParseDocument validates raw as a PUT body {schema_version, data, encrypt}.
// every rejection is a *ValidationError.
// it also rejects input postgres jsonb cannot store (invalid UTF-8, \u0000, unpaired surrogates),
// so such bodies surface as client errors instead of storage failures.
func ParseDocument(raw []byte, limits Limits) (Document, error) {
	if int64(len(raw)) > limits.MaxBodyBytes {
		return Document{}, invalid(CodeTooLarge, "body exceeds size limit")
	}
	if !utf8.Valid(raw) {
		return Document{}, invalid(CodeInvalidDocument, "body is not valid utf-8")
	}
	if err := checkDepth(raw, limits.MaxDepth); err != nil {
		return Document{}, err
	}
	if err := checkEscapes(raw); err != nil {
		return Document{}, err
	}

	var env envelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Document{}, invalid(CodeInvalidJSON, "body is not valid json")
		}
		return Document{}, invalid(CodeInvalidDocument, "body must be an object with schema_version, data and optional encrypt")
	}
	if dec.More() {
		return Document{}, invalid(CodeInvalidJSON, "body is not valid json")
	}

	if env.SchemaVersion == nil || *env.SchemaVersion != SchemaVersion {
		return Document{}, invalid(CodeUnsupportedSchemaVersion, "schema_version must be "+strconv.Itoa(SchemaVersion))
	}

	data, err := parseData(env.Data)
	if err != nil {
		return Document{}, err
	}
	encrypt, err := parseEncrypt(env.Encrypt)
	if err != nil {
		return Document{}, err
	}
	return Document{SchemaVersion: SchemaVersion, Data: data, Encrypt: encrypt}, nil
}

// parseData accepts arbitrary values, reserving $sealed objects for stored ciphertext.
func parseData(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if !isObject(raw) {
		return nil, invalid(CodeInvalidDocument, "data must be an object")
	}
	data, err := decodeObject(raw)
	if err != nil {
		return nil, invalid(CodeInvalidDocument, "data must be an object")
	}
	for key, rawValue := range data {
		if !dataKeyPattern.MatchString(key) {
			return nil, invalid(CodeInvalidDocument, "data contains an invalid key")
		}
		dec := json.NewDecoder(bytes.NewReader(rawValue))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, invalid(CodeInvalidDocument, "data contains an invalid value")
		}
		if containsSealed(value) {
			return nil, invalid(CodeInvalidDocument, "data contains a reserved $sealed object")
		}
	}
	return data, nil
}

func containsSealed(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if _, found := v["$sealed"]; found {
			return true
		}
		for _, child := range v {
			if containsSealed(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(v, containsSealed)
	}
	return false
}

func parseEncrypt(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var keys []string
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, invalid(CodeInvalidDocument, "encrypt must be an array of keys")
	}
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) > 64 {
		return nil, invalid(CodeInvalidDocument, "encrypt must contain at most 64 keys")
	}
	for _, key := range keys {
		if !dataKeyPattern.MatchString(key) {
			return nil, invalid(CodeInvalidDocument, "encrypt contains an invalid key")
		}
	}
	return keys, nil
}

// checkDepth walks raw token by token, without recursion, so hostile nesting cannot exhaust the stack.
func checkDepth(raw []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return invalid(CodeInvalidJSON, "body is not valid json")
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return invalid(CodeInvalidDocument, "body exceeds nesting limit")
			}
		default:
			depth--
		}
	}
}

// checkEscapes rejects \u escapes postgres jsonb refuses: \u0000 and unpaired utf-16 surrogates.
// raw must already be valid json, so every backslash starts a complete escape sequence.
func checkEscapes(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++ // skip the escaped character, a second backslash included
		r, ok := unicodeEscape(raw, i-1)
		if !ok {
			continue
		}
		i += 4
		switch {
		case r == 0:
			return invalid(CodeInvalidDocument, "body contains a \\u0000 escape")
		case r >= 0xDC00 && r <= 0xDFFF:
			return invalid(CodeInvalidDocument, "body contains an unpaired surrogate escape")
		case r >= 0xD800 && r <= 0xDBFF:
			low, ok := unicodeEscape(raw, i+1)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return invalid(CodeInvalidDocument, "body contains an unpaired surrogate escape")
			}
			i += 6
		}
	}
	return nil
}

// unicodeEscape decodes a \uXXXX sequence starting at raw[at], ok is false if there is none.
func unicodeEscape(raw []byte, at int) (rune, bool) {
	if at+6 > len(raw) || raw[at] != '\\' || raw[at+1] != 'u' {
		return 0, false
	}
	v, err := strconv.ParseUint(string(raw[at+2:at+6]), 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(v), true
}

func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// String returns a short description without document content.
func (d Document) String() string {
	return fmt.Sprintf("{SchemaVersion:%d Keys:%d UpdatedAt:%s}", d.SchemaVersion, len(d.Data), d.UpdatedAt.Format(time.RFC3339))
}
