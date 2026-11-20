package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "mcs_session"

const sessionTTL = 12 * time.Hour

// Option customizes the API server.
type Option func(*Server)

// WithConsoleAuth enables cookie-session login for the web console and API.
func WithConsoleAuth(user, password string) Option {
	return func(s *Server) {
		s.consoleUser = user
		if password != "" {
			sum := sha256.Sum256([]byte(password))
			hash, err := bcrypt.GenerateFromPassword(sum[:], bcrypt.DefaultCost)
			if err == nil {
				s.consoleHash = hash
			}
		}
		s.sessions = map[string]time.Time{}
	}
}

func (s *Server) authEnabled() bool {
	return s.consoleUser != "" && len(s.consoleHash) > 0
}

func (s *Server) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		switch {
		case path == "/api/v1/login", path == "/api/v1/logout", path == "/api/v1/session", path == "/api/v1/healthz":
			next.ServeHTTP(w, r)
			return
		case strings.HasPrefix(path, "/api/"), strings.HasPrefix(path, "/recordings/"):
			if !s.validSession(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "UNAUTHORIZED", "message": "login required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.consoleUser)) == 1
	sum := sha256.Sum256([]byte(req.Password))
	passOK := bcrypt.CompareHashAndPassword(s.consoleHash, sum[:]) == nil
	if !userOK || !passOK {
		writeJSON(w, 401, map[string]any{"code": "UNAUTHORIZED", "message": "invalid username or password"})
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": "session token failed"})
		return
	}
	token := hex.EncodeToString(buf)
	s.sessMu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()), Secure: r.TLS != nil,
	})
	writeJSON(w, 200, map[string]any{"status": "ok", "user": s.consoleUser})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessMu.Lock()
		delete(s.sessions, c.Value)
		s.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"auth_required": s.authEnabled(),
		"logged_in":     !s.authEnabled() || s.validSession(r),
		"user":          s.consoleUser,
	})
}
