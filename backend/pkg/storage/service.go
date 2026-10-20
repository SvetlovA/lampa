package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

//go:generate go tool moq -out mocks/store.go -pkg mocks -skip-ensure -fmt goimports . Store

// ErrUnavailable is returned when the store fails for a reason other than a missing document.
var ErrUnavailable = errors.New("storage unavailable")

// Store persists user records. PgStore is the production implementation.
type Store interface {
	Get(ctx context.Context, userID string) (Record, error)
	Upsert(ctx context.Context, rec Record) (Record, error)
	Delete(ctx context.Context, userID string) error
}

// Logger is the logging dependency of Service.
type Logger interface {
	Printf(format string, args ...any)
}

// Service reads, replaces and deletes the document of a user, sealing connection credentials at rest.
type Service struct {
	store  Store
	sealer *Sealer
	limits Limits
	logger Logger
}

// NewService creates a Service. every dependency is required and every limit must be positive.
func NewService(store Store, sealer *Sealer, limits Limits, logger Logger) (*Service, error) {
	switch {
	case store == nil:
		return nil, errors.New("nil store")
	case sealer == nil:
		return nil, errors.New("nil sealer")
	case logger == nil:
		return nil, errors.New("nil logger")
	case limits.MaxBodyBytes <= 0 || limits.MaxDepth <= 0:
		return nil, errors.New("limits must be positive")
	}
	return &Service{store: store, sealer: sealer, limits: limits, logger: logger}, nil
}

// Get returns the version-2 document with readable sealed values opened.
// older versions read as missing; unreadable sealed values are omitted.
func (s *Service) Get(ctx context.Context, userID string) (Document, error) {
	id, err := canonicalUserID(userID)
	if err != nil {
		return Document{}, err
	}
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return Document{}, s.storeError("get user data", err)
	}

	if rec.SchemaVersion != SchemaVersion {
		return Document{}, ErrNotFound
	}
	data, err := decodeObject(rec.Data)
	if err != nil {
		return Document{}, fmt.Errorf("decode stored data: %w", err)
	}
	opened, err := s.sealer.openAll(id, data, s.logger)
	if err != nil {
		return Document{}, fmt.Errorf("open stored data: %w", err)
	}
	return Document{SchemaVersion: rec.SchemaVersion, Data: opened, UpdatedAt: rec.UpdatedAt}, nil
}

// Replace validates raw as a PUT body and stores it as the whole document of userID.
// validation failures are returned as the *ValidationError from ParseDocument, unwrapped.
// the returned document is the one stored, credentials included, with the stored UpdatedAt.
func (s *Service) Replace(ctx context.Context, userID string, raw []byte) (Document, error) {
	id, err := canonicalUserID(userID)
	if err != nil {
		return Document{}, err
	}
	doc, err := ParseDocument(raw, s.limits)
	if err != nil {
		return Document{}, err
	}

	sealed, err := s.sealer.sealListed(id, doc.Data, doc.Encrypt)
	if err != nil {
		return Document{}, fmt.Errorf("seal data: %w", err)
	}
	data, err := encodeObject(sealed)
	if err != nil {
		return Document{}, fmt.Errorf("encode data: %w", err)
	}

	rec, err := s.store.Upsert(ctx, Record{UserID: id, SchemaVersion: doc.SchemaVersion, Data: data})
	if errors.Is(err, ErrInvalidData) {
		// the driver error carries only the sqlstate message, never document content
		s.logger.Printf("[WARN] upsert user data: %v", err)
		return Document{}, invalid(CodeInvalidDocument, "document contains a value that cannot be stored")
	}
	if err != nil {
		return Document{}, s.storeError("upsert user data", err)
	}
	doc.UpdatedAt = rec.UpdatedAt
	return doc, nil
}

// Delete removes the document of userID. deleting a missing document is not an error.
func (s *Service) Delete(ctx context.Context, userID string) error {
	id, err := canonicalUserID(userID)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return s.storeError("delete user data", err)
	}
	return nil
}

// storeError maps a store failure to ErrNotFound or ErrUnavailable. unavailability is logged,
// the driver error is kept in the chain for callers that log it too.
func (s *Service) storeError(op string, err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	s.logger.Printf("[WARN] %s: %v", op, err)
	return fmt.Errorf("%s: %w: %w", op, ErrUnavailable, err)
}

// canonicalUserID validates userID as a uuid and returns it in lower case.
func canonicalUserID(userID string) (string, error) {
	if _, err := parseUUID(userID); err != nil {
		return "", err
	}
	return strings.ToLower(userID), nil
}
