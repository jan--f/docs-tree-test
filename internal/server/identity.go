package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

const participantIdentityKeyName = "participant-identity-v1"
const participantIdentityFingerprintName = "participant-identity-v1-fingerprint"

func (s *Server) setIdentityKey(tx *sql.Tx, key []byte, newDatabase bool) error {
	external := len(key) != 0
	if external && len(key) != 32 {
		return errors.New("participant identity key must be exactly 32 bytes")
	}
	var stored, fingerprint []byte
	err := tx.QueryRow("SELECT value FROM server_secrets WHERE name=?", participantIdentityKeyName).Scan(&stored)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && len(stored) != 32 {
		return errors.New("stored participant identity key has invalid length")
	}
	err = tx.QueryRow("SELECT value FROM server_secrets WHERE name=?", participantIdentityFingerprintName).Scan(&fingerprint)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if !newDatabase && len(fingerprint) != sha256.Size {
		return errors.New("stored participant identity key fingerprint has invalid length")
	}
	if external && stored != nil {
		return errors.New("this database uses a local participant identity key; recreate the database to use --identity-key-file")
	}
	if !external {
		key = stored
		if key == nil {
			if !newDatabase {
				return errors.New("this database requires --identity-key-file with its original participant identity key")
			}
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO server_secrets(name,value) VALUES(?,?)", participantIdentityKeyName, key); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "treetest: using database-local participant identity key; create production databases with --identity-key-file")
		}
	}
	sum := sha256.Sum256(key)
	if newDatabase {
		if _, err := tx.Exec("INSERT INTO server_secrets(name,value) VALUES(?,?)", participantIdentityFingerprintName, sum[:]); err != nil {
			return err
		}
	} else if subtle.ConstantTimeCompare(fingerprint, sum[:]) != 1 {
		return errors.New("participant identity key does not match this database; use the original key or recreate the database")
	}
	s.identityKey = append([]byte(nil), key...)
	return nil
}
