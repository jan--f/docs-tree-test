package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jan--f/docs-tree-test/internal/server"
	"github.com/jan--f/docs-tree-test/internal/study"
)

func TestIdentityKeyCommandGeneratesExternalKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	db, keyFile := filepath.Join(dir, "study.sqlite"), filepath.Join(dir, "identity.key")
	if err := run([]string{"identity-key", "--out", keyFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("treetest.sqlite"); !os.IsNotExist(err) {
		t.Fatal("key generation opened the default database")
	}
	key, err := loadIdentityKey(keyFile)
	if err != nil || len(key) != 32 {
		t.Fatalf("generated key cannot be loaded: %v", err)
	}
	info, err := os.Stat(keyFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file is not private: %v %v", info, err)
	}
	if err := run([]string{"identity-key", "--out", keyFile}); err == nil {
		t.Fatal("identity-key command overwrote its output")
	}
	if after, err := loadIdentityKey(keyFile); err != nil || !bytes.Equal(key, after) {
		t.Fatal("failed key generation changed the key file")
	}
	if err := run([]string{"identity-key", "--db", db, "--out", filepath.Join(dir, "another.key")}); err == nil {
		t.Fatal("key generation accepted a database export flag")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("key generation created a database")
	}
	backup := filepath.Join(dir, "backup.sqlite")
	if err := run([]string{"backup", "--db", db, "--identity-key-file", keyFile, "--out", backup}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"backup", "--db", backup, "--identity-key-file", keyFile, "--out", filepath.Join(dir, "restored.sqlite")}); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing-key.sqlite")
	if err := run([]string{"backup", "--db", db, "--out", missing}); err == nil {
		t.Fatal("external-key database opened without its key file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("failed command created an output file")
	}
}

func TestListAndRemoveStudyCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "study.sqlite")
	s, err := server.Open(db, nil, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	bundle := study.Bundle{Config: study.Config{
		SchemaVersion: 1, Slug: "example", Title: "Example", TasksPerSession: 3,
		Variants: []study.Variant{{ID: "old", Name: "Old", Tree: "old.md"}, {ID: "new", Name: "New", Tree: "new.md"}},
		Tasks: []study.Task{
			{ID: "a", Prompt: "Find A", Difficulty: "easy", Answers: map[string][]string{"old": {"one"}, "new": {"one"}}},
			{ID: "b", Prompt: "Find B", Difficulty: "medium", Answers: map[string][]string{"old": {"two"}, "new": {"two"}}},
			{ID: "c", Prompt: "Find C", Difficulty: "hard", Answers: map[string][]string{"old": {"three"}, "new": {"three"}}},
		}, Panels: [][]string{{"a", "b", "c"}},
	}, Trees: map[string]string{
		"old.md": "- [A](page:a/one)\n- [B](page:b/two)\n- [C](page:c/three)\n",
		"new.md": "- [A](page:a/one)\n- [B](page:b/two)\n- [C](page:c/three)\n",
	}}
	id, err := s.Import(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	listErr := run([]string{"list", "--db", db})
	os.Stdout = stdout
	writer.Close()
	output, readErr := io.ReadAll(reader)
	reader.Close()
	if listErr != nil || readErr != nil || !strings.Contains(string(output), id+"\texample\t\"Example\"\t0\t") {
		t.Fatalf("list did not show the imported study: %s (%v, %v)", output, listErr, readErr)
	}
	if err := run([]string{"remove", "--db", db}); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("missing version: %v", err)
	}
	if err := run([]string{"remove", "--db", db, "--version", "not-found", "--confirm"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown version: %v", err)
	}
	if err := run([]string{"remove", "--db", db, "--version", id}); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(db, nil, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetStudy(context.Background(), id); err != nil {
		t.Fatalf("dry run deleted version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"remove", "--db", db, "--version", id, "--confirm"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"list", "--db", db}); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(db, nil, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.GetStudy(context.Background(), id); err == nil {
		t.Fatal("confirmed removal left the version in the database")
	}
}
