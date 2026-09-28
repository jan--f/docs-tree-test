package server

import (
	"context"
	"database/sql"
	"fmt"
)

// PurgePreview deliberately includes only aggregate counts so the CLI can
// show a safe dry run without exposing participant data.
type PurgePreview struct {
	RunID       string
	Slug        string
	Sessions    int
	Attempts    int
	Events      int
	Receipts    int
	Invitations int
}

func (s *Server) PurgeCandidates(ctx context.Context, runID string, overdueOnly bool) ([]PurgePreview, error) {
	where := "r.status='closed'"
	args := []any{}
	if runID != "" {
		where += " AND r.id=?"
		args = append(args, runID)
	} else {
		where += " AND r.purged_at IS NULL"
	}
	if overdueOnly {
		where += " AND (r.purge_pending=1 OR r.retention_target_at<=?)"
		args = append(args, now())
	}
	rows, err := s.db.QueryContext(ctx, "SELECT r.id,r.slug,(SELECT COUNT(*) FROM sessions s WHERE s.run_id=r.id),(SELECT COUNT(*) FROM attempts a JOIN sessions s ON s.id=a.session_id WHERE s.run_id=r.id),(SELECT COUNT(*) FROM events e JOIN attempts a ON a.id=e.attempt_id JOIN sessions s ON s.id=a.session_id WHERE s.run_id=r.id),(SELECT COUNT(*) FROM commands c JOIN sessions s ON s.id=c.session_id WHERE s.run_id=r.id),(SELECT COUNT(*) FROM invitations i WHERE i.run_id=r.id) FROM runs r WHERE "+where+" ORDER BY r.created_at,r.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PurgePreview{}
	for rows.Next() {
		var p PurgePreview
		if err := rows.Scan(&p.RunID, &p.Slug, &p.Sessions, &p.Attempts, &p.Events, &p.Receipts, &p.Invitations); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Server) PurgeRun(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM runs WHERE id=?", runID).Scan(&status); err == sql.ErrNoRows {
		return problem(404, "run not found")
	} else if err != nil {
		return err
	}
	if status != "closed" {
		return problem(409, "only closed runs can be purged")
	}
	// A committed deletion is not a completed purge until free pages and old
	// WAL frames are removed. Pending runs remain closed and can be retried.
	statements := []string{
		"DELETE FROM events WHERE attempt_id IN (SELECT a.id FROM attempts a JOIN sessions s ON s.id=a.session_id WHERE s.run_id=?)",
		"DELETE FROM commands WHERE session_id IN (SELECT id FROM sessions WHERE run_id=?)",
		"DELETE FROM attempts WHERE session_id IN (SELECT id FROM sessions WHERE run_id=?)",
		"DELETE FROM invitations WHERE run_id=?",
		"DELETE FROM sessions WHERE run_id=?",
		"UPDATE runs SET event_bytes=0,data_bytes=0,purged_at=NULL,purge_pending=1 WHERE id=?",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, runID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.sanitizeDatabase(ctx); err != nil {
		s.audit(nil, "system", "retention.purge", "run:"+runID, "pending_cleanup")
		return fmt.Errorf("participant rows deleted; database cleanup is pending: %w; retry purge --run %s --confirm", err, runID)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE runs SET purged_at=?,purge_pending=0 WHERE id=?", now(), runID); err != nil {
		return err
	}
	s.audit(nil, "system", "retention.purge", "run:"+runID, "success")
	return nil
}

func (s *Server) sanitizeDatabase(ctx context.Context) error {
	// secure_delete wipes deleted cells; VACUUM removes free pages.
	// Truncation removes pre-purge WAL copies.
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	var busy, frames, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint blocked by another database connection")
	}
	return nil
}
