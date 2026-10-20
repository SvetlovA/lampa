package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

// documentResponse is the wire shape of a stored document, returned by GET.
type documentResponse struct {
	SchemaVersion int                        `json:"schema_version"`
	Data          map[string]json.RawMessage `json:"data"`
	UpdatedAt     time.Time                  `json:"updated_at"`
}

func (s *Server) getUserData(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	if !identityMatches(w, r, id) {
		return
	}
	doc, err := s.svc.Get(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(doc.ETag))
	if stripWeakETag(r.Header.Get("If-None-Match")) == strconv.Quote(doc.ETag) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeDocument(w, doc)
}

func (s *Server) putUserData(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	if !identityMatches(w, r, id) {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if maxErr := (*http.MaxBytesError)(nil); errors.As(err, &maxErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, storage.CodeTooLarge, "body exceeds size limit")
			return
		}
		s.logger.Printf("[WARN] read body of %q %q: %v", r.Method, logPath(r.URL.Path), err)
		WriteError(w, http.StatusBadRequest, storage.CodeInvalidJSON, "body could not be read")
		return
	}
	var expectedETag *string
	if values, present := r.Header["If-Match"]; present {
		// even an empty or malformed header is a precondition, never an upsert.
		tag := stripWeakETag(strings.Join(values, ","))
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' {
			tag = tag[1 : len(tag)-1]
		}
		expectedETag = &tag
	}
	doc, err := s.svc.Replace(r.Context(), id, raw, expectedETag)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(doc.ETag))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteUserData(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	if !identityMatches(w, r, id) {
		return
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeServiceError maps a service error to the api error contract. unexpected errors are
// logged and answered with a generic 500; the service never puts document content into errors.
func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *storage.ValidationError
	switch {
	case errors.As(err, &verr):
		status := http.StatusBadRequest
		if verr.Code == storage.CodeTooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		WriteError(w, status, verr.Code, verr.Reason)
	case errors.Is(err, storage.ErrPreconditionFailed):
		WriteError(w, http.StatusPreconditionFailed, "precondition_failed", "user data changed; fetch it again")
	case errors.Is(err, storage.ErrNotFound):
		WriteError(w, http.StatusNotFound, "user_data_not_found", "user data not found")
	case errors.Is(err, storage.ErrUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage unavailable")
	default:
		s.logger.Printf("[ERROR] %q %q: %v", r.Method, logPath(r.URL.Path), err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func writeDocument(w http.ResponseWriter, doc storage.Document) {
	WriteJSON(w, http.StatusOK, documentResponse{
		SchemaVersion: doc.SchemaVersion,
		Data:          doc.Data,
		UpdatedAt:     doc.UpdatedAt.UTC(),
	})
}

// isJSON reports whether a Content-Type header names application/json, parameters allowed.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// stripWeakETag compares tags without the optional weak validator prefix.
func stripWeakETag(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "W/")
}
