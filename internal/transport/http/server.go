// Package http exposes the usecases as a JSON REST API.
package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/moixblau/HappyX.Api/internal/domain"
	"github.com/moixblau/HappyX.Api/internal/usecase"
)

type Server struct {
	svc       *usecase.Service
	keyHashes [][]byte
	log       *slog.Logger
	web       *Web // nil when the dashboard is not configured
}

func NewServer(svc *usecase.Service, keyHashes [][]byte, log *slog.Logger) *Server {
	return &Server{svc: svc, keyHashes: keyHashes, log: log}
}

// Handler wires the routes. Every tenant route is under /v1/guilds/{guildID}, where
// guildID is the Discord guild (server) ID.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/guilds", s.listGuilds)
	api.HandleFunc("POST /v1/guilds", s.registerGuild)
	api.HandleFunc("GET /v1/guilds/{guildID}", s.getGuild)
	api.HandleFunc("PATCH /v1/guilds/{guildID}", s.updateGuild)
	api.HandleFunc("DELETE /v1/guilds/{guildID}", s.removeGuild)
	api.HandleFunc("GET /v1/removals", s.listRemovals)
	api.HandleFunc("DELETE /v1/removals/{guildID}", s.doneRemoving)
	api.HandleFunc("GET /v1/guilds/{guildID}/members", s.listMembers)
	api.HandleFunc("PUT /v1/guilds/{guildID}/members", s.syncMembers)
	api.HandleFunc("PUT /v1/guilds/{guildID}/members/{userID}", s.joinMember)
	api.HandleFunc("DELETE /v1/guilds/{guildID}/members/{userID}", s.leaveMember)
	api.HandleFunc("GET /v1/guilds/{guildID}/members/{userID}/access", s.memberAccess)
	api.HandleFunc("PUT /v1/guilds/{guildID}/members/{userID}/mood", s.recordMood)
	api.HandleFunc("PUT /v1/guilds/{guildID}/roles", s.syncRoles)
	api.HandleFunc("GET /v1/guilds/{guildID}/report", s.report)
	api.HandleFunc("POST /v1/guilds/{guildID}/dispatches", s.claimDispatch)
	api.HandleFunc("POST /v1/guilds/{guildID}/kudos", s.giveKudos)
	api.HandleFunc("POST /v1/guilds/{guildID}/game/rounds", s.startGameRound)
	api.HandleFunc("GET /v1/guilds/{guildID}/game/rounds/{week}", s.gameRound)
	api.HandleFunc("PUT /v1/guilds/{guildID}/game/rounds/{week}/answers/{userID}", s.answerGame)
	api.HandleFunc("PUT /v1/guilds/{guildID}/game/proposals/{userID}", s.proposeQuestion)
	mux.Handle("/v1/", s.requireKey(api))
	if s.web != nil {
		s.webRoutes(mux)
	}

	return s.logRequests(mux)
}

func (s *Server) requireKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" || !s.validKey(key) {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validKey(key string) bool {
	sum := sha256.Sum256([]byte(key))
	valid := false
	for _, h := range s.keyHashes {
		if subtle.ConstantTimeCompare(sum[:], h) == 1 {
			valid = true
		}
	}
	return valid
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration", time.Since(start))
	})
}

// --- helpers ---

const maxBodyBytes = 64 << 10

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, maxBodyBytes)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Reason)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrInactive):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrNotAMember), errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, domain.ErrClosed), errors.Is(err, domain.ErrGamesOff):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrRemoving):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, domain.ErrTooMany):
		writeError(w, http.StatusTooManyRequests, err.Error())
	default:
		s.log.Error("internal error", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
