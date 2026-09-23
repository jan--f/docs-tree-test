package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jan--f/docs-tree-test/internal/study"
)

// Event uses an opaque string or integer epoch so clients can persist a page's
// performance-clock identity without converting between wall and monotonic time.
type Event struct {
	ID        string          `json:"id"`
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	NodeID    string          `json:"node_id,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Epoch     json.RawMessage `json:"epoch"`
	Visible   *bool           `json:"visible,omitempty"`
	Outcome   string          `json:"outcome,omitempty"`
}

func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event
	var value plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "seq", "type", "elapsed_ms", "epoch"} {
		if len(fields[key]) == 0 || bytes.Equal(fields[key], []byte("null")) {
			return errors.New("event requires " + key)
		}
	}
	// Treat alternate JSON string escaping as the same epoch identity.
	var epoch string
	if json.Unmarshal(value.Epoch, &epoch) == nil {
		value.Epoch, _ = json.Marshal(epoch)
	}
	*e = Event(value)
	return nil
}

type indexedNode struct {
	study.Node
	Parent string
}

func indexNodes(nodes []study.Node, ids map[string]string) map[string]indexedNode {
	result := map[string]indexedNode{}
	var walk func([]study.Node, string)
	walk = func(nodes []study.Node, parent string) {
		for _, n := range nodes {
			id := ids[n.ID]
			result[id] = indexedNode{n, parent}
			walk(n.Children, id)
		}
	}
	walk(nodes, "")
	return result
}

func loadEvents(ctx context.Context, q querier, attemptID string) ([]Event, error) {
	rows, err := q.QueryContext(ctx, "SELECT payload_json FROM events WHERE attempt_id=? ORDER BY seq", attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func canonicalEvent(e Event) string { data, _ := json.Marshal(e); return string(data) }

func validEpoch(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return len(text) > 0 && len(text) <= 128
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return false
	}
	i, err := strconv.ParseInt(string(number), 10, 64)
	return err == nil && i >= 0
}

type finishSelection struct{ Outcome, NodeID string }

func appendEvents(ctx context.Context, q querier, session *storedSession, snapshot *frozenSnapshot, a *attempt, incoming []Event, terminal *finishSelection) ([]Event, error) {
	if len(incoming) > 1000 {
		return nil, problem(422, "send at most 1000 events per request")
	}
	if terminal != nil {
		if len(incoming) == 0 || incoming[len(incoming)-1].Type != "submit" {
			return nil, problem(422, "finish requires a final submit event")
		}
		last := incoming[len(incoming)-1]
		if last.Outcome != terminal.Outcome || last.NodeID != terminal.NodeID {
			return nil, problem(422, "submit outcome and node_id must match the finish request")
		}
	}
	saved, err := loadEvents(ctx, q, a.ID)
	if err != nil {
		return nil, err
	}
	ids := map[string]int{}
	for _, e := range saved {
		ids[e.ID] = e.Seq
	}
	index := indexNodes(snapshot.Trees[session.Variant], session.Nodes)
	originalCount := len(saved)
	for i, e := range incoming {
		if e.Type == "submit" && (terminal == nil || i != len(incoming)-1) {
			return nil, problem(422, "submit is only accepted as the final event of finish")
		}
		if e.Type != "submit" && e.Outcome != "" {
			return nil, problem(422, "outcome is only valid on submit events")
		}
		if e.Seq < 1 {
			return nil, problem(422, "event sequence begins at 1")
		}
		if e.Seq <= len(saved) {
			if canonicalEvent(saved[e.Seq-1]) != canonicalEvent(e) {
				return nil, problem(409, "event sequence conflict; reload this session")
			}
			continue
		}
		if a.FinishedAt.Valid || a.Index != session.Index {
			return nil, problem(409, "attempt already finished")
		}
		if e.Seq != len(saved)+1 {
			return nil, problem(409, "event sequence gap; reload this session")
		}
		if _, ok := ids[e.ID]; ok {
			return nil, problem(409, "event ID already used at a different sequence")
		}
		if len(e.ID) == 0 || len(e.ID) > 128 || strings.TrimSpace(e.ID) != e.ID {
			return nil, problem(422, "invalid event ID")
		}
		if e.ElapsedMS < 0 || e.ElapsedMS > 365*24*60*60*1000 {
			return nil, problem(422, "invalid elapsed_ms")
		}
		if !validEpoch(e.Epoch) {
			return nil, problem(422, "epoch must be a nonempty string or nonnegative integer")
		}
		switch e.Type {
		case "tree_shown", "enter", "back", "root", "select", "visibility_hidden", "visibility_visible", "resume", "submit":
		default:
			return nil, problem(422, "invalid event type")
		}
		if e.Seq == 1 && e.Type != "tree_shown" && !(e.Type == "submit" && e.Outcome == "skipped") {
			return nil, problem(422, "first event must be tree_shown or a comprehension-skip submit")
		}
		if e.Type == "submit" && e.Visible == nil {
			return nil, problem(422, "submit requires visible")
		}
		if e.Type == "visibility_hidden" && e.Visible != nil && *e.Visible {
			return nil, problem(422, "visibility_hidden cannot have visible=true")
		}
		if e.Type == "visibility_visible" && e.Visible != nil && !*e.Visible {
			return nil, problem(422, "visibility_visible cannot have visible=false")
		}
		if e.Type == "submit" && e.Outcome != "selected" && e.Outcome != "gave_up" && e.Outcome != "skipped" {
			return nil, problem(422, "submit requires a valid outcome")
		}
		if e.NodeID != "" {
			if _, ok := index[e.NodeID]; !ok {
				return nil, problem(422, "unknown node_id for this session")
			}
		}
		if (e.Type == "enter" || e.Type == "select") && e.NodeID == "" {
			return nil, problem(422, "navigation event requires node_id")
		}
		if e.Type == "select" && index[e.NodeID].ContentID == "" {
			return nil, problem(422, "node is not selectable")
		}
		if len(saved) >= 100000 {
			return nil, problem(422, "attempt event limit exceeded")
		}
		ids[e.ID] = e.Seq
		saved = append(saved, e)
	}
	// A page epoch is contiguous. Returning to an older page's stream indicates
	// another tab is writing and must reload instead of merging its navigation.
	seen := map[string]bool{}
	previousEpoch := ""
	var previousElapsed int64
	current := ""
	for i, e := range saved {
		epoch := strings.TrimSpace(string(e.Epoch))
		if epoch != previousEpoch {
			if seen[epoch] {
				return nil, problem(409, "page epoch conflict; reload this session")
			}
			if e.Visible == nil {
				return nil, problem(422, "each clock epoch must begin with visible")
			}
			if i > 0 && e.Type != "resume" {
				return nil, problem(422, "each new clock epoch must begin with resume")
			}
			seen[epoch] = true
			previousEpoch, previousElapsed = epoch, e.ElapsedMS
		} else {
			if e.ElapsedMS < previousElapsed {
				return nil, problem(409, "elapsed_ms must be monotonic within an epoch")
			}
			previousElapsed = e.ElapsedMS
		}
		switch e.Type {
		case "submit":
			if i != len(saved)-1 {
				return nil, problem(422, "submit must be the unique terminal event")
			}
		case "tree_shown":
			if e.Seq != 1 {
				return nil, problem(422, "tree_shown may only begin an attempt; use resume after a reload")
			}
		case "enter":
			if index[e.NodeID].Parent != current {
				return nil, problem(422, "enter must target a child of the current location")
			}
			current = e.NodeID
		case "select":
			if e.NodeID != current && index[e.NodeID].Parent != current {
				return nil, problem(422, "select must target a visible page")
			}
		case "back":
			ancestor, valid := index[current].Parent, e.NodeID == ""
			for ancestor != "" {
				if ancestor == e.NodeID {
					valid = true
				}
				ancestor = index[ancestor].Parent
			}
			if current == "" || !valid {
				return nil, problem(422, "back must target an ancestor location")
			}
			current = e.NodeID
		case "root":
			if e.NodeID != "" {
				return nil, problem(422, "root event cannot specify node_id")
			}
			current = ""
		case "resume", "visibility_hidden", "visibility_visible":
			if e.NodeID != "" && e.NodeID != current {
				return nil, problem(409, "navigation location conflict; reload this session")
			}
		}
	}
	for _, e := range saved[originalCount:] {
		if _, err := q.ExecContext(ctx, "INSERT INTO events(attempt_id,seq,id,payload_json,received_at) VALUES(?,?,?,?,?)", a.ID, e.Seq, e.ID, canonicalEvent(e), now()); err != nil {
			return nil, err
		}
	}
	a.NextSeq = len(saved) + 1
	if _, err := q.ExecContext(ctx, "UPDATE attempts SET next_seq=? WHERE id=?", a.NextSeq, a.ID); err != nil {
		return nil, err
	}
	return saved, nil
}

// v1 keeps partial observations separate from complete navigation time. Every
// epoch starts with explicit visibility; resumes never imply a visible page.
func eventMetricsV1(events []Event, index map[string]indexedNode, selected, outcome string, finalized bool) attemptMetrics {
	m := attemptMetrics{TimingQuality: "not_started", Direct: true}
	visible := false
	browsing, interrupted := false, false
	previousEpoch := ""
	var previousElapsed int64
	ancestors := map[string]bool{}
	for id := selected; id != ""; id = index[id].Parent {
		ancestors[id] = true
	}
	for _, e := range events {
		epoch := strings.TrimSpace(string(e.Epoch))
		nextVisible := visible
		if e.Visible != nil {
			nextVisible = *e.Visible
		}
		if e.Type == "visibility_hidden" {
			nextVisible = false
		}
		if e.Type == "visibility_visible" {
			nextVisible = true
		}
		if epoch != previousEpoch {
			m.ClockEpochs++
			if previousEpoch != "" {
				interrupted = true
			}
			// A missing boundary flag is never inferred as visible, including
			// when inspecting unsupported or damaged historical event data.
			nextVisible = e.Visible != nil && *e.Visible
		} else {
			knownEnd := nextVisible || e.Type == "visibility_hidden"
			if visible && knownEnd && e.Type != "resume" {
				m.ObservedNavigationMS += e.ElapsedMS - previousElapsed
			}
			if nextVisible != visible && e.Type != "visibility_hidden" && e.Type != "visibility_visible" {
				interrupted = true
			}
		}
		if !nextVisible {
			interrupted = true
		}
		switch e.Type {
		case "tree_shown":
			browsing = true
		case "resume", "visibility_hidden":
			interrupted = true
		case "back", "root":
			m.Backtracks++
			m.Direct = false
		case "enter":
			if !ancestors[e.NodeID] {
				m.Direct = false
			}
		case "select":
			if e.NodeID != selected {
				m.Direct = false
			}
		}
		previousEpoch, previousElapsed, visible = epoch, e.ElapsedMS, nextVisible
	}
	if !browsing {
		return m
	}
	m.TimingQuality = "interrupted"
	if finalized && !interrupted && len(events) > 1 && events[len(events)-1].Type == "submit" {
		m.TimingQuality = "complete"
		if outcome == "selected" || outcome == "gave_up" {
			value := m.ObservedNavigationMS
			m.NavigationMS = &value
		}
	}
	return m
}
