package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mcs/internal/auth"
	"mcs/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{2,32}$`)

type userView struct {
	Username    string `json:"username"`
	Role        string `json:"role"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

func validRole(role string) bool {
	return role == "admin" || role == "operator" || role == "viewer"
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "user database unavailable"})
		return
	}
	users := s.db.ListUsers()
	items := make([]userView, 0, len(users))
	for _, u := range users {
		items = append(items, userView{Username: u.Username, Role: u.Role, Enabled: u.Enabled, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt})
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "user database unavailable"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernamePattern.MatchString(req.Username) {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "用户名需为 2-32 位字母、数字、_ . -"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "密码至少 8 位"})
		return
	}
	if req.Role == "" {
		req.Role = "operator"
	}
	if !validRole(req.Role) {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "角色必须是 admin/operator/viewer"})
		return
	}
	if _, ok := s.db.GetUser(req.Username); ok {
		writeJSON(w, 409, map[string]any{"code": "CONFLICT", "message": "用户已存在"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	now := time.Now().Format(time.RFC3339)
	if err := s.db.CreateUser(store.User{Username: req.Username, PasswordHash: hash, Role: req.Role, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	writeJSON(w, 201, userView{Username: req.Username, Role: req.Role, Enabled: true, CreatedAt: now})
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "user database unavailable"})
		return
	}
	target := r.PathValue("username")
	current, _ := s.sessionFor(r)
	u, ok := s.db.GetUser(target)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "用户不存在"})
		return
	}
	var req struct {
		Role     *string `json:"role"`
		Enabled  *bool   `json:"enabled"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	newRole := u.Role
	if req.Role != nil {
		if !validRole(*req.Role) {
			writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "角色必须是 admin/operator/viewer"})
			return
		}
		newRole = *req.Role
	}
	newEnabled := u.Enabled
	if req.Enabled != nil {
		newEnabled = *req.Enabled
	}
	if target == current.username && (!newEnabled || newRole != "admin") {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "不能禁用自己或移除自己的管理员角色"})
		return
	}
	if u.Role == "admin" && u.Enabled && (newRole != "admin" || !newEnabled) && s.db.CountRole("admin") <= 1 {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "至少保留一个启用的管理员"})
		return
	}
	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < 8 {
			writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "密码至少 8 位"})
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
			return
		}
		u.PasswordHash = hash
	}
	u.Role = newRole
	u.Enabled = newEnabled
	u.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := s.db.UpdateUser(u); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	if req.Password != nil && *req.Password != "" {
		s.revokeUserSessions(u.Username)
	}
	writeJSON(w, 200, userView{Username: u.Username, Role: u.Role, Enabled: u.Enabled, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt})
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "user database unavailable"})
		return
	}
	target := r.PathValue("username")
	current, _ := s.sessionFor(r)
	if target == current.username {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "不能删除当前登录用户"})
		return
	}
	u, ok := s.db.GetUser(target)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "用户不存在"})
		return
	}
	if u.Role == "admin" && u.Enabled && s.db.CountRole("admin") <= 1 {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "至少保留一个启用的管理员"})
		return
	}
	if err := s.db.DeleteUser(target); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	s.revokeUserSessions(target)
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) mePassword(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "user database unavailable"})
		return
	}
	sess, ok := s.sessionFor(r)
	if !ok {
		writeJSON(w, 401, map[string]any{"code": "UNAUTHORIZED", "message": "login required"})
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.NewPassword) < 8 {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "新密码至少 8 位"})
		return
	}
	u, ok := s.db.GetUser(sess.username)
	if !ok || !auth.CheckPassword(u.PasswordHash, req.OldPassword) {
		writeJSON(w, 401, map[string]any{"code": "UNAUTHORIZED", "message": "原密码错误"})
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	u.PasswordHash = hash
	u.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := s.db.UpdateUser(u); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	s.revokeUserSessions(u.Username)
	writeJSON(w, 200, map[string]any{"status": "ok"})
}
