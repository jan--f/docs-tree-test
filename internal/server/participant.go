package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jan--f/docs-tree-test/internal/study"
)

type publicNode struct {
	ID         string       `json:"id"`
	Label      string       `json:"label"`
	Selectable bool         `json:"selectable"`
	Children   []publicNode `json:"children"`
}

type publicTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}
type publicAttempt struct {
	ID        string  `json:"id"`
	NextSeq   int     `json:"next_seq"`
	Events    []Event `json:"events"`
	StartedAt string  `json:"started_at"`
}
type Session struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Completed  bool           `json:"completed"`
	TaskIndex  int            `json:"task_index"`
	TotalTasks int            `json:"total_tasks"`
	Task       *publicTask    `json:"task,omitempty"`
	Tree       []publicNode   `json:"tree,omitempty"`
	Attempt    *publicAttempt `json:"attempt"`
}

type storedSession struct {
	ID           string
	RunID        string
	Variant      string
	Panel        int
	Tasks        []string
	Nodes        map[string]string
	PublicTasks  map[string]string
	Index        int
	EventBytes   int64
	StorageBytes int64
}

type attempt struct {
	ID         string
	SessionID  string
	Index      int
	TaskID     string
	StartedAt  string
	NextSeq    int
	FinishedAt sql.NullString
	Policy     Policy
	EventBytes int64
}

