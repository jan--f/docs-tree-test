package server

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const participantCookie = "tree_participant"
const adminCookie = "tree_admin"

const (
	minAdminPasswordBytes = 15
	adminBcryptCost       = 12
	maxLoginFailures      = 10000
	loginFailureLifetime  = 24 * time.Hour
	dummyBcryptCost12     = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
)

type adminIdentity struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	Role          string `json:"role,omitempty"`
	CSRF          string `json:"csrf_token,omitempty"`
}

type identityKey struct{}

type loginFailure struct {
	failures int
	until    time.Time
	touched  time.Time
	entry    *list.Element
}

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string]*loginFailure
	order    *list.List
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string]*loginFailure), order: list.New()}
}

func (l *loginLimiter) key(source, username string) string {
	return digest(source + "\x00" + strings.ToLower(strings.TrimSpace(username)))
}

// remove is called with mu held. The list is ordered by the last failure, so
// expiry and capacity eviction do not scan the whole cache on each login.
func (l *loginLimiter) remove(key string) {
	if v := l.failures[key]; v != nil {
		l.order.Remove(v.entry)
		delete(l.failures, key)
	}
}

func (l *loginLimiter) blocked(source, username string) time.Duration {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	key := l.key(source, username)
	v := l.failures[key]
	if v == nil {
		return 0
	}
	if now.Sub(v.touched) >= loginFailureLifetime {
		l.remove(key)
		return 0
	}
	if !now.Before(v.until) {
		return 0
	}
	return v.until.Sub(now)
}

func (l *loginLimiter) failed(source, username string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for oldest := l.order.Front(); oldest != nil; oldest = l.order.Front() {
		key := oldest.Value.(string)
		if now.Sub(l.failures[key].touched) < loginFailureLifetime {
			break
		}
		l.remove(key)
	}
	key := l.key(source, username)
	v := l.failures[key]
	if v == nil {
		if len(l.failures) >= maxLoginFailures {
			l.remove(l.order.Front().Value.(string))
		}
		v = &loginFailure{entry: l.order.PushBack(key)}
		l.failures[key] = v
	} else {
		l.order.MoveToBack(v.entry)
	}
	v.failures++
	v.touched = now
	// A bounded delay slows guessing without enabling a permanent-lockout DoS.
	switch {
	case v.failures >= 12:
		v.until = now.Add(15 * time.Minute)
	case v.failures >= 8:
		v.until = now.Add(5 * time.Minute)
	case v.failures >= 5:
		v.until = now.Add(30 * time.Second)
	}
}

func (l *loginLimiter) succeeded(source, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remove(l.key(source, username))
}

// SetUser creates or replaces a CLI-managed account and revokes its sessions.
func (s *Server) SetUser(username, password, role string) error {
	if strings.TrimSpace(username) != username || len(username) < 1 || len(username) > 128 {
		return errors.New("username must contain 1–128 characters without surrounding whitespace")
	}
	if len(password) < minAdminPasswordBytes || len(password) > 72 {
		return errors.New("password must contain 15–72 bytes")
	}
	if role != "owner" && role != "analyst" {
		return errors.New("role must be owner or analyst")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), adminBcryptCost)
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(nil, username, "account.set", "user:"+username, "success")
	return nil
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
	return t, nil
}

func (s *Server) participantIdentity(runID, participantToken string) string {
	mac := hmac.New(sha256.New, s.identityKey)
	_, _ = mac.Write([]byte(runID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(participantToken))
	return hex.EncodeToString(mac.Sum(nil))
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
	if err := decodeLimit(w, r, &req, 8<<10); err != nil {
		return err
	}
	source := s.sourceNetwork(r)
	if wait, ok := s.limits.allow("admin:login:source:"+source, 30, time.Minute); !ok {
		s.audit(r, boundedAuditName(req.Username), "login", "admin", "rate_limited")
		return retryAfter(w, wait)
	}
	if len(req.Username) < 1 || len(req.Username) > 128 || strings.TrimSpace(req.Username) != req.Username {
		s.audit(r, boundedAuditName(req.Username), "login", "admin", "failure")
		return problem(401, "invalid username or password")
	}
	// A suspended source must not spend the account's shared login budget.
	if wait := s.loginLimits.blocked(source, req.Username); wait > 0 {
		s.audit(r, boundedAuditName(req.Username), "login", "admin", "rate_limited")
		return retryAfter(w, wait)
	}
	if wait, ok := s.limits.allow("admin:login:account:"+strings.ToLower(strings.TrimSpace(req.Username)), 10, time.Minute); !ok {
		s.audit(r, boundedAuditName(req.Username), "login", "admin", "rate_limited")
		return retryAfter(w, wait)
	}
	var hash []byte
	var role string
	err := s.db.QueryRowContext(r.Context(), "SELECT password_hash,role FROM users WHERE username=?", req.Username).Scan(&hash, &role)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	// Spend the same bcrypt work for unknown users to avoid a username oracle.
	if err == sql.ErrNoRows {
		hash = []byte(dummyBcryptCost12)
	}
	verified := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) == nil
	if !verified || role == "" {
		s.loginLimits.failed(source, req.Username)
		s.audit(r, boundedAuditName(req.Username), "login", "admin", "failure")
		return problem(401, "invalid username or password")
	}
	t, csrf, err := s.createAdminSession(r.Context(), req.Username, hash, role, token(r, adminCookie))
	if err != nil {
		return err
	}
	s.cookie(w, adminCookie, t, 12*60*60)
	s.loginLimits.succeeded(source, req.Username)
	s.audit(r, req.Username, "login", "admin", "success")
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
	s.audit(r, adminFromContext(r).Username, "logout", "admin", "success")
	return respond(w, map[string]bool{"ok": true})
}
