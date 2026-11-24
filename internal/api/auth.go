package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"mcs/internal/auth"
)

const sessionCookie = "mcs_session"

const sessionTTL = 12 * time.Hour

type consoleSession struct {
	expires  time.Time
	username string
	role     string
}

// Option customizes the API server.
type Option func(*Server)

// WithConsoleAuth enables the legacy single-admin login used by older
// deployments and tests. If a users table exists, database users take
// precedence.
func WithConsoleAuth(user, password string) Option {
	return func(s *Server) {
		s.consoleUser = user
		if password != "" {
			hash, err := auth.HashPassword(password)
			if err == nil {
				s.consoleHash = []byte(hash)
			}
		}
		if s.sessions == nil {
			s.sessions = map[string]consoleSession{}
		}
	}
}

func (s *Server) authEnabled() bool {
	if s.db != nil && s.db.CountUsers() > 0 {
		return true
	}
	return s.consoleUser != "" && len(s.consoleHash) > 0
}

func (s *Server) sessionFor(r *http.Request) (consoleSession, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return consoleSession{}, false
	}
	s.sessMu.Lock()
	sess, ok := s.sessions[c.Value]
	if ok && time.Now().After(sess.expires) {
		delete(s.sessions, c.Value)
		ok = false
	}
	s.sessMu.Unlock()
	if !ok {
		return consoleSession{}, false
	}
	// If the account was disabled, deleted or its role changed, reflect that
	// immediately instead of waiting for the old session to expire.
	if s.db != nil && s.db.CountUsers() > 0 {
		u, found := s.db.GetUser(sess.username)
		if !found || !u.Enabled {
			s.revokeUserSessions(sess.username)
			return consoleSession{}, false
		}
		sess.role = u.Role
	}
	if sess.role == "" {
		sess.role = "admin"
	}
	return sess, true
}

func (s *Server) revokeUserSessions(username string) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	for token, sess := range s.sessions {
		if sess.username == username {
			delete(s.sessions, token)
		}
	}
}

func (s *Server) validSession(r *http.Request) bool {
	_, ok := s.sessionFor(r)
	return ok
}

func adminOnlyPath(path string) bool {
	switch path {
	case "/api/v1/system/settings", "/api/v1/system/settings/rollback",
		"/api/v1/system/restart", "/api/v1/tts/config", "/api/v1/tts/test":
		return true
	}
	return strings.HasPrefix(path, "/api/v1/users") || strings.HasPrefix(path, "/api/v1/conference/rooms") || path == "/api/v1/conference/audio"
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
			sess, ok := s.sessionFor(r)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "UNAUTHORIZED", "message": "login required"})
				return
			}
			if adminOnlyPath(path) && sess.role != "admin" {
				writeJSON(w, http.StatusForbidden, map[string]any{"code": "FORBIDDEN", "message": "admin role required"})
				return
			}
			if sess.role == "viewer" && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && path != "/api/v1/me/password" {
				writeJSON(w, http.StatusForbidden, map[string]any{"code": "FORBIDDEN", "message": "viewer is read-only"})
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
	req.Username = strings.TrimSpace(req.Username)
	username, role, ok := "", "", false
	if s.db != nil && s.db.CountUsers() > 0 {
		if u, found := s.db.GetUser(req.Username); found && u.Enabled && auth.CheckPassword(u.PasswordHash, req.Password) {
			username, role, ok = u.Username, u.Role, true
			s.db.TouchUserLogin(u.Username)
		}
	} else {
		userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.consoleUser)) == 1
		if userOK && auth.CheckPassword(string(s.consoleHash), req.Password) {
			username, role, ok = s.consoleUser, "admin", true
		}
	}
	if !ok {
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
	if s.sessions == nil {
		s.sessions = map[string]consoleSession{}
	}
	s.sessions[token] = consoleSession{expires: time.Now().Add(sessionTTL), username: username, role: role}
	s.sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()), Secure: r.TLS != nil,
	})
	writeJSON(w, 200, map[string]any{"status": "ok", "user": username, "role": role})
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
	authRequired := s.authEnabled()
	if !authRequired {
		writeJSON(w, 200, map[string]any{"auth_required": false, "logged_in": true, "user": "admin", "role": "admin"})
		return
	}
	if sess, ok := s.sessionFor(r); ok {
		writeJSON(w, 200, map[string]any{"auth_required": true, "logged_in": true, "user": sess.username, "role": sess.role})
		return
	}
	writeJSON(w, 200, map[string]any{"auth_required": true, "logged_in": false, "user": "", "role": ""})
}
