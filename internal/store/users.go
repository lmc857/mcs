package store

import (
	"database/sql"
	"errors"
	"time"
)

type User struct {
	Username     string
	PasswordHash string
	Role         string
	Enabled      bool
	CreatedAt    string
	UpdatedAt    string
	LastLoginAt  string
}

func (d *DB) CountUsers() int {
	if d == nil {
		return 0
	}
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func (d *DB) CountRole(role string) int {
	if d == nil {
		return 0
	}
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM users WHERE role=? AND enabled=1`, role).Scan(&n)
	return n
}

func (d *DB) GetUser(username string) (User, bool) {
	if d == nil {
		return User{}, false
	}
	var u User
	var enabled int
	var last sql.NullString
	err := d.sql.QueryRow(`SELECT username,password_hash,role,enabled,created_at,updated_at,last_login_at FROM users WHERE username=?`, username).
		Scan(&u.Username, &u.PasswordHash, &u.Role, &enabled, &u.CreatedAt, &u.UpdatedAt, &last)
	if err != nil {
		return User{}, false
	}
	u.Enabled = enabled != 0
	if last.Valid {
		u.LastLoginAt = last.String
	}
	return u, true
}

func (d *DB) ListUsers() []User {
	if d == nil {
		return nil
	}
	rows, err := d.sql.Query(`SELECT username,password_hash,role,enabled,created_at,updated_at,last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var enabled int
		var last sql.NullString
		if rows.Scan(&u.Username, &u.PasswordHash, &u.Role, &enabled, &u.CreatedAt, &u.UpdatedAt, &last) == nil {
			u.Enabled = enabled != 0
			if last.Valid {
				u.LastLoginAt = last.String
			}
			out = append(out, u)
		}
	}
	return out
}

func (d *DB) CreateUser(u User) error {
	if d == nil {
		return errors.New("no database")
	}
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if u.UpdatedAt == "" {
		u.UpdatedAt = u.CreatedAt
	}
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	_, err := d.sql.Exec(`INSERT INTO users(username,password_hash,role,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.Role, enabled, u.CreatedAt, u.UpdatedAt)
	return err
}

func (d *DB) UpdateUser(u User) error {
	if d == nil {
		return errors.New("no database")
	}
	if u.UpdatedAt == "" {
		u.UpdatedAt = time.Now().Format(time.RFC3339)
	}
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	res, err := d.sql.Exec(`UPDATE users SET password_hash=?,role=?,enabled=?,updated_at=? WHERE username=?`,
		u.PasswordHash, u.Role, enabled, u.UpdatedAt, u.Username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) DeleteUser(username string) error {
	if d == nil {
		return errors.New("no database")
	}
	_, err := d.sql.Exec(`DELETE FROM users WHERE username=?`, username)
	return err
}

func (d *DB) TouchUserLogin(username string) {
	if d == nil {
		return
	}
	_, _ = d.sql.Exec(`UPDATE users SET last_login_at=? WHERE username=?`, time.Now().Format(time.RFC3339), username)
}
