package server

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// AssignedRow is the common denominator for every summary and blinded export.
// Unreached tasks remain rows; Correct is null unless an answer was selected.
type AssignedRow struct {
	RunID                string `json:"run_id"`
	VersionID            string `json:"version_id"`
	VersionHash          string `json:"version_hash"`
	Mode                 string `json:"mode"`
	Difficulty           string `json:"difficulty"`
	SessionID            string `json:"session_id"`
	Code                 string `json:"code"`
	TaskID               string `json:"task_id"`
	TaskIndex            int    `json:"task_index"`
	SessionCompleted     bool   `json:"session_completed"`
	Experience           string `json:"experience"`
	DocsFamiliarity      string `json:"docs_familiarity"`
	Shown                bool   `json:"shown"`
	Outcome              string `json:"outcome"`
	Correct              *bool  `json:"correct"`
	NavigationMS         *int64 `json:"navigation_ms"`
	ServerElapsedMS      *int64 `json:"server_elapsed_ms"`
	ObservedNavigationMS int64  `json:"observed_navigation_ms"`
	TimingQuality        string `json:"timing_quality"`
	ClockEpochs          int    `json:"clock_epochs"`
	Policy               Policy `json:"policy"`
	DirectSuccess        bool   `json:"direct_success"`
	Backtracks           int    `json:"backtracks"`
}

type Summary struct {
	Code                string   `json:"code"`
	TaskID              string   `json:"task_id,omitempty"`
	Participants        int      `json:"participants"`
	Completed           int      `json:"completed"`
	Assigned            int      `json:"assigned"`
	Shown               int      `json:"shown"`
	Correct             int      `json:"correct"`
	Incorrect           int      `json:"incorrect"`
	GaveUp              int      `json:"gave_up"`
	Skipped             int      `json:"skipped"`
	Unfinished          int      `json:"unfinished"`
	Unreached           int      `json:"unreached"`
	SuccessYield        float64  `json:"success_yield"`
	ShownSuccessRate    float64  `json:"shown_success_rate"`
	MedianNavigationMS  *float64 `json:"median_navigation_ms"`
	DirectSuccesses     int      `json:"direct_successes"`
	Backtracks          int      `json:"backtracks"`
	InterruptedAttempts int      `json:"interrupted_attempts"`
}

type health struct {
	Enrolled    int     `json:"enrolled"`
	Completed   int     `json:"completed"`
	Incomplete  int     `json:"incomplete"`
	LastEventAt *string `json:"last_event_at"`
}

type reportSession struct {
	ID, Variant, Experience, Familiarity string
	Tasks                                []string
	Nodes                                map[string]string
	Completed                            bool
}

type reportAttempt struct {
	ID, SessionID     string
	TaskID, StartedAt string
	FinishedAt        sql.NullString
	NextSeq           int
	SelectedNode      sql.NullString
	Index             int
	Outcome           sql.NullString
	Correct           sql.NullBool
	Navigation        sql.NullInt64
	Direct            bool
	Backtracks        int
	ServerElapsed     sql.NullInt64
	Observed          int64
	TimingQuality     string
	ClockEpochs       int
	Policy            Policy
}

