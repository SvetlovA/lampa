package api

import (
	"context"
	"net/http"
)

//go:generate go tool moq -out mocks/torrents.go -pkg mocks -skip-ensure -fmt goimports . Torrents

const (
	torrserversPrefix = "/api/v1/torrservers/"
	torrentHashesPath = torrserversPrefix + "{server_id}/hashes"
	torrentHashPath   = torrentHashesPath + "/{hash}"
)

// Torrents manages per-user membership without contacting TorrServer.
type Torrents interface {
	List(ctx context.Context, userID, serverID string) ([]string, error)
	Claim(ctx context.Context, userID, serverID, hash string) error
	Release(ctx context.Context, userID, serverID, hash string) error
}

func (s *Server) getTorrentHashes(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	hashes, err := s.torrents.List(r.Context(), id, r.PathValue("server_id"))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if hashes == nil {
		hashes = []string{}
	}
	WriteJSON(w, http.StatusOK, struct {
		Hashes []string `json:"hashes"`
	}{Hashes: hashes})
}

func (s *Server) putTorrentHash(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	if err := s.torrents.Claim(r.Context(), id, r.PathValue("server_id"), r.PathValue("hash")); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteTorrentHash(w http.ResponseWriter, r *http.Request) {
	id, _ := UserID(r.Context())
	if err := s.torrents.Release(r.Context(), id, r.PathValue("server_id"), r.PathValue("hash")); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
