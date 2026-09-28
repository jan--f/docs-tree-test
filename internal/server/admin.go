package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/jan--f/docs-tree-test/internal/study"
)

type run struct {
	ID              string            `json:"id"`
	Slug            string            `json:"slug"`
	VersionID       string            `json:"version_id"`
	Mode            string            `json:"mode"`
	Status          string            `json:"status"`
	CreatedAt       string            `json:"created_at"`
	EnrollmentCap   int               `json:"enrollment_cap"`
	Recruitment     string            `json:"recruitment"`
	RetentionDays   int               `json:"retention_days"`
	RetentionTarget *string           `json:"retention_target_at,omitempty"`
	PurgedAt        *string           `json:"purged_at,omitempty"`
	PurgePending    bool              `json:"purge_pending,omitempty"`
	EventBytes      int64             `json:"event_bytes"`
	DataBytes       int64             `json:"data_bytes"`
	Arms            map[string]string `json:"-"`
	Tasks           map[string]string `json:"-"`
	Allocation      []allocation      `json:"-"`
}

type allocation struct {
	Variant string `json:"variant"`
	Panel   int    `json:"panel"`
}

func loadRun(ctx context.Context, q querier, column, value string) (*run, error) {
	var a run
	var arms, tasks, alloc string
	// column is an internal constant, never request input.
	var suspended bool
	var retentionTarget, purgedAt sql.NullString
	err := q.QueryRowContext(ctx, "SELECT id,slug,version_id,mode,status,created_at,arms_json,tasks_json,allocation_json,write_suspended,enrollment_cap,recruitment,retention_days,retention_target_at,purged_at,event_bytes,data_bytes,purge_pending FROM runs WHERE "+column+"=?", value).Scan(&a.ID, &a.Slug, &a.VersionID, &a.Mode, &a.Status, &a.CreatedAt, &arms, &tasks, &alloc, &suspended, &a.EnrollmentCap, &a.Recruitment, &a.RetentionDays, &retentionTarget, &purgedAt, &a.EventBytes, &a.DataBytes, &a.PurgePending)
	if err == sql.ErrNoRows {
		return nil, problem(404, "run not found")
	}
	if err != nil {
		return nil, err
	}
	if suspended {
		a.Status = "suspended"
	}
	if retentionTarget.Valid {
		a.RetentionTarget = &retentionTarget.String
	}
	if purgedAt.Valid {
		a.PurgedAt = &purgedAt.String
	}
	for _, item := range []struct {
		data string
		into any
	}{{arms, &a.Arms}, {tasks, &a.Tasks}, {alloc, &a.Allocation}} {
		if err := json.Unmarshal([]byte(item.data), item.into); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

func shuffle[T any](items []T) error {
	for i := len(items) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(n.Int64())
		items[i], items[j] = items[j], items[i]
	}
	return nil
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) error {
	var b study.Bundle
	if err := decode(w, r, &b); err != nil {
		return err
	}
	snapshot, err := study.Validate(b)
	if err != nil {
		return problem(422, err.Error())
	}
	counts := map[string]int{}
	var count func([]study.Node) int
	count = func(nodes []study.Node) int {
		n := len(nodes)
		for _, node := range nodes {
			n += count(node.Children)
		}
		return n
	}
	for id, nodes := range snapshot.Trees {
		counts[id] = count(nodes)
	}
	return respond(w, map[string]any{"hash": publicationHash(snapshot.Hash, currentPolicy()), "content_hash": snapshot.Hash, "policy": currentPolicy(), "node_counts": counts, "task_count": len(b.Config.Tasks), "panel_count": len(b.Config.Panels)})
}

type draftInfo struct {
	ID        string `json:"id"`
	Revision  int    `json:"revision"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,revision,title,updated_at FROM drafts ORDER BY updated_at DESC,id")
	if err != nil {
		return err
	}
	defer rows.Close()
	drafts := []draftInfo{}
	for rows.Next() {
		var d draftInfo
		if err := rows.Scan(&d.ID, &d.Revision, &d.Title, &d.UpdatedAt); err != nil {
			return err
		}
		drafts = append(drafts, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return respond(w, map[string]any{"drafts": drafts})
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) error {
	var b study.Bundle
	if err := decode(w, r, &b); err != nil {
		return err
	}
	data, err := marshal(b)
	if err != nil {
		return err
	}
	id := randomID()
	if _, err := s.db.ExecContext(r.Context(), "INSERT INTO drafts(id,revision,title,bundle_json,updated_at) VALUES(?,1,?,?,?)", id, b.Config.Title, string(data), now()); err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "draft.create", "draft:"+id, "success")
	return respond(w, map[string]any{"id": id, "revision": 1})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) error {
	var revision int
	var data string
	id := r.PathValue("id")
	err := s.db.QueryRowContext(r.Context(), "SELECT revision,bundle_json FROM drafts WHERE id=?", id).Scan(&revision, &data)
	if err == sql.ErrNoRows {
		return problem(404, "draft not found")
	}
	if err != nil {
		return err
	}
	return respond(w, map[string]any{"id": id, "revision": revision, "bundle": json.RawMessage(data)})
}

func (s *Server) updateDraft(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Revision int          `json:"revision"`
		Bundle   study.Bundle `json:"bundle"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	data, err := marshal(req.Bundle)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int
	id := r.PathValue("id")
	err = tx.QueryRowContext(r.Context(), "SELECT revision FROM drafts WHERE id=?", id).Scan(&revision)
	if err == sql.ErrNoRows {
		return problem(404, "draft not found")
	}
	if err != nil {
		return err
	}
	if revision != req.Revision {
		return problem(409, "draft revision changed; reload before saving")
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE drafts SET revision=revision+1,title=?,bundle_json=?,updated_at=? WHERE id=?", req.Bundle.Config.Title, string(data), now(), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "draft.update", "draft:"+id, "success")
	return respond(w, map[string]any{"id": id, "revision": revision + 1})
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Revision int `json:"revision"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int
	var data string
	err = tx.QueryRowContext(r.Context(), "SELECT revision,bundle_json FROM drafts WHERE id=?", r.PathValue("id")).Scan(&revision, &data)
	if err == sql.ErrNoRows {
		return problem(404, "draft not found")
	}
	if err != nil {
		return err
	}
	if revision != req.Revision {
		return problem(409, "draft revision changed; reload before publishing")
	}
	var b study.Bundle
	if err := json.Unmarshal([]byte(data), &b); err != nil {
		return err
	}
	snapshot, err := study.Validate(b)
	if err != nil {
		return problem(422, err.Error())
	}
	id, err := insertVersion(r.Context(), tx, snapshot)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "version.publish", "version:"+id, "success")
	return respond(w, map[string]any{"id": id, "hash": publicationHash(snapshot.Hash, currentPolicy()), "content_hash": snapshot.Hash, "policy": currentPolicy()})
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) error {
	type version struct {
		ID          string `json:"id"`
		Hash        string `json:"hash"`
		Title       string `json:"title"`
		Slug        string `json:"slug"`
		CreatedAt   string `json:"created_at"`
		ContentHash string `json:"content_hash"`
		Policy      Policy `json:"policy"`
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,hash,title,slug,created_at,content_hash,scoring_policy FROM versions ORDER BY created_at DESC,id")
	if err != nil {
		return err
	}
	defer rows.Close()
	versions := []version{}
	for rows.Next() {
		var v version
		var policyData string
		if err := rows.Scan(&v.ID, &v.Hash, &v.Title, &v.Slug, &v.CreatedAt, &v.ContentHash, &policyData); err != nil {
			return err
		}
		v.Policy, err = decodePolicy(policyData)
		if err != nil {
			return err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return respond(w, map[string]any{"versions": versions})
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		VersionID     string `json:"version_id"`
		Slug          string `json:"slug"`
		Mode          string `json:"mode"`
		EnrollmentCap int    `json:"enrollment_cap,omitempty"`
		Recruitment   string `json:"recruitment,omitempty"`
		RetentionDays int    `json:"retention_days,omitempty"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	if len(req.Slug) > 80 || !slugPattern.MatchString(req.Slug) {
		return problem(422, "slug must be 1–80 lowercase letters, digits and single hyphens")
	}
	if req.Mode != "pilot" && req.Mode != "real" {
		return problem(422, "mode must be pilot or real")
	}
	if req.EnrollmentCap == 0 {
		req.EnrollmentCap = 200
	}
	if req.EnrollmentCap < 1 || req.EnrollmentCap > 10000 {
		return problem(422, "enrollment_cap must be between 1 and 10000")
	}
	if req.Recruitment == "" {
		req.Recruitment = "public"
	}
	if req.Recruitment != "public" && req.Recruitment != "invitation" {
		return problem(422, "recruitment must be public or invitation")
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = 90
	}
	if req.RetentionDays < 1 || req.RetentionDays > 3650 {
		return problem(422, "retention_days must be between 1 and 3650")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := loadSnapshot(r.Context(), tx, req.VersionID)
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM runs WHERE slug=?", req.Slug).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return problem(409, "run slug already exists")
	}
	arms, tasks := map[string]string{}, map[string]string{}
	variants := append([]study.Variant(nil), snapshot.Bundle.Config.Variants...)
	if err := shuffle(variants); err != nil {
		return err
	}
	for i, v := range variants {
		arms[v.ID] = fmt.Sprintf("A%02d", i+1)
	}
	for i, t := range snapshot.Bundle.Config.Tasks {
		tasks[t.ID] = fmt.Sprintf("T%02d", i+1)
	}
	armsJSON, err := marshal(arms)
	if err != nil {
		return err
	}
	tasksJSON, err := marshal(tasks)
	if err != nil {
		return err
	}
	a := run{ID: randomID(), Slug: req.Slug, VersionID: req.VersionID, Mode: req.Mode, Status: "paused", CreatedAt: now(), EnrollmentCap: req.EnrollmentCap, Recruitment: req.Recruitment, RetentionDays: req.RetentionDays}
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO runs(id,slug,version_id,mode,status,created_at,arms_json,tasks_json,enrollment_cap,recruitment,retention_days) VALUES(?,?,?,?,?,?,?,?,?,?,?)", a.ID, a.Slug, a.VersionID, a.Mode, a.Status, a.CreatedAt, string(armsJSON), string(tasksJSON), a.EnrollmentCap, a.Recruitment, a.RetentionDays); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "run.create", "run:"+a.ID, "success")
	return respond(w, a)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,slug,version_id,mode,status,created_at,write_suspended,enrollment_cap,recruitment,retention_days,retention_target_at,purged_at,event_bytes,data_bytes,purge_pending FROM runs ORDER BY created_at DESC,id")
	if err != nil {
		return err
	}
	defer rows.Close()
	runs := []run{}
	for rows.Next() {
		var a run
		var suspended bool
		var retentionTarget, purgedAt sql.NullString
		if err := rows.Scan(&a.ID, &a.Slug, &a.VersionID, &a.Mode, &a.Status, &a.CreatedAt, &suspended, &a.EnrollmentCap, &a.Recruitment, &a.RetentionDays, &retentionTarget, &purgedAt, &a.EventBytes, &a.DataBytes, &a.PurgePending); err != nil {
			return err
		}
		if suspended {
			a.Status = "suspended"
		}
		if retentionTarget.Valid {
			a.RetentionTarget = &retentionTarget.String
		}
		if purgedAt.Valid {
			a.PurgedAt = &purgedAt.String
		}
		runs = append(runs, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return respond(w, map[string]any{"runs": runs})
}

func (s *Server) updateRun(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Status        string `json:"status,omitempty"`
		RetentionDays int    `json:"retention_days,omitempty"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	if req.Status == "" && req.RetentionDays == 0 {
		return problem(422, "status or retention_days is required")
	}
	if req.Status != "" && req.Status != "open" && req.Status != "paused" && req.Status != "closed" && req.Status != "suspended" {
		return problem(422, "status must be open, paused, closed or suspended")
	}
	if req.RetentionDays != 0 && (req.RetentionDays < 1 || req.RetentionDays > 3650) {
		return problem(422, "retention_days must be between 1 and 3650")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStatus string
	var currentlySuspended bool
	var retentionDays int
	var purgedAt, closedAt, retentionTarget sql.NullString
	var purgePending bool
	err = tx.QueryRowContext(r.Context(), "SELECT status,write_suspended,retention_days,purged_at,purge_pending,closed_at,retention_target_at FROM runs WHERE id=?", r.PathValue("id")).Scan(&currentStatus, &currentlySuspended, &retentionDays, &purgedAt, &purgePending, &closedAt, &retentionTarget)
	if err == sql.ErrNoRows {
		return problem(404, "run not found")
	}
	if err != nil {
		return err
	}
	if purgedAt.Valid && req.Status == "open" {
		return problem(409, "a purged run cannot be reopened")
	}
	if purgePending && req.Status != "" && req.Status != "closed" && req.Status != "suspended" {
		return problem(409, "a run awaiting purge cleanup must remain closed")
	}
	if req.RetentionDays != 0 {
		retentionDays = req.RetentionDays
	}
	status, suspended := currentStatus, currentlySuspended
	if req.Status == "suspended" {
		suspended = true
	} else if req.Status != "" {
		status = req.Status
		suspended = false
	}
	if status != "closed" {
		closedAt, retentionTarget = sql.NullString{}, sql.NullString{}
	} else {
		if currentStatus != "closed" || !closedAt.Valid {
			closedAt = sql.NullString{String: now(), Valid: true}
			retentionTarget = sql.NullString{}
		}
		if req.RetentionDays != 0 || !retentionTarget.Valid {
			closed, err := time.Parse(time.RFC3339Nano, closedAt.String)
			if err != nil {
				return err
			}
			retentionTarget = sql.NullString{String: closed.AddDate(0, 0, retentionDays).Format(time.RFC3339Nano), Valid: true}
		}
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE runs SET status=?,write_suspended=?,retention_days=?,closed_at=?,retention_target_at=? WHERE id=?", status, suspended, retentionDays, closedAt, retentionTarget, r.PathValue("id")); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "run.status", "run:"+r.PathValue("id"), "success")
	return respond(w, map[string]bool{"ok": true})
}

func (s *Server) key(w http.ResponseWriter, r *http.Request) error {
	a, err := loadRun(r.Context(), s.db, "id", r.PathValue("id"))
	if err != nil {
		return err
	}
	snapshot, err := loadSnapshot(r.Context(), s.db, a.VersionID)
	if err != nil {
		return err
	}
	s.audit(r, adminFromContext(r).Username, "export.owner_key", "run:"+a.ID, "success")
	return respond(w, map[string]any{"run_id": a.ID, "version_id": a.VersionID, "hash": snapshot.PublicationHash, "content_hash": snapshot.Hash, "policy": snapshot.Policy, "arms": a.Arms, "tasks": a.Tasks, "bundle": snapshot.Bundle})
}