func loadRunAttempts(ctx context.Context, q querier, runID string) ([]reportAttempt, error) {
	rows, err := q.QueryContext(ctx, "SELECT a.id,a.session_id,a.task_index,a.task_id,a.started_at,a.finished_at,a.next_seq,a.selected_node,a.outcome,a.correct,a.navigation_ms,a.direct,a.backtracks,a.server_elapsed_ms,a.observed_navigation_ms,a.timing_quality,a.clock_epochs,a.policy_json FROM attempts a JOIN sessions s ON s.id=a.session_id WHERE s.run_id=? ORDER BY s.created_at,s.id,a.task_index", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []reportAttempt{}
	for rows.Next() {
		var a reportAttempt
		var policy string
		if err := rows.Scan(&a.ID, &a.SessionID, &a.Index, &a.TaskID, &a.StartedAt, &a.FinishedAt, &a.NextSeq, &a.SelectedNode, &a.Outcome, &a.Correct, &a.Navigation, &a.Direct, &a.Backtracks, &a.ServerElapsed, &a.Observed, &a.TimingQuality, &a.ClockEpochs, &policy); err != nil {
			return nil, err
		}
		a.Policy, err = decodePolicy(policy)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func metricsForAttempt(ctx context.Context, q querier, a reportAttempt, snapshot *frozenSnapshot, variant string, nodes map[string]string, observedAt string) (attemptMetrics, int64, error) {
	m := attemptMetrics{ObservedNavigationMS: a.Observed, TimingQuality: a.TimingQuality, ClockEpochs: a.ClockEpochs, Direct: a.Direct, Backtracks: a.Backtracks}
	if a.Policy != snapshot.Policy {
		return m, 0, problem(409, "attempt policy does not match its frozen version")
	}
	engine, err := resolvePolicy(a.Policy)
	if err != nil {
		return m, 0, err
	}
	if a.FinishedAt.Valid {
		if !a.ServerElapsed.Valid {
			return m, 0, problem(409, "final attempt has no frozen timing metrics")
		}
		if a.Navigation.Valid {
			value := a.Navigation.Int64
			m.NavigationMS = &value
		}
		return m, a.ServerElapsed.Int64, nil
	}
	events, err := loadEvents(ctx, q, a.ID)
	if err != nil {
		return m, 0, err
	}
	m = engine.metrics(events, indexNodes(snapshot.Trees[variant], nodes), "", "", false)
	m.Direct = false
	elapsed, err := serverElapsed(a.StartedAt, observedAt)
	return m, elapsed, err
}

func reportRows(ctx context.Context, q querier, a *run) ([]AssignedRow, health, error) {
	sessions := []reportSession{}
	h := health{}
	rows, err := q.QueryContext(ctx, "SELECT id,variant_id,tasks_json,nodes_json,experience,docs_familiarity,completed_at IS NOT NULL FROM sessions WHERE run_id=? ORDER BY created_at,id", a.ID)
	if err != nil {
		return nil, h, err
	}
	for rows.Next() {
		var r reportSession
		var tasks, nodes string
		if err := rows.Scan(&r.ID, &r.Variant, &tasks, &nodes, &r.Experience, &r.Familiarity, &r.Completed); err != nil {
			rows.Close()
			return nil, h, err
		}
		if err := json.Unmarshal([]byte(tasks), &r.Tasks); err != nil {
			rows.Close()
			return nil, h, err
		}
		if err := json.Unmarshal([]byte(nodes), &r.Nodes); err != nil {
			rows.Close()
			return nil, h, err
		}
		sessions = append(sessions, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, h, err
	}
	attempts := map[string]map[int]reportAttempt{}
	storedAttempts, err := loadRunAttempts(ctx, q, a.ID)
	if err != nil {
		return nil, h, err
	}
	for _, r := range storedAttempts {
		if attempts[r.SessionID] == nil {
			attempts[r.SessionID] = map[int]reportAttempt{}
		}
		attempts[r.SessionID][r.Index] = r
	}
	snapshot, err := loadSnapshot(ctx, q, a.VersionID)
	if err != nil {
		return nil, h, err
	}
	result := []AssignedRow{}
	observedAt := now()
	difficulties := make(map[string]string)
	for _, task := range snapshot.Bundle.Config.Tasks {
		difficulties[task.ID] = task.Difficulty
	}
	for _, session := range sessions {
		h.Enrolled++
		if session.Completed {
			h.Completed++
		} else {
			h.Incomplete++
		}
		for i, task := range session.Tasks {
			r := AssignedRow{SessionID: session.ID, Code: a.Arms[session.Variant], TaskID: a.Tasks[task], TaskIndex: i, SessionCompleted: session.Completed, Experience: session.Experience, DocsFamiliarity: session.Familiarity, Outcome: "unreached", TimingQuality: "not_started", Policy: snapshot.Policy}
			r.RunID, r.VersionID, r.VersionHash, r.Mode, r.Difficulty = a.ID, a.VersionID, snapshot.PublicationHash, a.Mode, difficulties[task]
			if attempt, ok := attempts[session.ID][i]; ok {
				r.Shown, r.Outcome = true, "unfinished"
				metrics, elapsed, err := metricsForAttempt(ctx, q, attempt, snapshot, session.Variant, session.Nodes, observedAt)
				if err != nil {
					return nil, h, err
				}
				r.ServerElapsedMS = &elapsed
				r.NavigationMS, r.ObservedNavigationMS, r.TimingQuality, r.ClockEpochs = metrics.NavigationMS, metrics.ObservedNavigationMS, metrics.TimingQuality, metrics.ClockEpochs
				r.DirectSuccess, r.Backtracks = metrics.Direct, metrics.Backtracks
				if attempt.Outcome.Valid {
					r.Outcome = attempt.Outcome.String
					if r.Outcome == "selected" {
						value := attempt.Correct.Bool
						r.Correct = &value
					}
				}
			}
			result = append(result, r)
		}
	}
	var last sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT MAX(e.received_at) FROM events e JOIN attempts a ON a.id=e.attempt_id JOIN sessions s ON s.id=a.session_id WHERE s.run_id=?", a.ID).Scan(&last); err != nil {
		return nil, h, err
	}
	if last.Valid {
		h.LastEventAt = &last.String
	}
	return result, h, nil
}

func summarize(code, task string, rows []AssignedRow) Summary {
	s := Summary{Code: code, TaskID: task}
	participants, completed := map[string]bool{}, map[string]bool{}
	times := []int64{}
	for _, r := range rows {
		if r.Code != code || (task != "" && r.TaskID != task) {
			continue
		}
		participants[r.SessionID] = true
		if r.SessionCompleted {
			completed[r.SessionID] = true
		}
		s.Assigned++
		if r.Shown {
			s.Shown++
		}
		switch r.Outcome {
		case "selected":
			if r.Correct != nil && *r.Correct {
				s.Correct++
			} else {
				s.Incorrect++
			}
		case "gave_up":
			s.GaveUp++
		case "skipped":
			s.Skipped++
		case "unfinished":
			s.Unfinished++
		case "unreached":
			s.Unreached++
		}
		if r.TimingQuality == "interrupted" {
			s.InterruptedAttempts++
		}
		if r.TimingQuality == "complete" && r.NavigationMS != nil && (r.Outcome == "selected" || r.Outcome == "gave_up") {
			times = append(times, *r.NavigationMS)
		}
		if r.DirectSuccess {
			s.DirectSuccesses++
		}
		s.Backtracks += r.Backtracks
	}
	s.Participants, s.Completed = len(participants), len(completed)
	if s.Assigned > 0 {
		s.SuccessYield = float64(s.Correct) / float64(s.Assigned)
	}
	if s.Shown > 0 {
		s.ShownSuccessRate = float64(s.Correct) / float64(s.Shown)
	}
	if len(times) > 0 {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		median := float64(times[len(times)/2])
		if len(times)%2 == 0 {
			median = (float64(times[len(times)/2-1]) + median) / 2
		}
		s.MedianNavigationMS = &median
	}
	return s
}

func sortedValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

func (s *Server) results(w http.ResponseWriter, r *http.Request) error {
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := loadRun(r.Context(), tx, "id", r.PathValue("id"))
	if err != nil {
		return err
	}
	rows, health, err := reportRows(r.Context(), tx, a)
	if err != nil {
		return err
	}
	arms, tasks := []Summary{}, []Summary{}
	for _, arm := range sortedValues(a.Arms) {
		arms = append(arms, summarize(arm, "", rows))
		for _, task := range sortedValues(a.Tasks) {
			tasks = append(tasks, summarize(arm, task, rows))
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return respond(w, map[string]any{"health": health, "arms": arms, "tasks": tasks})
}

func csvCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) error {
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		return problem(422, "format must be csv or json")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := loadRun(r.Context(), tx, "id", r.PathValue("id"))
	if err != nil {
		return err
	}
	rows, _, err := reportRows(r.Context(), tx, a)
	if err != nil {
		return err
	}
	snapshot, err := loadSnapshot(r.Context(), tx, a.VersionID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.Header().Set("Content-Disposition", `attachment; filename="assigned-tasks.`+format+`"`)
	if format == "json" {
		return respond(w, map[string]any{"run_id": a.ID, "version_id": a.VersionID, "version_hash": snapshot.PublicationHash, "mode": a.Mode, "policy": snapshot.Policy, "rows": rows})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c := csv.NewWriter(w)
	if err := c.Write([]string{"session_id", "code", "task_id", "task_index", "session_completed", "experience", "docs_familiarity", "shown", "outcome", "correct", "navigation_ms", "direct_success", "backtracks", "server_elapsed_ms", "observed_navigation_ms", "timing_quality", "clock_epochs", "policy_version", "allocation_policy", "scoring_policy", "timing_policy", "run_id", "version_id", "version_hash", "mode", "difficulty"}); err != nil {
		return err
	}
	for _, r := range rows {
		correct, navigation, elapsed := "", "", ""
		if r.Correct != nil {
			correct = strconv.FormatBool(*r.Correct)
		}
		if r.NavigationMS != nil {
			navigation = strconv.FormatInt(*r.NavigationMS, 10)
		}
		if r.ServerElapsedMS != nil {
			elapsed = strconv.FormatInt(*r.ServerElapsedMS, 10)
		}
		if err := c.Write([]string{r.SessionID, r.Code, r.TaskID, strconv.Itoa(r.TaskIndex), strconv.FormatBool(r.SessionCompleted), csvCell(r.Experience), csvCell(r.DocsFamiliarity), strconv.FormatBool(r.Shown), r.Outcome, correct, navigation, strconv.FormatBool(r.DirectSuccess), strconv.Itoa(r.Backtracks), elapsed, strconv.FormatInt(r.ObservedNavigationMS, 10), r.TimingQuality, strconv.Itoa(r.ClockEpochs), r.Policy.Version, r.Policy.Allocation, r.Policy.Scoring, r.Policy.Timing, r.RunID, r.VersionID, r.VersionHash, r.Mode, r.Difficulty}); err != nil {
			return err
		}
	}
	c.Flush()
	return c.Error()
}

type ownerAttempt struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	TaskID          string  `json:"task_id"`
	TaskIndex       int     `json:"task_index"`
	StartedAt       string  `json:"started_at"`
	FinishedAt      *string `json:"finished_at"`
	NextSeq         int     `json:"next_seq"`
	Outcome         *string `json:"outcome"`
	NodeID          *string `json:"node_id"`
	Correct         *bool   `json:"correct"`
	ServerElapsedMS int64   `json:"server_elapsed_ms"`
	Policy          Policy  `json:"policy"`
	attemptMetrics
}

func (s *Server) eventExport(w http.ResponseWriter, r *http.Request) error {
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := loadRun(r.Context(), tx, "id", r.PathValue("id"))
	if err != nil {
		return err
	}
	snapshot, err := loadSnapshot(r.Context(), tx, a.VersionID)
	if err != nil {
		return err
	}
	type ownerSession struct {
		ID          string          `json:"id"`
		Variant     string          `json:"variant_id"`
		Panel       int             `json:"panel_index"`
		Tasks       json.RawMessage `json:"task_order"`
		Nodes       json.RawMessage `json:"node_ids"`
		PublicTasks json.RawMessage `json:"task_ids"`
	}
	sessions := []ownerSession{}
	bySession := map[string]ownerSession{}
	rows, err := tx.QueryContext(r.Context(), "SELECT id,variant_id,panel_index,tasks_json,nodes_json,public_tasks_json FROM sessions WHERE run_id=? ORDER BY created_at,id", a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v ownerSession
		var tasks, nodes, publicTasks string
		if err := rows.Scan(&v.ID, &v.Variant, &v.Panel, &tasks, &nodes, &publicTasks); err != nil {
			rows.Close()
			return err
		}
		v.Tasks, v.Nodes, v.PublicTasks = json.RawMessage(tasks), json.RawMessage(nodes), json.RawMessage(publicTasks)
		sessions = append(sessions, v)
		bySession[v.ID] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	storedAttempts, err := loadRunAttempts(r.Context(), tx, a.ID)
	if err != nil {
		return err
	}
	attempts := make([]ownerAttempt, 0, len(storedAttempts))
	observedAt := now()
	for _, a := range storedAttempts {
		session := bySession[a.SessionID]
		var nodes map[string]string
		if err := json.Unmarshal(session.Nodes, &nodes); err != nil {
			return err
		}
		metrics, elapsed, err := metricsForAttempt(r.Context(), tx, a, snapshot, session.Variant, nodes, observedAt)
		if err != nil {
			return err
		}
		v := ownerAttempt{ID: a.ID, SessionID: a.SessionID, TaskID: a.TaskID, TaskIndex: a.Index, StartedAt: a.StartedAt, NextSeq: a.NextSeq, ServerElapsedMS: elapsed, Policy: a.Policy, attemptMetrics: metrics}
		if a.FinishedAt.Valid {
			value := a.FinishedAt.String
			v.FinishedAt = &value
		}
		if a.Outcome.Valid {
			value := a.Outcome.String
			v.Outcome = &value
		}
		if a.SelectedNode.Valid && a.SelectedNode.String != "" {
			value := a.SelectedNode.String
			v.NodeID = &value
		}
		if a.Outcome.String == "selected" && a.Correct.Valid {
			value := a.Correct.Bool
			v.Correct = &value
		}
		attempts = append(attempts, v)
	}
	type ownerEvent struct {
		SessionID  string          `json:"session_id"`
		AttemptID  string          `json:"attempt_id"`
		TaskID     string          `json:"task_id"`
		Event      json.RawMessage `json:"event"`
		ReceivedAt string          `json:"received_at"`
	}
	events := []ownerEvent{}
	rows, err = tx.QueryContext(r.Context(), "SELECT s.id,a.id,a.task_id,e.payload_json,e.received_at FROM events e JOIN attempts a ON a.id=e.attempt_id JOIN sessions s ON s.id=a.session_id WHERE s.run_id=? ORDER BY s.created_at,s.id,a.task_index,e.seq", a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v ownerEvent
		var data string
		if err := rows.Scan(&v.SessionID, &v.AttemptID, &v.TaskID, &data, &v.ReceivedAt); err != nil {
			rows.Close()
			return err
		}
		v.Event = json.RawMessage(data)
		events = append(events, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.Header().Set("Content-Disposition", `attachment; filename="owner-events.json"`)
	return respond(w, map[string]any{"run_id": a.ID, "policy": snapshot.Policy, "sessions": sessions, "attempts": attempts, "events": events})
}
