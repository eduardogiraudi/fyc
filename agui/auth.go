package agui

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized reports whether the request carries the expected bearer token.
// The comparison is constant-time to avoid leaking the token through timing.
func (s *Server) authorized(r *http.Request) bool {
	if s.opts.Token == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) == 1
}

// applyCORS sets the CORS headers for the configured origin. When no origin is
// configured, no CORS header is emitted (same-origin / curl only).
func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.CORS == "" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if s.opts.CORS != "*" && !strings.EqualFold(origin, s.opts.CORS) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
	return true
}
