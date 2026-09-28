package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
