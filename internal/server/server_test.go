package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jan--f/docs-tree-test/internal/study"
)

const testOrigin = "http://study.test"

var testAssets = fstest.MapFS{
	"index.html":       {Data: []byte("<!doctype html><title>Index</title>")},
	"participant.html": {Data: []byte("<!doctype html><title>Participant</title>")},
	"admin.html":       {Data: []byte("<!doctype html><title>Admin</title>")},
	"assets/app.js":    {Data: []byte("console.log('loaded')")},
}

func fixture() study.Bundle {
	b := study.Bundle{Config: study.Config{SchemaVersion: 1, Slug: "source-study", Title: "Find your way", Instructions: "Choose a page.", TasksPerSession: 3,
		Variants: []study.Variant{{ID: "private-old", Name: "Private old design", Tree: "private-old.md"}, {ID: "private-new", Name: "Private new design", Tree: "private-new.md"}},
		Panels:   [][]string{{"private-task-0", "private-task-1", "private-task-2"}, {"private-task-3", "private-task-4", "private-task-5"}}}, Trees: map[string]string{}}
	for i := 0; i < 6; i++ {
		b.Config.Tasks = append(b.Config.Tasks, study.Task{ID: fmt.Sprintf("private-task-%d", i), Prompt: fmt.Sprintf("Find the requested information, situation %d.", i), Difficulty: []string{"easy", "medium", "hard"}[i%3], Answers: map[string][]string{"private-old": {"secret-target"}, "private-new": {"secret-target"}}})
	}
	for i, v := range b.Config.Variants {
		b.Trees[v.Tree] = fmt.Sprintf("- [Tree label %d](group:%s-group)\n  - [Target page](page:%s-target/secret-target)\n  - [Other page](page:%s-decoy/secret-other)\n  - [Duplicate target](page:%s-duplicate/secret-target)\n- [Other branch](group:%s-other)\n  - [Elsewhere](page:%s-else/secret-else)\n", i, v.ID, v.ID, v.ID, v.ID, v.ID, v.ID)
	}
	return b
}

type client struct {
	s       *Server
	mu      sync.Mutex
	cookies map[string]*http.Cookie
	csrf    string
}

func newClient(s *Server) *client { return &client{s: s, cookies: map[string]*http.Cookie{}} }

func (c *client) request(method, path string, value any) *httptest.ResponseRecorder {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, testOrigin+path, body)
	r.Header.Set("Origin", testOrigin)
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	for _, cookie := range c.cookies {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	c.mu.Unlock()
	w := httptest.NewRecorder()
	c.s.ServeHTTP(w, r)
	c.mu.Lock()
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge == -1 {
			delete(c.cookies, cookie.Name)
		} else {
			c.cookies[cookie.Name] = cookie
		}
	}
	c.mu.Unlock()
	return w
}

func call[T any](t *testing.T, c *client, method, path string, body any, status int) T {
	t.Helper()
	w := c.request(method, path, body)
	if w.Code != status {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %s: %v: %s", path, err, w.Body.String())
	}
	return result
}

func loginClient(t *testing.T, s *Server, username string) *client {
	t.Helper()
	c := newClient(s)
	a := call[adminIdentity](t, c, "POST", "/admin/api/login", map[string]string{"username": username, "password": "long-test-password"}, 200)
	c.csrf = a.CSRF
	return c
}

type environment struct {
	s                  *Server
	owner              *client
	db, version, runID string
}

