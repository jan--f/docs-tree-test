package server

import (
	"context"
	"database/sql"
	"fmt"
)

// PublishedStudy describes a frozen study version without exposing its source
// bundle or answer keys. Runs counts every run, including closed and purged ones.
type PublishedStudy struct {
	ID        string
	Slug      string
	Title     string
	Hash      string
	CreatedAt string
	Runs      int
}

const studyQuery = `SELECT v.id,v.slug,v.title,v.hash,v.created_at,
	(SELECT COUNT(*) FROM runs r WHERE r.version_id=v.id) FROM versions v`

func scanStudy(row interface{ Scan(...any) error }) (PublishedStudy, error) {
	var study PublishedStudy
	err := row.Scan(&study.ID, &study.Slug, &study.Title, &study.Hash, &study.CreatedAt, &study.Runs)
	return study, err
}

func (s *Server) ListStudies(ctx context.Context) ([]PublishedStudy, error) {
	rows, err := s.db.QueryContext(ctx, studyQuery+" ORDER BY v.created_at DESC,v.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	studies := []PublishedStudy{}
	for rows.Next() {
		study, err := scanStudy(rows)
		if err != nil {
			return nil, err
		}
		studies = append(studies, study)
	}
	return studies, rows.Err()
}

func (s *Server) GetStudy(ctx context.Context, id string) (PublishedStudy, error) {
	study, err := scanStudy(s.db.QueryRowContext(ctx, studyQuery+" WHERE v.id=?", id))
	if err == sql.ErrNoRows {
		return study, fmt.Errorf("study version %q not found", id)
	}
	return study, err
}

// RemoveStudy deletes only a published version that has never been used by a
// run. The immutability trigger is lifted inside the same write transaction and
// restored before commit; rollback restores it on any failure. The run check and
// deletion share the write lock, so a concurrent run cannot appear between them.
func (s *Server) RemoveStudy(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("study version ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runs int
	err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM runs WHERE version_id=?) FROM versions WHERE id=?", id, id).Scan(&runs)
	if err == sql.ErrNoRows {
		return fmt.Errorf("study version %q not found", id)
	}
	if err != nil {
		return err
	}
	if runs != 0 {
		return fmt.Errorf("study version %q has %d run(s); cannot remove a version used by a run", id, runs)
	}
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER versions_immutable_delete"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM versions WHERE id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CREATE TRIGGER versions_immutable_delete BEFORE DELETE ON versions BEGIN SELECT RAISE(ABORT,'versions are immutable'); END"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(nil, "system", "version.remove", "version:"+id, "success")
	return nil
}
