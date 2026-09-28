package server

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncompatibleDatabaseRequiresRecreation(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 4, 5, 6, schemaVersion + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "incompatible.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(fmt.Sprintf("CREATE TABLE existing_data (value TEXT); INSERT INTO existing_data VALUES('preserve me'); PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(path, nil, testOrigin); err == nil {
				s.Close()
				t.Fatal("incompatible schema was accepted")
			} else if !strings.Contains(err.Error(), "recreate the database") {
				t.Fatalf("missing recreation instruction: %v", err)
			}
			var after int
			if err := db.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
				t.Fatalf("rejected database changed version: %d %v", after, err)
			}
			var value string
			if err := db.QueryRow("SELECT value FROM existing_data").Scan(&value); err != nil || value != "preserve me" {
				t.Fatalf("rejected database changed contents: %q %v", value, err)
			}
		})
	}
}

func TestDatabaseInitializationIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.sqlite")
	if s, err := OpenWithOptions(path, nil, testOrigin, Options{IdentityKey: []byte("too short")}); err == nil {
		s.Close()
		t.Fatal("invalid key was accepted")
	}
	s, err := Open(path, nil, testOrigin)
	if err != nil {
		t.Fatalf("failed initialization left a partial schema: %v", err)
	}
	defer s.Close()
	if count(t, s, "PRAGMA user_version") != schemaVersion {
		t.Fatal("fresh database has the wrong schema version")
	}
}
