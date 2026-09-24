package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/SvetlovA/lampa/backend/pkg/storage"
)

// documentResponse is the wire shape of a stored document, returned by GET and PUT.
type documentResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Data          orderedData `json:"data"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// orderedData encodes document sections in storage.Sections order instead of map key order.
type orderedData map[string]json.RawMessage

// MarshalJSON writes the known sections in response order and skips missing ones.
func (d orderedData) MarshalJSON() ([]byte, error) {
	out := []byte{'{'}
	for _, name := range storage.Sections {
		raw, ok := d[name]
		if !ok {
			continue
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, name...)
		out = append(out, '"', ':')
		out = append(out, raw...)
	}
	return append(out, '}'), nil
}

func (s *Server) getUserData(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	doc, err := s.svc.Get(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeDocument(w, doc)
}

func (s *Server) putUserData(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if maxErr := (*http.MaxBytesError)(nil); errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, storage.CodeTooLarge, "body exceeds size limit")
			return
		}
		s.logger.Printf("[WARN] read body of %q %q: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadRequest, storage.CodeInvalidJSON, "body could not be read")
		return
	}
	id, _ := UserID(r.Context())
	doc, err := s.svc.Replace(r.Context(), id, raw)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeDocument(w, doc)
}

func (s *Server) deleteUserData(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
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
		writeError(w, status, verr.Code, verr.Reason)
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "user_data_not_found", "user data not found")
	case errors.Is(err, storage.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage unavailable")
	case errors.Is(err, storage.ErrConnectionsUnreadable):
		writeError(w, http.StatusInternalServerError, "connections_unreadable", "stored connections cannot be read")
	default:
		s.logger.Printf("[ERROR] %q %q: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func writeDocument(w http.ResponseWriter, doc storage.Document) {
	writeJSON(w, http.StatusOK, documentResponse{
		SchemaVersion: doc.SchemaVersion,
		Data:          orderedData(doc.Data),
		UpdatedAt:     doc.UpdatedAt.UTC(),
	})
}

// isJSON reports whether a Content-Type header names application/json, parameters allowed.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}