func setup(t *testing.T) *environment {
	t.Helper()
	db := filepath.Join(t.TempDir(), "study.db")
	s, err := Open(db, testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SetUser("owner", "long-test-password", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUser("analyst", "long-test-password", "analyst"); err != nil {
		t.Fatal(err)
	}
	version, err := s.Import(fixture())
	if err != nil {
		t.Fatal(err)
	}
	owner := loginClient(t, s, "owner")
	a := call[run](t, owner, "POST", "/admin/api/runs", map[string]string{"version_id": version, "slug": "test-run", "mode": "pilot"}, 200)
	if a.Status != "paused" {
		t.Fatal("new run was not paused")
	}
	call[map[string]any](t, owner, "PATCH", "/admin/api/runs/"+a.ID, map[string]string{"status": "open"}, 200)
	return &environment{s, owner, db, version, a.ID}
}

func participant(t *testing.T, s *Server) *client {
	t.Helper()
	c := newClient(s)
	call[map[string]any](t, c, "GET", "/api/public/test-run", nil, 200)
	return c
}

func enroll(t *testing.T, c *client) Session {
	t.Helper()
	return call[Session](t, c, "POST", "/api/public/test-run/join", map[string]string{"experience": "some", "docs_familiarity": "occasionally"}, 200)
}

func startTask(t *testing.T, c *client) Session {
	t.Helper()
	current := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200)
	return call[Session](t, c, "POST", "/api/public/test-run/start", map[string]string{"command_id": randomID(), "task_id": current.Task.ID}, 200)
}

func event(seq int, typ, node string, elapsed int64, epoch string) Event {
	raw, _ := json.Marshal(epoch)
	visible := typ != "visibility_hidden"
	return Event{ID: fmt.Sprintf("event-%s-%d", epoch, seq), Seq: seq, Type: typ, NodeID: node, ElapsedMS: elapsed, Epoch: raw, Visible: &visible}
}

func submit(seq int, node string, elapsed int64, epoch, outcome string) Event {
	e := event(seq, "submit", node, elapsed, epoch)
	e.Outcome = outcome
	return e
}

func finishBody(a Session, command, outcome, node string, events []Event) map[string]any {
	body := map[string]any{"attempt_id": a.Attempt.ID, "command_id": command, "outcome": outcome, "events": events}
	if node != "" {
		body["node_id"] = node
	}
	return body
}

func count(t *testing.T, s *Server, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnrollmentConcurrencyAndClosedResume(t *testing.T) {
	e := setup(t)
	second, err := Open(e.db, testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	clients := make([]*client, 32)
	for i := range clients {
		s := e.s
		if i%2 == 0 {
			s = second
		}
		clients[i] = participant(t, s)
	}
	if n := count(t, e.s, "SELECT COUNT(*) FROM sessions"); n != 0 {
		t.Fatalf("landing page enrolled %d people", n)
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			w := c.request("POST", "/api/public/test-run/join", map[string]string{"experience": "new", "docs_familiarity": "never"})
			if w.Code != 200 {
				t.Errorf("concurrent enrollment: %d %s", w.Code, w.Body.String())
			}
		}(c)
	}
	wg.Wait()
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := clients[0].request("POST", "/api/public/test-run/join", map[string]string{})
			if w.Code != 200 {
				t.Errorf("retry: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if n := count(t, e.s, "SELECT COUNT(*) FROM sessions"); n != 32 {
		t.Fatalf("allocated %d sessions", n)
	}
	rows, err := e.s.db.Query("SELECT variant_id,panel_index,COUNT(*) FROM sessions GROUP BY variant_id,panel_index")
	if err != nil {
		t.Fatal(err)
	}
	groups := 0
	for rows.Next() {
		var variant string
		var panel, n int
		if err := rows.Scan(&variant, &panel, &n); err != nil {
			t.Fatal(err)
		}
		if n != 8 {
			t.Errorf("unbalanced variant/panel block %s/%d: %d", variant, panel, n)
		}
		groups++
	}
	rows.Close()
	if groups != 4 {
		t.Fatalf("only %d allocation combinations", groups)
	}
	first := call[Session](t, clients[0], "GET", "/api/public/test-run/session", nil, 200)
	if first.Attempt != nil || first.TaskIndex != 0 {
		t.Fatal("enrollment skipped prompt phase")
	}
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": "closed"}, 200)
	resumed := enroll(t, clients[0])
	if !reflect.DeepEqual(first, resumed) {
		t.Fatal("join did not resume original assignment")
	}
	unjoined := participant(t, e.s)
	call[map[string]any](t, unjoined, "POST", "/api/public/test-run/join", map[string]string{}, 409)
	current := startTask(t, clients[0])
	call[Session](t, clients[0], "POST", "/api/public/test-run/finish", finishBody(current, "closed-skip", "skipped", "", []Event{submit(1, "", 0, "closed", "skipped")}), 200)
	if n := count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE outcome='skipped'"); n != 1 {
		t.Fatal("closing enrollment prevented an existing session finishing")
	}
	var storedHash string
	if err := e.s.db.QueryRow("SELECT identity_hash FROM sessions WHERE id=?", first.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == clients[0].cookies[participantCookie].Value || len(storedHash) != 64 {
		t.Fatal("raw bearer token was persisted")
	}
}

func TestEventsAtomicFinishAndReceipts(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	prompt := enroll(t, c)
	if prompt.Attempt != nil || count(t, e.s, "SELECT COUNT(*) FROM attempts") != 0 {
		t.Fatal("task started before /start")
	}
	startBody := map[string]string{"command_id": "start-once", "task_id": prompt.Task.ID}
	first := c.request("POST", "/api/public/test-run/start", startBody)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	var a Session
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	retry := c.request("POST", "/api/public/test-run/start", startBody)
	if !bytes.Equal(first.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("start receipt changed")
	}
	if got := startTask(t, c); got.Attempt.ID != a.Attempt.ID {
		t.Fatal("second start created a second attempt")
	}
	group, target := a.Tree[0].ID, a.Tree[0].Children[2].ID // duplicate placement of the accepted content
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "missing-events", "gave_up", "", nil), 422)
	checkpoint := []Event{event(1, "tree_shown", "", 0, "first"), event(2, "enter", group, 300, "first")}
	post := func(events []Event, want int) map[string]any {
		return call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": events}, want)
	}
	if got := post(checkpoint, 200)["next_seq"]; got != float64(3) {
		t.Fatalf("next sequence %v", got)
	}
	post(checkpoint, 200)
	changed := append([]Event(nil), checkpoint...)
	changed[1].ElapsedMS++
	post(changed, 409)
	post([]Event{event(4, "select", target, 900, "first")}, 409)
	idConflict := event(3, "select", target, 900, "first")
	idConflict.ID = checkpoint[0].ID
	post([]Event{idConflict}, 409)
	post([]Event{event(3, "select", target, 100, "first")}, 409)
	other := participant(t, e.s)
	otherSession := enroll(t, other)
	post([]Event{event(3, "select", otherSession.Tree[0].Children[0].ID, 900, "first")}, 422)
	call[map[string]any](t, other, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": checkpoint}, 404)
	call[map[string]any](t, other, "POST", "/api/public/test-run/finish", finishBody(a, "stolen", "gave_up", "", checkpoint), 404)
	final := event(3, "select", target, 900, "first")
	invalidFinish := finishBody(a, "bad-selection", "selected", group, []Event{final, submit(4, group, 1200, "first", "selected")})
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", invalidFinish, 422)
	if count(t, e.s, "SELECT COUNT(*) FROM events WHERE attempt_id=?", a.Attempt.ID) != 2 {
		t.Fatal("failed finish committed final events")
	}
	request := finishBody(a, "finish-once", "selected", target, []Event{final, submit(4, target, 1200, "first", "selected")})
	w := c.request("POST", "/api/public/test-run/finish", request)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var next Session
	if err := json.Unmarshal(w.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if next.TaskIndex != 1 || next.Attempt != nil {
		t.Fatal("finish failed to advance to next prompt")
	}
	if strings.Contains(w.Body.String(), "correct") || strings.Contains(w.Body.String(), "secret-") {
		t.Fatal("participant received scoring information")
	}
	for i := 0; i < 3; i++ {
		retry := c.request("POST", "/api/public/test-run/finish", request)
		if retry.Code != 200 || !bytes.Equal(w.Body.Bytes(), retry.Body.Bytes()) {
			t.Fatal("finish did not return original command receipt")
		}
	}
	second := startTask(t, c)
	retry = c.request("POST", "/api/public/test-run/finish", request)
	if !bytes.Equal(w.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("receipt changed after starting next attempt")
	}
	badRetry := finishBody(a, "finish-once", "gave_up", "", []Event{final})
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", badRetry, 409)
	post([]Event{final}, 200)
	post([]Event{event(4, "resume", group, 1000, "first")}, 409)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE correct=1 AND direct=1 AND navigation_ms=1200 AND observed_navigation_ms=1200 AND timing_quality='complete' AND clock_epochs=1") != 1 {
		t.Fatal("server did not score accepted content placement or derive timing/directness")
	}
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(second, "skip", "skipped", "", []Event{submit(1, "", 0, "skip", "skipped")}), 200)
	third := startTask(t, c)
	end := call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(third, "give-up", "gave_up", "", []Event{event(1, "tree_shown", "", 0, "give-up"), submit(2, "", 200, "give-up", "gave_up")}), 200)
	if !end.Completed || end.Task != nil || end.Attempt != nil || len(end.Tree) != 0 {
		t.Fatal("completed session shape is wrong")
	}
	var result struct {
		Health health    `json:"health"`
		Arms   []Summary `json:"arms"`
	}
	result = call[struct {
		Health health    `json:"health"`
		Arms   []Summary `json:"arms"`
	}](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/results", nil, 200)
	if result.Health.Enrolled != 2 || result.Health.Completed != 1 || result.Health.Incomplete != 1 || result.Health.LastEventAt == nil {
		t.Fatalf("health: %+v", result.Health)
	}
	var assigned, shown, correct, skipped, gaveUp, unreached int
	for _, arm := range result.Arms {
		assigned += arm.Assigned
		shown += arm.Shown
		correct += arm.Correct
		skipped += arm.Skipped
		gaveUp += arm.GaveUp
		unreached += arm.Unreached
		if arm.Assigned != arm.Correct+arm.Incorrect+arm.GaveUp+arm.Skipped+arm.Unfinished+arm.Unreached {
			t.Fatal("summary denominator lost tasks")
		}
		if arm.Assigned > 0 && arm.SuccessYield != float64(arm.Correct)/float64(arm.Assigned) {
			t.Fatal("wrong yield denominator")
		}
	}
	if assigned != 6 || shown != 3 || correct != 1 || skipped != 1 || gaveUp != 1 || unreached != 3 {
		t.Fatalf("inconsistent summaries: assigned=%d shown=%d correct=%d skipped=%d gave_up=%d unreached=%d", assigned, shown, correct, skipped, gaveUp, unreached)
	}
}

func TestRestartSnapshotPinningAndBackup(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	checkpoint := []Event{event(1, "tree_shown", "", 0, "before-restart"), event(2, "enter", a.Tree[0].ID, 100, "before-restart")}
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": checkpoint}, 200)
	before := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200)
	changed := fixture()
	changed.Config.Title = "Changed author title"
	for _, task := range changed.Config.Tasks {
		for variant := range task.Answers {
			task.Answers[variant][0] = "secret-other"
		}
	}
	for file, tree := range changed.Trees {
		changed.Trees[file] = strings.ReplaceAll(tree, "Target page", "Changed author label")
	}
	otherVersion, err := e.s.Import(changed)
	if err != nil {
		t.Fatal(err)
	}
	if otherVersion == e.version {
		t.Fatal("changed bundle reused a frozen version")
	}
	changed.Config.Title = "Changed in memory again"
	if _, err := e.s.db.Exec("UPDATE versions SET title='corrupted' WHERE id=?", e.version); err == nil {
		t.Fatal("immutable version accepted SQL update")
	}
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(e.db, testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c.s = reopened
	after := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed session\nbefore: %+v\nafter: %+v", before, after)
	}
	if after.Title != "Find your way" || after.Tree[0].Children[0].Label != "Target page" {
		t.Fatal("published run followed mutable author data")
	}
	final := []Event{event(3, "resume", after.Tree[0].ID, 0, "after-restart"), event(4, "select", after.Tree[0].Children[0].ID, 70, "after-restart"), submit(5, after.Tree[0].Children[0].ID, 100, "after-restart", "selected")}
	request := finishBody(after, "finish-after-restart", "selected", after.Tree[0].Children[0].ID, final)
	call[Session](t, c, "POST", "/api/public/test-run/finish", request, 200)
	if count(t, reopened, "SELECT COUNT(*) FROM attempts WHERE correct=1 AND navigation_ms IS NULL AND observed_navigation_ms=200 AND timing_quality='interrupted' AND clock_epochs=2") != 1 {
		t.Fatal("scoring or clock was not pinned across restart")
	}
	var mode string
	if err := reopened.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL: %s %v", mode, err)
	}
	if count(t, reopened, "PRAGMA foreign_keys") != 1 || count(t, reopened, "PRAGMA synchronous") != 2 {
		t.Fatal("durability/foreign keys not configured")
	}
	backup := filepath.Join(t.TempDir(), "snapshot.db")
	if err := reopened.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Backup(backup); err == nil {
		t.Fatal("backup overwrote an existing destination")
	}
	copy, err := Open(backup, testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	c.s = copy
	resumed := call[Session](t, c, "GET", "/api/public/test-run/session", nil, 200)
	if resumed.ID != after.ID || resumed.TaskIndex != 1 || resumed.Attempt != nil {
		t.Fatal("backup lost WAL-backed progress")
	}
	call[Session](t, c, "POST", "/api/public/test-run/finish", request, 200)
	if count(t, copy, "SELECT COUNT(*) FROM events") != 5 {
		t.Fatal("backup lost events")
	}
}

