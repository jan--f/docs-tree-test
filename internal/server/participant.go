package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

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
	ID          string
	RunID       string
	Variant     string
	Panel       int
	Tasks       []string
	Nodes       map[string]string
	PublicTasks map[string]string
	Index       int
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
}

func loadSession(ctx context.Context, q querier, runID, identity string) (*storedSession, error) {
	a := &storedSession{}
	var tasks, nodes, publicTasks string
	err := q.QueryRowContext(ctx, "SELECT id,run_id,variant_id,panel_index,tasks_json,nodes_json,public_tasks_json,task_index FROM sessions WHERE run_id=? AND identity_hash=?", runID, identity).Scan(&a.ID, &a.RunID, &a.Variant, &a.Panel, &tasks, &nodes, &publicTasks, &a.Index)
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

func owned(ctx context.Context, q querier, slug, identity string) (*storedSession, *frozenSnapshot, error) {
	run, err := loadRun(ctx, q, "slug", slug)
	if err != nil {
		return nil, nil, err
	}
	a, err := loadSession(ctx, q, run.ID, identity)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := loadSnapshot(ctx, q, run.VersionID)
	return a, snapshot, err
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
	return respond(w, map[string]string{"title": snapshot.Bundle.Config.Title, "instructions": snapshot.Bundle.Config.Instructions, "mode": a.Mode, "status": a.Status})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Experience      string `json:"experience"`
		DocsFamiliarity string `json:"docs_familiarity"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	if len(req.Experience) > 200 || len(req.DocsFamiliarity) > 200 {
		return problem(422, "experience fields must be at most 200 bytes")
	}
	identity, err := anonymous(r)
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
	a, err := loadSession(r.Context(), tx, run.ID, identity)
	if err != nil {
		if e, ok := err.(*apiError); !ok || e.status != 404 {
			return err
		}
		if run.Status != "open" {
			return problem(409, "enrollment is not open")
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
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO sessions(id,run_id,identity_hash,variant_id,panel_index,tasks_json,nodes_json,public_tasks_json,experience,docs_familiarity,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", a.ID, a.RunID, identity, a.Variant, a.Panel, string(tasks), string(nodes), string(publicTasks), req.Experience, req.DocsFamiliarity, now()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE runs SET allocation_json=? WHERE id=?", string(alloc), run.ID); err != nil {
			return err
		}
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

func (s *Server) session(w http.ResponseWriter, r *http.Request) error {
	identity, err := anonymous(r)
	if err != nil {
		return problem(404, "not enrolled in this run")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, err := owned(r.Context(), tx, r.PathValue("slug"), identity)
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

func receipt(ctx context.Context, q querier, sessionID, command, operation, hash string) ([]byte, error) {
	var op, savedHash, result string
	err := q.QueryRowContext(ctx, "SELECT operation,request_hash,response_json FROM commands WHERE session_id=? AND command_id=?", sessionID, command).Scan(&op, &savedHash, &result)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if op != operation || hash != savedHash {
		return nil, problem(409, "command_id already used with a different request")
	}
	return []byte(result), nil
}

func saveReceipt(ctx context.Context, q querier, sessionID, command, operation, hash string, response *Session) ([]byte, error) {
	data, err := marshal(response)
	if err != nil {
		return nil, err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO commands(session_id,command_id,operation,request_hash,response_json,created_at) VALUES(?,?,?,?,?,?)", sessionID, command, operation, hash, string(data), now())
	return data, err
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		CommandID string `json:"command_id"`
		TaskID    string `json:"task_id"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	if err := commandID(req.CommandID); err != nil {
		return err
	}
	if req.TaskID == "" {
		return problem(422, "task_id is required")
	}
	identity, err := anonymous(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, err := owned(r.Context(), tx, r.PathValue("slug"), identity)
	if err != nil {
		return err
	}
	data, err := receipt(r.Context(), tx, a.ID, req.CommandID, "start", digest(req.TaskID))
	if err != nil {
		return err
	}
	if data != nil {
		return rawResponse(w, data)
	}
	if a.Index >= len(a.Tasks) {
		return problem(409, "session is complete")
	}
	if req.TaskID != a.PublicTasks[a.Tasks[a.Index]] {
		return problem(409, "task changed; reload this session before starting")
	}
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO attempts(id,session_id,task_index,task_id,started_at,policy_json) VALUES(?,?,?,?,?,?) ON CONFLICT(session_id,task_index) DO NOTHING", randomID(), a.ID, a.Index, a.Tasks[a.Index], now(), policyJSON(snapshot.Policy)); err != nil {
		return err
	}
	result, err := makeSession(r.Context(), tx, a, snapshot)
	if err != nil {
		return err
	}
	data, err = saveReceipt(r.Context(), tx, a.ID, req.CommandID, "start", digest(req.TaskID), result)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return rawResponse(w, data)
}

func loadAttempt(ctx context.Context, q querier, sessionID, id string) (*attempt, error) {
	a := &attempt{}
	var policy string
	err := q.QueryRowContext(ctx, "SELECT id,session_id,task_index,task_id,started_at,next_seq,finished_at,policy_json FROM attempts WHERE id=? AND session_id=?", id, sessionID).Scan(&a.ID, &a.SessionID, &a.Index, &a.TaskID, &a.StartedAt, &a.NextSeq, &a.FinishedAt, &policy)
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
	if err := decode(w, r, &req); err != nil {
		return err
	}
	identity, err := anonymous(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, snapshot, err := owned(r.Context(), tx, r.PathValue("slug"), identity)
	if err != nil {
		return err
	}
	attempt, err := loadAttempt(r.Context(), tx, a.ID, req.AttemptID)
	if err != nil {
		return err
	}
	if attempt.Policy != snapshot.Policy {
		return problem(409, "attempt policy does not match its frozen version")
	}
	if _, err := appendEvents(r.Context(), tx, a, snapshot, attempt, req.Events, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
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
	if err := decode(w, r, &req); err != nil {
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
	identity, err := anonymous(r)
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
	a, snapshot, err := owned(r.Context(), tx, r.PathValue("slug"), identity)
	if err != nil {
		return err
	}
	data, err := receipt(r.Context(), tx, a.ID, req.CommandID, "finish", hash)
	if err != nil {
		return err
	}
	if data != nil {
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
	data, err = saveReceipt(r.Context(), tx, a.ID, req.CommandID, "finish", hash, result)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return rawResponse(w, data)
}
