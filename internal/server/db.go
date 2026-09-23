package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jan--f/docs-tree-test/internal/study"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE users (username TEXT PRIMARY KEY, password_hash BLOB NOT NULL, role TEXT NOT NULL CHECK(role IN ('owner','analyst')));
CREATE TABLE admin_sessions (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE, csrf TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE drafts (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, title TEXT NOT NULL, bundle_json TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE versions (id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, title TEXT NOT NULL, slug TEXT NOT NULL, snapshot_json TEXT NOT NULL, scoring_policy TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TRIGGER versions_immutable_update BEFORE UPDATE ON versions BEGIN SELECT RAISE(ABORT,'versions are immutable'); END;
CREATE TRIGGER versions_immutable_delete BEFORE DELETE ON versions BEGIN SELECT RAISE(ABORT,'versions are immutable'); END;
CREATE TABLE runs (id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE, version_id TEXT NOT NULL REFERENCES versions(id), mode TEXT NOT NULL CHECK(mode IN ('pilot','real')), status TEXT NOT NULL CHECK(status IN ('open','paused','closed')), created_at TEXT NOT NULL, arms_json TEXT NOT NULL, tasks_json TEXT NOT NULL, allocation_json TEXT NOT NULL DEFAULT '[]');
CREATE TABLE sessions (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), identity_hash TEXT NOT NULL, variant_id TEXT NOT NULL, panel_index INTEGER NOT NULL, tasks_json TEXT NOT NULL, nodes_json TEXT NOT NULL, public_tasks_json TEXT NOT NULL, experience TEXT NOT NULL, docs_familiarity TEXT NOT NULL, task_index INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, completed_at TEXT, UNIQUE(run_id,identity_hash));
CREATE TABLE attempts (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), task_index INTEGER NOT NULL, task_id TEXT NOT NULL, started_at TEXT NOT NULL, next_seq INTEGER NOT NULL DEFAULT 1, finished_at TEXT, outcome TEXT, selected_node TEXT, correct INTEGER, navigation_ms INTEGER, direct INTEGER NOT NULL DEFAULT 0, backtracks INTEGER NOT NULL DEFAULT 0, UNIQUE(session_id,task_index));
CREATE TABLE events (attempt_id TEXT NOT NULL REFERENCES attempts(id), seq INTEGER NOT NULL, id TEXT NOT NULL, payload_json TEXT NOT NULL, received_at TEXT NOT NULL, PRIMARY KEY(attempt_id,seq), UNIQUE(attempt_id,id));
CREATE TABLE commands (session_id TEXT NOT NULL REFERENCES sessions(id), command_id TEXT NOT NULL, operation TEXT NOT NULL, request_hash TEXT NOT NULL, response_json TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(session_id,command_id));
CREATE INDEX sessions_run ON sessions(run_id);
CREATE INDEX attempts_session ON attempts(session_id);
CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at);
PRAGMA user_version=1;
`

const migration2 = `
ALTER TABLE versions ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN policy_json TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN server_elapsed_ms INTEGER;
ALTER TABLE attempts ADD COLUMN observed_navigation_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE attempts ADD COLUMN timing_quality TEXT NOT NULL DEFAULT 'not_started';
ALTER TABLE attempts ADD COLUMN clock_epochs INTEGER NOT NULL DEFAULT 0;
PRAGMA user_version=2;
`

// Open creates or opens a durable SQLite database and installs the API routes.
func Open(dbPath string, assets fs.FS, publicURL string) (*Server, error) {
	s := &Server{assets: assets}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("public URL must be an http(s) origin")
		}
		s.origin, s.secure = u.Scheme+"://"+u.Host, u.Scheme == "https"
	}
	var dsn string
	if dbPath == ":memory:" {
		dsn = "file:" + randomID() + "?mode=memory&cache=shared"
	} else {
		absolute, err := filepath.Abs(dbPath)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: absolute}).String() + "?"
	}
	dsn += "&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer connection also keeps read/modify/write transactions predictable.
	// SQLite's immediate transactions serialize independent server processes too.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s.db = db
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) migrate() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return fmt.Errorf("database schema %d is newer than supported schema 2", version)
	}
	if version == 0 {
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		version = 1
	}
	if version == 1 {
		if _, err := tx.Exec(migration2); err != nil {
			return fmt.Errorf("migrate schema 2: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Server) Close() error { return s.db.Close() }

// Backup uses SQLite's consistent online VACUUM INTO snapshot, including WAL data.
// A destination must not already exist, so an earlier backup cannot be destroyed.
func (s *Server) Backup(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = s.db.Exec("VACUUM INTO '" + strings.ReplaceAll(abs, "'", "''") + "'")
	if err != nil {
		_ = os.Remove(abs)
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Import validates and freezes a source bundle, parsed tree and scoring policy.
func (s *Server) Import(bundle study.Bundle) (string, error) {
	snapshot, err := study.Validate(bundle)
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := insertVersion(context.Background(), tx, snapshot)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func insertVersion(ctx context.Context, q querier, snapshot *study.Snapshot) (string, error) {
	return insertVersionWithPolicy(ctx, q, snapshot, currentPolicy())
}

func insertVersionWithPolicy(ctx context.Context, q querier, snapshot *study.Snapshot, policy Policy) (string, error) {
	if _, err := resolvePolicy(policy); err != nil {
		return "", err
	}
	hash := publicationHash(snapshot.Hash, policy)
	var id string
	err := q.QueryRowContext(ctx, "SELECT id FROM versions WHERE hash=?", hash).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	id = randomID()
	_, err = q.ExecContext(ctx, "INSERT INTO versions(id,hash,content_hash,title,slug,snapshot_json,scoring_policy,created_at) VALUES(?,?,?,?,?,?,?,?)", id, hash, snapshot.Hash, snapshot.Bundle.Config.Title, snapshot.Bundle.Config.Slug, string(data), policyJSON(policy), now())
	return id, err
}

func loadSnapshot(ctx context.Context, q querier, version string) (*frozenSnapshot, error) {
	var data, contentHash, hash, policyData string
	if err := q.QueryRowContext(ctx, "SELECT snapshot_json,content_hash,hash,scoring_policy FROM versions WHERE id=?", version).Scan(&data, &contentHash, &hash, &policyData); err != nil {
		if err == sql.ErrNoRows {
			return nil, problem(404, "version not found")
		}
		return nil, err
	}
	policy, err := decodePolicy(policyData)
	if err != nil {
		return nil, err
	}
	snapshot := &frozenSnapshot{PublicationHash: hash, Policy: policy}
	if err := json.Unmarshal([]byte(data), &snapshot.Snapshot); err != nil {
		return nil, err
	}
	content, err := json.Marshal(snapshot.Bundle)
	if err != nil {
		return nil, err
	}
	if contentHash != snapshot.Hash || digest(string(content)) != contentHash || publicationHash(contentHash, policy) != hash {
		return nil, problem(409, "frozen publication hash does not match its content and policy")
	}
	return snapshot, nil
}