func TestEpochTimingBacktrackingAndEventValidation(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	group, target := a.Tree[0].ID, a.Tree[0].Children[0].ID
	events := []Event{
		event(1, "tree_shown", "", 1000, "one"), event(2, "enter", group, 1100, "one"),
		event(3, "visibility_hidden", group, 1200, "one"), event(4, "visibility_visible", group, 10000, "one"),
		event(5, "back", "", 10100, "one"), event(6, "resume", "", 50, "two"),
		event(7, "enter", group, 80, "two"), event(8, "select", target, 150, "two"),
		submit(9, target, 200, "two", "selected"),
	}
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": events[:6]}, 200)
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{event(7, "resume", "", 10200, "one")}}, 409)
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{event(7, "select", target, 80, "two")}}, 422)
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "timed-finish", "selected", target, events[6:]), 200)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE correct=1 AND navigation_ms IS NULL AND observed_navigation_ms=450 AND timing_quality='interrupted' AND backtracks=1 AND direct=0") != 1 {
		t.Fatal("hidden/cross-epoch intervals or backtracking were scored incorrectly")
	}
	b := startTask(t, c)
	bad := map[string]any{"attempt_id": b.Attempt.ID, "events": []map[string]any{{"id": "missing-elapsed", "seq": 1, "type": "tree_shown", "epoch": "x"}}}
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", bad, 400)
}

