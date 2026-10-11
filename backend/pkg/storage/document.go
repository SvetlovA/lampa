// Package storage keeps one validated JSON document per user in PostgreSQL.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

// SchemaVersion is the only document schema version accepted by ParseDocument.
const SchemaVersion = 1

// defaults for Limits fields that have no env knob
const (
	DefaultMaxSectionBytes = 1 << 20 // 1 MiB
	DefaultMaxDepth        = 32
)

// Sections lists the allowed top-level keys of Document.Data, in response order.
var Sections = []string{"settings", "favorites", "bookmarks", "scores", "subscriptions", "progress", "history", "other"}

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
	MaxBodyBytes    int64 // whole request body, bytes
	MaxSectionBytes int   // single section of data, bytes
	MaxDepth        int   // object/array nesting of the whole body, the envelope object counts as 1
}

// Document is the logical per-user document: all eight sections are always present in Data.
type Document struct {
	SchemaVersion int
	Data          map[string]json.RawMessage
	UpdatedAt     time.Time
}

// envelope is the wire shape of a PUT body.
type envelope struct {
	SchemaVersion *int            `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

// ParseDocument validates raw as a PUT body {schema_version, data} and returns the document
// with missing sections filled with {}. every rejection is a *ValidationError.
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
		return Document{}, invalid(CodeInvalidDocument, "body must be an object with schema_version and data only")
	}
	if dec.More() {
		return Document{}, invalid(CodeInvalidJSON, "body is not valid json")
	}

	if env.SchemaVersion == nil || *env.SchemaVersion != SchemaVersion {
		return Document{}, invalid(CodeUnsupportedSchemaVersion, "schema_version must be "+strconv.Itoa(SchemaVersion))
	}

	data, err := parseData(env.Data, limits.MaxSectionBytes)
	if err != nil {
		return Document{}, err
	}
	return Document{SchemaVersion: SchemaVersion, Data: data}, nil
}

// parseData checks that data is an object of known object-valued sections and fills the missing ones.
func parseData(raw json.RawMessage, maxSectionBytes int) (map[string]json.RawMessage, error) {
	if !isObject(raw) {
		return nil, invalid(CodeInvalidDocument, "data must be an object")
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, invalid(CodeInvalidDocument, "data must be an object")
	}

	for name, value := range sections {
		if !slices.Contains(Sections, name) {
			return nil, invalid(CodeInvalidDocument, "data contains an unknown section")
		}
		if !isObject(value) {
			return nil, invalid(CodeInvalidDocument, "every section must be an object")
		}
		if len(value) > maxSectionBytes {
			return nil, invalid(CodeInvalidDocument, "section exceeds size limit")
		}
	}

	for _, name := range Sections {
		if _, ok := sections[name]; !ok {
			sections[name] = json.RawMessage(`{}`)
		}
	}
	return sections, nil
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
	return fmt.Sprintf("{SchemaVersion:%d Sections:%d UpdatedAt:%s}", d.SchemaVersion, len(d.Data), d.UpdatedAt.Format(time.RFC3339))
}