func loadSession(ctx context.Context, q querier, runID, identity string) (*storedSession, error) {
	a := &storedSession{}
	var tasks, nodes, publicTasks string
	err := q.QueryRowContext(ctx, "SELECT id,run_id,variant_id,panel_index,tasks_json,nodes_json,public_tasks_json,task_index,event_bytes,storage_bytes FROM sessions WHERE run_id=? AND identity_hash=?", runID, identity).Scan(&a.ID, &a.RunID, &a.Variant, &a.Panel, &tasks, &nodes, &publicTasks, &a.Index, &a.EventBytes, &a.StorageBytes)
	if err == sql.ErrNoRows {
		return nil, problem(404, "not enrolled in this run")
	}
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		data string
		into any
	}{{tasks, &a.Tasks}, {nodes, &a.Nodes}, {publicTasks, &a.PublicTasks}} {
		if err := json.Unmarshal([]byte(item.data), item.into); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (s *Server) owned(ctx context.Context, q querier, slug, participantToken string) (*storedSession, *frozenSnapshot, *run, string, error) {
	run, err := loadRun(ctx, q, "slug", slug)
	if err != nil {
		return nil, nil, nil, "", err
	}
	identity := s.participantIdentity(run.ID, participantToken)
	a, err := loadSession(ctx, q, run.ID, identity)
	if err != nil {
		return nil, nil, nil, "", err
	}
	snapshot, err := loadSnapshot(ctx, q, run.VersionID)
	return a, snapshot, run, identity, err
}

func findTask(snapshot *frozenSnapshot, id string) study.Task {
	for _, t := range snapshot.Bundle.Config.Tasks {
		if t.ID == id {
			return t
		}
	}
	return study.Task{}
}

func nodesFor(nodes []study.Node, ids map[string]string) []publicNode {
	result := make([]publicNode, 0, len(nodes))
	for _, n := range nodes {
		result = append(result, publicNode{ids[n.ID], n.Label, n.ContentID != "", nodesFor(n.Children, ids)})
	}
	return result
}

func makeSession(ctx context.Context, q querier, a *storedSession, snapshot *frozenSnapshot) (*Session, error) {
	result := &Session{ID: a.ID, Title: snapshot.Bundle.Config.Title, TaskIndex: a.Index, TotalTasks: len(a.Tasks), Completed: a.Index >= len(a.Tasks)}
	if result.Completed {
		return result, nil
	}
	task := findTask(snapshot, a.Tasks[a.Index])
	result.Task = &publicTask{a.PublicTasks[task.ID], task.Prompt}
	result.Tree = nodesFor(snapshot.Trees[a.Variant], a.Nodes)
	var p publicAttempt
	var policyData string
	err := q.QueryRowContext(ctx, "SELECT id,next_seq,started_at,policy_json FROM attempts WHERE session_id=? AND task_index=?", a.ID, a.Index).Scan(&p.ID, &p.NextSeq, &p.StartedAt, &policyData)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	policy, err := decodePolicy(policyData)
	if err != nil {
		return nil, err
	}
	if policy != snapshot.Policy {
		return nil, problem(409, "attempt policy does not match its frozen version")
	}
	p.Events, err = loadEvents(ctx, q, p.ID)
	if err != nil {
		return nil, err
	}
	result.Attempt = &p
	return result, nil
}

func (s *Server) publicInfo(w http.ResponseWriter, r *http.Request) error {
	a, err := loadRun(r.Context(), s.db, "slug", r.PathValue("slug"))
	if err != nil {
		return err
	}
	snapshot, err := loadSnapshot(r.Context(), s.db, a.VersionID)
	if err != nil {
		return err
	}
	if token(r, participantCookie) == "" {
		s.cookie(w, participantCookie, randomID(), 365*24*60*60)
	}
	return respond(w, map[string]string{"title": snapshot.Bundle.Config.Title, "instructions": snapshot.Bundle.Config.Instructions, "mode": a.Mode, "status": a.Status, "recruitment": a.Recruitment})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Experience      string `json:"experience"`
		DocsFamiliarity string `json:"docs_familiarity"`
		InviteToken     string `json:"invite_token,omitempty"`
	}
	if err := decodeLimit(w, r, &req, 8<<10); err != nil {
		return err
	}
	if !validDemographics(req.Experience, req.DocsFamiliarity) {
		return problem(422, "experience fields must use the available response options")
	}
	participantToken, err := anonymous(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := loadRun(r.Context(), tx, "slug", r.PathValue("slug"))
	if err != nil {
		return err
	}
	snapshot, err := loadSnapshot(r.Context(), tx, run.VersionID)
	if err != nil {
		return err
	}
	engine, err := resolvePolicy(snapshot.Policy)
	if err != nil {
		return err
	}
	if run.Status == "suspended" {
		return problem(423, "participant writes are temporarily suspended")
	}
	identity := s.participantIdentity(run.ID, participantToken)
	a, err := loadSession(r.Context(), tx, run.ID, identity)
	if limitErr := s.limitParticipant(w, run.ID, identity, "join", false); limitErr != nil {
		return limitErr
	}
	enrolledNew := false
	if err != nil {
		if e, ok := err.(*apiError); !ok || e.status != 404 {
			return err
		}
		if run.Status != "open" {
			return problem(409, "enrollment is not open")
		}
		var enrolled int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM sessions WHERE run_id=?", run.ID).Scan(&enrolled); err != nil {
			return err
		}
		if enrolled >= run.EnrollmentCap {
			return problem(429, "this study has reached its enrollment limit")
		}
		if len(run.Allocation) == 0 {
			run.Allocation, err = engine.allocate(snapshot.Bundle.Config)
			if err != nil {
				return err
			}
		}
		choice := run.Allocation[0]
		a = &storedSession{ID: randomID(), RunID: run.ID, Variant: choice.Variant, Panel: choice.Panel, Tasks: append([]string(nil), snapshot.Bundle.Config.Panels[choice.Panel]...), Nodes: map[string]string{}, PublicTasks: map[string]string{}}
		if err := engine.order(a.Tasks); err != nil {
			return err
		}
		var ids func([]study.Node)
		ids = func(nodes []study.Node) {
			for _, n := range nodes {
				a.Nodes[n.ID] = randomID()
				ids(n.Children)
			}
		}
		ids(snapshot.Trees[a.Variant])
		for _, id := range a.Tasks {
			a.PublicTasks[id] = randomID()
		}
		tasks, _ := marshal(a.Tasks)
		nodes, _ := marshal(a.Nodes)
		publicTasks, _ := marshal(a.PublicTasks)
		alloc, _ := marshal(run.Allocation[1:])
		a.StorageBytes = int64(len(tasks) + len(nodes) + len(publicTasks) + len(req.Experience) + len(req.DocsFamiliarity))
		if a.StorageBytes > maxSessionEventData {
			return problem(422, "study session data exceeds the storage limit")
		}
		if run.DataBytes+a.StorageBytes > maxRunEventData {
			return problem(429, "study storage limit reached")
		}
		if run.Recruitment == "invitation" {
			if !validInvitationToken(req.InviteToken) {
				return problem(403, "a valid study invitation is required")
			}
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO sessions(id,run_id,identity_hash,variant_id,panel_index,tasks_json,nodes_json,public_tasks_json,experience,docs_familiarity,created_at,storage_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", a.ID, a.RunID, identity, a.Variant, a.Panel, string(tasks), string(nodes), string(publicTasks), req.Experience, req.DocsFamiliarity, now(), a.StorageBytes); err != nil {
			return err
		}
		if run.Recruitment == "invitation" {
			result, err := tx.ExecContext(r.Context(), "UPDATE invitations SET used_at=?,session_id=? WHERE run_id=? AND token_hash=? AND used_at IS NULL AND expires_at>?", now(), a.ID, run.ID, digest(req.InviteToken), now())
			if err != nil {
				return err
			}
			used, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if used != 1 {
				return problem(403, "the study invitation is invalid, expired, or already used")
			}
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE runs SET allocation_json=? WHERE id=?", string(alloc), run.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE runs SET data_bytes=data_bytes+? WHERE id=?", a.StorageBytes, run.ID); err != nil {
			return err
		}
		enrolledNew = true
	}
	result, err := makeSession(r.Context(), tx, a, snapshot)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if enrolledNew {
		s.metrics.sessionsEnrolled.Inc()
	}
	return respond(w, result)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) error {
	participantToken, err := anonymous(r)
	if err != nil {
		return problem(404, "not enrolled in this run")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, _, _, err := s.owned(r.Context(), tx, r.PathValue("slug"), participantToken)
	if err != nil {
		return err
	}
	result, err := makeSession(r.Context(), tx, a, snapshot)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return respond(w, result)
}

func commandID(id string) error {
	if strings.TrimSpace(id) != id || len(id) < 1 || len(id) > 128 {
		return problem(422, "command_id must contain 1–128 characters")
	}
	return nil
}

type commandReceipt struct {
	Operation     string
	RequestHash   string
	ResponseIndex int
	AttemptID     sql.NullString
	ExpiresAt     string
}

func receipt(ctx context.Context, q querier, sessionID, command, operation, hash string) (*commandReceipt, error) {
	var saved commandReceipt
	err := q.QueryRowContext(ctx, "SELECT operation,request_hash,response_index,attempt_id,expires_at FROM commands WHERE session_id=? AND command_id=?", sessionID, command).Scan(&saved.Operation, &saved.RequestHash, &saved.ResponseIndex, &saved.AttemptID, &saved.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if saved.Operation != operation || hash != saved.RequestHash {
		return nil, problem(409, "command_id already used with a different request")
	}
	expires, err := time.Parse(time.RFC3339Nano, saved.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if !expires.After(time.Now().UTC()) {
		if _, err := q.ExecContext(ctx, "DELETE FROM commands WHERE session_id=? AND command_id=?", sessionID, command); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &saved, nil
}

func saveReceipt(ctx context.Context, q querier, sessionID, command, operation, hash string, responseIndex int, attemptID string) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM commands WHERE expires_at<=?", now()); err != nil {
		return err
	}
	var count int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM commands WHERE session_id=? AND expires_at>?", sessionID, now()).Scan(&count); err != nil {
		return err
	}
	if count >= maxCommandReceipts {
		return problem(429, "too many recent command receipts; reload the study")
	}
	_, err := q.ExecContext(ctx, "INSERT INTO commands(session_id,command_id,operation,request_hash,response_index,attempt_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)", sessionID, command, operation, hash, responseIndex, nullString(attemptID), now(), time.Now().UTC().Add(receiptLifetime).Format(time.RFC3339Nano))
	return err
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Server) replayReceipt(ctx context.Context, q querier, session *storedSession, snapshot *frozenSnapshot, saved *commandReceipt) ([]byte, error) {
	if saved.ResponseIndex < 0 || saved.ResponseIndex > len(session.Tasks) {
		return nil, problem(409, "command receipt cannot be reconstructed; reload the study")
	}
	response := &Session{ID: session.ID, Title: snapshot.Bundle.Config.Title, TaskIndex: saved.ResponseIndex, TotalTasks: len(session.Tasks), Completed: saved.ResponseIndex >= len(session.Tasks)}
	if !response.Completed {
		task := findTask(snapshot, session.Tasks[saved.ResponseIndex])
		response.Task = &publicTask{session.PublicTasks[task.ID], task.Prompt}
		response.Tree = nodesFor(snapshot.Trees[session.Variant], session.Nodes)
	}
	if saved.AttemptID.Valid {
		var started string
		if err := q.QueryRowContext(ctx, "SELECT started_at FROM attempts WHERE id=? AND session_id=?", saved.AttemptID.String, session.ID).Scan(&started); err != nil {
			return nil, err
		}
		response.Attempt = &publicAttempt{ID: saved.AttemptID.String, NextSeq: 1, Events: []Event{}, StartedAt: started}
	}
	return marshal(response)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		CommandID string `json:"command_id"`
		TaskID    string `json:"task_id"`
	}
	if err := decodeLimit(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := commandID(req.CommandID); err != nil {
		return err
	}
	if req.TaskID == "" {
		return problem(422, "task_id is required")
	}
	participantToken, err := anonymous(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, run, identity, err := s.owned(r.Context(), tx, r.PathValue("slug"), participantToken)
	if err != nil {
		return err
	}
	if run.Status == "suspended" {
		return problem(423, "participant writes are temporarily suspended")
	}
	saved, err := receipt(r.Context(), tx, a.ID, req.CommandID, "start", digest(req.TaskID))
	if limitErr := s.limitParticipant(w, run.ID, identity, "start", saved != nil); limitErr != nil {
		return limitErr
	}
	if err != nil {
		return err
	}
	if saved != nil {
		data, err := s.replayReceipt(r.Context(), tx, a, snapshot, saved)
		if err != nil {
			return err
		}
		return rawResponse(w, data)
	}
	if a.Index >= len(a.Tasks) {
		return problem(409, "session is complete")
	}
	if req.TaskID != a.PublicTasks[a.Tasks[a.Index]] {
		return problem(409, "task changed; reload this session before starting")
	}
	var existing int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM attempts WHERE session_id=? AND task_index=?", a.ID, a.Index).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return problem(409, "task already started; reload this session")
	}
	attemptID := randomID()
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO attempts(id,session_id,task_index,task_id,started_at,policy_json) VALUES(?,?,?,?,?,?)", attemptID, a.ID, a.Index, a.Tasks[a.Index], now(), policyJSON(snapshot.Policy)); err != nil {
		return err
	}
	result, err := makeSession(r.Context(), tx, a, snapshot)
	if err != nil {
		return err
	}
	if err := saveReceipt(r.Context(), tx, a.ID, req.CommandID, "start", digest(req.TaskID), a.Index, attemptID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.metrics.attemptsStarted.Inc()
	data, err := marshal(result)
	if err != nil {
		return err
	}
	return rawResponse(w, data)
}

func loadAttempt(ctx context.Context, q querier, sessionID, id string) (*attempt, error) {
	a := &attempt{}
	var policy string
	err := q.QueryRowContext(ctx, "SELECT id,session_id,task_index,task_id,started_at,next_seq,finished_at,policy_json,event_bytes FROM attempts WHERE id=? AND session_id=?", id, sessionID).Scan(&a.ID, &a.SessionID, &a.Index, &a.TaskID, &a.StartedAt, &a.NextSeq, &a.FinishedAt, &policy, &a.EventBytes)
	if err == sql.ErrNoRows {
		return nil, problem(404, "attempt not found")
	}
	if err != nil {
		return nil, err
	}
	a.Policy, err = decodePolicy(policy)
	return a, err
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		AttemptID string  `json:"attempt_id"`
		Events    []Event `json:"events"`
	}
	if err := decodeLimit(w, r, &req, 512<<10); err != nil {
		return err
	}
	participantToken, err := anonymous(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, run, identity, err := s.owned(r.Context(), tx, r.PathValue("slug"), participantToken)
	if err != nil {
		return err
	}
	if run.Status == "suspended" {
		return problem(423, "participant writes are temporarily suspended")
	}
	if err := s.limitParticipant(w, run.ID, identity, "events", false); err != nil {
		return err
	}
	attempt, err := loadAttempt(r.Context(), tx, a.ID, req.AttemptID)
	if err != nil {
		return err
	}
	if attempt.Policy != snapshot.Policy {
		return problem(409, "attempt policy does not match its frozen version")
	}
	previousSeq := attempt.NextSeq
	if _, err := appendEvents(r.Context(), tx, a, snapshot, attempt, req.Events, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.metrics.eventsAccepted.Add(float64(attempt.NextSeq - previousSeq))
	return respond(w, map[string]int{"next_seq": attempt.NextSeq})
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		AttemptID string  `json:"attempt_id"`
		CommandID string  `json:"command_id"`
		Outcome   string  `json:"outcome"`
		NodeID    string  `json:"node_id,omitempty"`
		Events    []Event `json:"events"`
	}
	if err := decodeLimit(w, r, &req, 512<<10); err != nil {
		return err
	}
	if err := commandID(req.CommandID); err != nil {
		return err
	}
	if req.Outcome != "selected" && req.Outcome != "gave_up" && req.Outcome != "skipped" {
		return problem(422, "invalid outcome")
	}
	if req.Outcome != "selected" && req.NodeID != "" {
		return problem(422, "node_id is only valid for selected outcomes")
	}
	participantToken, err := anonymous(r)
	if err != nil {
		return err
	}
	requestJSON, _ := marshal(req)
	hash := digest(string(requestJSON))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, run, identity, err := s.owned(r.Context(), tx, r.PathValue("slug"), participantToken)
	if err != nil {
		return err
	}
	if run.Status == "suspended" {
		return problem(423, "participant writes are temporarily suspended")
	}
	saved, err := receipt(r.Context(), tx, a.ID, req.CommandID, "finish", hash)
	if limitErr := s.limitParticipant(w, run.ID, identity, "finish", saved != nil); limitErr != nil {
		return limitErr
	}
	if err != nil {
		return err
	}
	if saved != nil {
		data, err := s.replayReceipt(r.Context(), tx, a, snapshot, saved)
		if err != nil {
			return err
		}
		return rawResponse(w, data)
	}
	attempt, err := loadAttempt(r.Context(), tx, a.ID, req.AttemptID)
	if err != nil {
		return err
	}
	if attempt.FinishedAt.Valid || attempt.Index != a.Index {
		return problem(409, "attempt already finished; reload this session")
	}
	if attempt.Policy != snapshot.Policy {
		return problem(409, "attempt policy does not match its frozen version")
	}
	engine, err := resolvePolicy(attempt.Policy)
	if err != nil {
		return err
	}
	previousSeq := attempt.NextSeq
	events, err := appendEvents(r.Context(), tx, a, snapshot, attempt, req.Events, &finishSelection{req.Outcome, req.NodeID})
	if err != nil {
		return err
	}
	index := indexNodes(snapshot.Trees[a.Variant], a.Nodes)
	correct := false
	if req.Outcome == "selected" {
		n, ok := index[req.NodeID]
		if !ok || n.ContentID == "" {
			return problem(422, "select a selectable node from this session")
		}
		last := ""
		for _, event := range events {
			if event.Type == "select" {
				last = event.NodeID
			}
			if event.Type == "enter" || event.Type == "back" || event.Type == "root" {
				last = ""
			}
		}
		if last != req.NodeID {
			return problem(422, "matching select event is required before finishing")
		}
		correct = engine.correct(findTask(snapshot, attempt.TaskID), a.Variant, n.ContentID)
	}
	metrics := engine.metrics(events, index, req.NodeID, req.Outcome, true)
	finished := now()
	elapsed, err := serverElapsed(attempt.StartedAt, finished)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE attempts SET finished_at=?,outcome=?,selected_node=?,correct=?,navigation_ms=?,direct=?,backtracks=?,server_elapsed_ms=?,observed_navigation_ms=?,timing_quality=?,clock_epochs=?,policy_json=? WHERE id=?", finished, req.Outcome, req.NodeID, correct, metrics.NavigationMS, correct && metrics.Direct, metrics.Backtracks, elapsed, metrics.ObservedNavigationMS, metrics.TimingQuality, metrics.ClockEpochs, policyJSON(attempt.Policy), attempt.ID); err != nil {
		return err
	}
	a.Index++
	var completed any
	if a.Index == len(a.Tasks) {
		completed = finished
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE sessions SET task_index=?,completed_at=? WHERE id=?", a.Index, completed, a.ID); err != nil {
		return err
	}
	result, err := makeSession(r.Context(), tx, a, snapshot)
	if err != nil {
		return err
	}
	if err := saveReceipt(r.Context(), tx, a.ID, req.CommandID, "finish", hash, a.Index, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.metrics.eventsAccepted.Add(float64(attempt.NextSeq - previousSeq))
	s.metrics.attemptsFinished.WithLabelValues(req.Outcome).Inc()
	if result.Completed {
		s.metrics.sessionsCompleted.Inc()
	}
	data, err := marshal(result)
	if err != nil {
		return err
	}
	return rawResponse(w, data)
}

func validDemographics(experience, familiarity string) bool {
	experiences := map[string]bool{"": true, "new": true, "some": true, "regular": true, "extensive": true}
	familiarities := map[string]bool{"": true, "never": true, "occasionally": true, "regularly": true}
	return experiences[experience] && familiarities[familiarity]
}

func validInvitationToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