func assertBlinded(t *testing.T, data string) {
	t.Helper()
	for _, forbidden := range []string{"private-old", "private-new", "private-task-", "secret-target", "secret-other", "Private old design", "Private new design", "Tree label", "Target page", "source-study", "answers", "bundle", "content_id", "variant_id", "panel_index"} {
		if strings.Contains(data, forbidden) {
			t.Fatalf("blinded response contains %q: %s", forbidden, data)
		}
	}
}

func TestAuthorizationDraftRevisionsAndBlindedExports(t *testing.T) {
	e := setup(t)
	analyst := loginClient(t, e.s, "analyst")
	public := participant(t, e.s)
	prompt := enroll(t, public)
	data, _ := json.Marshal(prompt)
	// Participants see tree labels, but never private node/task/variant identifiers.
	for _, secret := range []string{"private-", "secret-", "answers", "content_id", "correct", ".md"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("participant disclosure: %s", data)
		}
	}
	a := startTask(t, public)
	call[map[string]any](t, public, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{event(1, "tree_shown", "", 0, "owner-export")}}, 200)
	call[map[string]any](t, newClient(e.s), "GET", "/admin/api/runs", nil, 401)
	call[map[string]any](t, newClient(e.s), "POST", "/admin/api/login", map[string]string{"username": "owner", "password": "wrong"}, 401)
	for _, item := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/admin/api/validate", fixture()}, {"GET", "/admin/api/drafts", nil}, {"POST", "/admin/api/drafts", fixture()},
		{"GET", "/admin/api/drafts/id", nil}, {"PUT", "/admin/api/drafts/id", map[string]any{}}, {"POST", "/admin/api/drafts/id/publish", map[string]any{}},
		{"GET", "/admin/api/versions", nil}, {"POST", "/admin/api/runs", map[string]any{}}, {"PATCH", "/admin/api/runs/" + e.runID, map[string]any{}},
		{"GET", "/admin/api/runs/" + e.runID + "/key", nil}, {"GET", "/admin/api/runs/" + e.runID + "/events", nil},
	} {
		call[map[string]any](t, analyst, item.method, item.path, item.body, 403)
	}
	csrf := e.owner.csrf
	e.owner.csrf = "wrong"
	call[map[string]any](t, e.owner, "POST", "/admin/api/validate", fixture(), 403)
	e.owner.csrf = csrf
	validation := call[map[string]any](t, e.owner, "POST", "/admin/api/validate", fixture(), 200)
	if validation["task_count"] != float64(6) || validation["panel_count"] != float64(2) {
		t.Fatalf("validation summary: %v", validation)
	}
	draft := call[map[string]any](t, e.owner, "POST", "/admin/api/drafts", fixture(), 200)
	id := draft["id"].(string)
	changed := fixture()
	changed.Config.Title = "Draft second revision"
	call[map[string]any](t, e.owner, "PUT", "/admin/api/drafts/"+id, map[string]any{"revision": 1, "bundle": changed}, 200)
	call[map[string]any](t, e.owner, "PUT", "/admin/api/drafts/"+id, map[string]any{"revision": 1, "bundle": fixture()}, 409)
	call[map[string]any](t, e.owner, "POST", "/admin/api/drafts/"+id+"/publish", map[string]int{"revision": 1}, 409)
	version := call[map[string]any](t, e.owner, "POST", "/admin/api/drafts/"+id+"/publish", map[string]int{"revision": 2}, 200)
	duplicate := call[map[string]any](t, e.owner, "POST", "/admin/api/drafts/"+id+"/publish", map[string]int{"revision": 2}, 200)
	if !reflect.DeepEqual(version, duplicate) {
		t.Fatal("publishing identical content created another version")
	}
	changed.Config.Title = "Mutable draft third revision"
	call[map[string]any](t, e.owner, "PUT", "/admin/api/drafts/"+id, map[string]any{"revision": 2, "bundle": changed}, 200)
	got := call[map[string]any](t, e.owner, "GET", "/admin/api/drafts/"+id, nil, 200)
	if got["revision"] != float64(3) {
		t.Fatal("draft revision did not advance")
	}
	call[map[string]any](t, e.owner, "GET", "/admin/api/drafts", nil, 200)
	versions := call[map[string]any](t, e.owner, "GET", "/admin/api/versions", nil, 200)
	versionsJSON, _ := json.Marshal(versions)
	if strings.Contains(string(versionsJSON), "Mutable draft") || !strings.Contains(string(versionsJSON), "Draft second revision") {
		t.Fatal("published version changed with draft")
	}
	for _, path := range []string{"/admin/api/runs", "/admin/api/runs/" + e.runID + "/results", "/admin/api/runs/" + e.runID + "/export?format=json", "/admin/api/runs/" + e.runID + "/export?format=csv"} {
		w := analyst.request("GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("analyst %s: %d %s", path, w.Code, w.Body.String())
		}
		assertBlinded(t, w.Body.String())
	}
	export := call[struct {
		Rows []AssignedRow `json:"rows"`
	}](t, analyst, "GET", "/admin/api/runs/"+e.runID+"/export?format=json", nil, 200)
	if len(export.Rows) != 3 || export.Rows[0].Outcome != "unfinished" || export.Rows[1].Outcome != "unreached" {
		t.Fatalf("assigned rows lost unfinished/unreached tasks: %+v", export.Rows)
	}
	csvResponse := analyst.request("GET", "/admin/api/runs/"+e.runID+"/export?format=csv", nil)
	csvRows, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	if err != nil || len(csvRows) != 4 {
		t.Fatalf("CSV rows: %v %v", csvRows, err)
	}
	key := e.owner.request("GET", "/admin/api/runs/"+e.runID+"/key", nil)
	if key.Code != 200 || !strings.Contains(key.Body.String(), "private-old.md") || !strings.Contains(key.Body.String(), "secret-target") {
		t.Fatal("owner key lacks immutable source")
	}
	detail := e.owner.request("GET", "/admin/api/runs/"+e.runID+"/events", nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "received_at") || !strings.Contains(detail.Body.String(), "node_ids") {
		t.Fatal("owner event export incomplete")
	}
	real := call[run](t, e.owner, "POST", "/admin/api/runs", map[string]string{"version_id": e.version, "slug": "real-run", "mode": "real"}, 200)
	call[map[string]any](t, public, "GET", "/api/public/real-run", nil, 200)
	call[map[string]any](t, public, "POST", "/api/public/real-run/join", map[string]string{}, 409)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+real.ID, map[string]string{"status": "open"}, 200)
	realSession := call[Session](t, public, "POST", "/api/public/real-run/join", map[string]string{}, 200)
	if realSession.ID == prompt.ID {
		t.Fatal("pilot and real enrollments were combined")
	}
	call[map[string]any](t, analyst, "POST", "/admin/api/logout", map[string]any{}, 200)
	session := call[adminIdentity](t, analyst, "GET", "/admin/api/session", nil, 200)
	if session.Authenticated {
		t.Fatal("logout did not revoke session")
	}
}

