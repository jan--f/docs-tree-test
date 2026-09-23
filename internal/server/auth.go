package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const participantCookie = "tree_participant"
const adminCookie = "tree_admin"

type adminIdentity struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	Role          string `json:"role,omitempty"`
	CSRF          string `json:"csrf_token,omitempty"`
}

type identityKey struct{}

// SetUser creates or replaces a CLI-managed account and revokes its sessions.
func (s *Server) SetUser(username, password, role string) error {
	if strings.TrimSpace(username) != username || len(username) < 1 || len(username) > 128 {
		return errors.New("username must contain 1–128 characters without surrounding whitespace")
	}
	if len(password) < 8 || len(password) > 72 {
		return errors.New("password must contain 8–72 bytes")
	}
	if role != "owner" && role != "analyst" {
		return errors.New("role must be owner or analyst")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO users(username,password_hash,role) VALUES(?,?,?) ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash,role=excluded.role", username, hash, role); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM admin_sessions WHERE username=?", username); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func token(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil || len(c.Value) != 43 {
		return ""
	}
	for _, c := range c.Value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return ""
		}
	}
	return c.Value
}

func anonymous(r *http.Request) (string, error) {
	t := token(r, participantCookie)
	if t == "" {
		return "", problem(401, "open the study to establish an anonymous session")
	}
	return digest(t), nil
}

func (s *Server) admin(r *http.Request) (adminIdentity, error) {
	a := adminIdentity{}
	t := token(r, adminCookie)
	if t == "" {
		return a, nil
	}
	err := s.db.QueryRowContext(r.Context(), "SELECT u.username,u.role,a.csrf FROM admin_sessions a JOIN users u ON u.username=a.username WHERE a.token_hash=? AND a.expires_at>?", digest(t), time.Now().UTC().Format(time.RFC3339)).Scan(&a.Username, &a.Role, &a.CSRF)
	if err == sql.ErrNoRows {
		return adminIdentity{}, nil
	}
	if err != nil {
		return a, err
	}
	a.Authenticated = true
	return a, nil
}

func (s *Server) authorize(owner bool, next handler) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		a, err := s.admin(r)
		if err != nil {
			return err
		}
		if !a.Authenticated {
			return problem(401, "administrator login required")
		}
		if owner && a.Role != "owner" {
			return problem(403, "owner role required")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.CSRF)) != 1 {
			return problem(403, "invalid CSRF token")
		}
		return next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, a)))
	}
}

func (s *Server) adminSession(w http.ResponseWriter, r *http.Request) error {
	a, err := s.admin(r)
	if err != nil {
		return err
	}
	return respond(w, a)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	var hash []byte
	var role string
	err := s.db.QueryRowContext(r.Context(), "SELECT password_hash,role FROM users WHERE username=?", req.Username).Scan(&hash, &role)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	// Spend the same bcrypt work for unknown users to avoid a username oracle.
	if err == sql.ErrNoRows {
		hash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || role == "" {
		return problem(401, "invalid username or password")
	}
	t, csrf, err := s.createAdminSession(r.Context(), req.Username, hash, role, token(r, adminCookie))
	if err != nil {
		return err
	}
	s.cookie(w, adminCookie, t, 12*60*60)
	return respond(w, adminIdentity{true, req.Username, role, csrf})
}

// Recheck the verified credentials under the same write transaction that creates
// the session. SetUser cannot reset/revoke between this check and the INSERT.
func (s *Server) createAdminSession(ctx context.Context, username string, verifiedHash []byte, verifiedRole, oldToken string) (string, string, error) {
	t, csrf := randomID(), randomID()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var currentHash []byte
	var currentRole string
	err = tx.QueryRowContext(ctx, "SELECT password_hash,role FROM users WHERE username=?", username).Scan(&currentHash, &currentRole)
	if err == sql.ErrNoRows {
		return "", "", problem(401, "credentials changed; log in again")
	}
	if err != nil {
		return "", "", err
	}
	if subtle.ConstantTimeCompare(currentHash, verifiedHash) != 1 || currentRole != verifiedRole {
		return "", "", problem(401, "credentials changed; log in again")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE expires_at<=? OR token_hash=?", time.Now().UTC().Format(time.RFC3339), digest(oldToken)); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO admin_sessions(token_hash,username,csrf,expires_at) VALUES(?,?,?,?)", digest(t), username, csrf, time.Now().UTC().Add(12*time.Hour).Format(time.RFC3339)); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return t, csrf, nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM admin_sessions WHERE token_hash=?", digest(token(r, adminCookie))); err != nil {
		return err
	}
	s.cookie(w, adminCookie, "", -1)
	return respond(w, map[string]bool{"ok": true})
}
