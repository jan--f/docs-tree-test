package server

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityKeyModeAndProgressSurviveRestartAndBackup(t *testing.T) {
	for _, mode := range []string{"local", "external"} {
		t.Run(mode, func(t *testing.T) {
			options := Options{}
			if mode == "external" {
				options.IdentityKey = bytes.Repeat([]byte{42}, 32)
			}
			e := setupWithOptions(t, options)
			key := bytes.Clone(e.s.identityKey)
			c := participant(t, e.s)
			enroll(t, c)
			a := startTask(t, c)
			call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{event(1, "tree_shown", "", 0, "before-restart")}}, 200)
			before := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200)
			backup := filepath.Join(t.TempDir(), "backup.sqlite")
			if err := e.s.Backup(backup); err != nil {
				t.Fatal(err)
			}
			if mode == "external" {
				if count(t, e.s, "SELECT COUNT(*) FROM server_secrets WHERE name=?", participantIdentityKeyName) != 0 {
					t.Fatal("external identity key was stored in the database")
				}
				for _, path := range []string{e.db, e.db + "-wal", backup} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(data, key) {
						t.Fatalf("external identity key appears in %s", path)
					}
				}
			}
			if err := e.s.Close(); err != nil {
				t.Fatal(err)
			}
			wrong := bytes.Clone(key)
			wrong[0] ^= 1
			invalidKeys := [][]byte{nil, wrong}
			if mode == "local" {
				// Even supplying the exact local key cannot externalize this database.
				invalidKeys = [][]byte{key, wrong}
			}
			for _, path := range []string{e.db, backup} {
				for _, invalid := range invalidKeys {
					if bad, err := OpenWithOptions(path, testAssets, testOrigin, Options{IdentityKey: invalid}); err == nil {
						bad.Close()
						t.Fatal("restart or restore accepted a different key or storage mode")
					} else if mode == "local" && !strings.Contains(err.Error(), "recreate the database") {
						t.Fatalf("missing recreation instruction: %v", err)
					}
				}
				restored, err := OpenWithOptions(path, testAssets, testOrigin, options)
				if err != nil {
					t.Fatal(err)
				}
				defer restored.Close()
				c.s = restored
				if !bytes.Equal(restored.identityKey, key) {
					t.Fatal("restart or restore changed the identity key")
				}
				if got := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200); !reflect.DeepEqual(got, before) {
					t.Fatal("restart or restore changed saved participant progress")
				}
				if got := enroll(t, c); !reflect.DeepEqual(got, before) || count(t, restored, "SELECT COUNT(*) FROM sessions") != 1 {
					t.Fatal("restart or restore created a duplicate enrollment")
				}
			}
		})
	}
}

func TestIdentityKeyModeIsFixedBeforeEnrollment(t *testing.T) {
	for _, mode := range []string{"local", "external"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "study.sqlite")
			options := Options{}
			if mode == "external" {
				options.IdentityKey = bytes.Repeat([]byte{42}, 32)
			}
			s, err := OpenWithOptions(path, nil, testOrigin, options)
			if err != nil {
				t.Fatal(err)
			}
			invalid := [][]byte{nil, bytes.Repeat([]byte{43}, 32)}
			if mode == "local" {
				invalid = [][]byte{bytes.Clone(s.identityKey), bytes.Repeat([]byte{43}, 32)}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			for _, key := range invalid {
				if bad, err := OpenWithOptions(path, nil, testOrigin, Options{IdentityKey: key}); err == nil {
					bad.Close()
					t.Fatal("empty database accepted a different key or storage mode")
				}
			}
			// Missing metadata on an existing database must not generate a new key.
			s, err = OpenWithOptions(path, nil, testOrigin, options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("DELETE FROM server_secrets"); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if bad, err := OpenWithOptions(path, nil, testOrigin, options); err == nil {
				bad.Close()
				t.Fatal("existing database silently regenerated identity metadata")
			}
		})
	}
}