func TestOriginCookiesStaticAndStrictRequests(t *testing.T) {
	e := setup(t)
	call[map[string]any](t, newClient(e.s), "GET", "/api/public/test-run/session", nil, 404)
	for _, origin := range []string{"", "https://evil.test", "null"} {
		r := httptest.NewRequest("POST", testOrigin+"/admin/api/login", strings.NewReader(`{"username":"owner","password":"long-test-password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("origin %q was accepted: %d", origin, w.Code)
		}
	}
	for _, path := range []string{"/", "/admin", "/s/test-run", "/assets/app.js"} {
		w := newClient(e.s).request("GET", path, nil)
		if w.Code != 200 || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("static route %s: %d", path, w.Code)
		}
	}
	c := participant(t, e.s)
	cookie := c.cookies[participantCookie]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("HTTP cookie: %+v", cookie)
	}
	r := httptest.NewRequest("POST", testOrigin+"/api/public/test-run/join", strings.NewReader(`{"experience":"x","docs_familiarity":"x","variant_id":"private-old"}`))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("unknown request fields accepted")
	}
	secure, err := Open(e.db, testAssets, "https://study.test")
	if err != nil {
		t.Fatal(err)
	}
	defer secure.Close()
	r = httptest.NewRequest("GET", "https://study.test/api/public/test-run", nil)
	w = httptest.NewRecorder()
	secure.ServeHTTP(w, r)
	if len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS cookie is not Secure")
	}
	if err := e.s.SetUser("owner", "replacement-password", "owner"); err != nil {
		t.Fatal(err)
	}
	call[map[string]any](t, e.owner, "GET", "/admin/api/runs", nil, 401)
}
