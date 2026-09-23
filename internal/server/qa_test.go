package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jan--f/docs-tree-test/internal/study"
	"golang.org/x/crypto/bcrypt"
)

func setVisible(e Event, visible bool) Event { e.Visible = &visible; return e }

func TestTerminalSubmissionAndStaleStreams(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	prompt := enroll(t, c)
	call[map[string]any](t, c, "POST", "/api/public/test-run/start", map[string]string{"command_id": "missing-task"}, 422)
	startRequest := map[string]string{"command_id": "pinned-start", "task_id": prompt.Task.ID}
	initial := c.request("POST", "/api/public/test-run/start", startRequest)
	if initial.Code != 200 {
		t.Fatal(initial.Body.String())
	}
	var a Session
	if err := json.Unmarshal(initial.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	group, target := a.Tree[0].ID, a.Tree[0].Children[0].ID
	checkpoint := []Event{event(1, "tree_shown", "", 100, "tab-a"), event(2, "enter", group, 200, "tab-a"), event(3, "select", target, 300, "tab-a")}
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": checkpoint}, 200)
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "no-terminal", "selected", target, nil), 422)
	terminal := submit(4, target, 1400, "tab-a", "selected")
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{terminal}}, 422)
	for name, events := range map[string][]Event{
		"stale selection":          {submit(3, target, 500, "tab-a", "selected")},
		"changed terminal outcome": {submit(4, target, 1400, "tab-a", "gave_up")},
		"changed terminal node":    {submit(4, a.Tree[0].Children[1].ID, 1400, "tab-a", "selected")},
		"two terminals":            {terminal, submit(5, target, 1500, "tab-a", "selected")},
		"action after terminal":    {terminal, event(5, "select", target, 1500, "tab-a")},
	} {
		t.Run(name, func(t *testing.T) {
			want := 422
			if name == "stale selection" {
				want = 409
			}
			call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(a, name, "selected", target, events), want)
		})
	}
	withoutVisibility := terminal
	withoutVisibility.Visible = nil
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "missing-visibility", "selected", target, []Event{withoutVisibility}), 422)
	if count(t, e.s, "SELECT COUNT(*) FROM events WHERE attempt_id=?", a.Attempt.ID) != 3 {
		t.Fatal("rejected terminals altered the stream")
	}
	request := finishBody(a, "terminal-once", "selected", target, []Event{terminal})
	finished := c.request("POST", "/api/public/test-run/finish", request)
	if finished.Code != 200 {
		t.Fatal(finished.Body.String())
	}
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE navigation_ms=1300 AND observed_navigation_ms=1300 AND timing_quality='complete' AND clock_epochs=1 AND correct=1") != 1 {
		t.Fatal("confirmation time was omitted")
	}
	call[map[string]any](t, c, "POST", "/api/public/test-run/start", map[string]string{"command_id": "stale-prompt", "task_id": prompt.Task.ID}, 409)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts") != 1 {
		t.Fatal("stale prompt started the next task")
	}
	if retry := c.request("POST", "/api/public/test-run/start", startRequest); retry.Code != 200 || !bytes.Equal(retry.Body.Bytes(), initial.Body.Bytes()) {
		t.Fatal("old start receipt was not returned before task comparison")
	}
	second := startTask(t, c)
	if retry := c.request("POST", "/api/public/test-run/finish", request); retry.Code != 200 || !bytes.Equal(retry.Body.Bytes(), finished.Body.Bytes()) {
		t.Fatal("terminal receipt changed after advancement")
	}
	// Thinking time without any intervening navigation belongs to the visible
	// trace too, and is measured by the terminal event rather than receipt time.
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(second, "thought-then-gave-up", "gave_up", "", []Event{event(1, "tree_shown", "", 100, "thinking"), submit(2, "", 5100, "thinking", "gave_up")}), 200)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE outcome='gave_up' AND navigation_ms=5000") != 1 {
		t.Fatal("give-up thinking time was omitted")
	}
	third := startTask(t, c)
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(third, "gave-up-without-tree", "gave_up", "", []Event{submit(1, "", 0, "prompt-only", "gave_up")}), 422)
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(third, "prompt-skip", "skipped", "", []Event{submit(1, "", 0, "prompt-only", "skipped")}), 200)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE outcome='skipped' AND navigation_ms IS NULL AND observed_navigation_ms=0 AND timing_quality='not_started'") != 1 {
		t.Fatal("prompt skip fabricated browsing")
	}
	if count(t, e.s, "SELECT COUNT(*) FROM events WHERE attempt_id=?", third.Attempt.ID) != 1 {
		t.Fatal("prompt skip contains a fictitious tree_shown")
	}
}

func TestHiddenResumeAndExplicitEpochVisibility(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	post := func(events []Event, want int) {
		call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": events}, want)
	}
	first := event(1, "tree_shown", "", 100, "initial")
	first.Visible = nil
	post([]Event{first}, 422)
	post([]Event{event(1, "tree_shown", "", 100, "initial"), event(2, "visibility_hidden", "", 200, "initial")}, 200)
	newEpoch := event(3, "resume", "", 10, "background")
	newEpoch.Visible = nil
	post([]Event{newEpoch}, 422)
	post([]Event{event(3, "visibility_visible", "", 10, "background")}, 422)
	post([]Event{setVisible(event(3, "resume", "", 10, "background"), false)}, 200)
	post([]Event{event(4, "resume", "", 300, "initial")}, 409)
	post([]Event{setVisible(event(4, "resume", "", 5, "background"), false)}, 409)
	// A same-epoch background resume must not turn visibility back on either.
	post([]Event{setVisible(event(4, "resume", "", 2000, "background"), false), event(5, "visibility_visible", "", 8000, "background")}, 200)
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "hidden-finish", "gave_up", "", []Event{submit(6, "", 8500, "background", "gave_up")}), 200)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE id=? AND navigation_ms IS NULL AND observed_navigation_ms=600 AND timing_quality='interrupted' AND clock_epochs=2", a.Attempt.ID) != 1 {
		t.Fatal("hidden resume counted background time or claimed complete timing")
	}
	export := call[struct {
		Rows []AssignedRow `json:"rows"`
	}](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/export?format=json", nil, 200)
	if r := export.Rows[0]; r.NavigationMS != nil || r.ServerElapsedMS == nil || r.ObservedNavigationMS != 600 || r.TimingQuality != "interrupted" || r.ClockEpochs != 2 {
		t.Fatalf("incorrect export timing: %+v", r)
	}
	results := call[struct {
		Arms []Summary `json:"arms"`
	}](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/results", nil, 200)
	interrupted := 0
	for _, arm := range results.Arms {
		interrupted += arm.InterruptedAttempts
		if arm.MedianNavigationMS != nil {
			t.Fatal("interrupted attempt entered median")
		}
	}
	if interrupted != 1 {
		t.Fatalf("interrupted_attempts=%d", interrupted)
	}
	second := startTask(t, c)
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": second.Attempt.ID, "events": []Event{setVisible(event(1, "tree_shown", "", 0, "initially-hidden"), false)}}, 200)
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(second, "hidden-submit", "gave_up", "", []Event{setVisible(submit(2, "", 10000, "initially-hidden", "gave_up"), false)}), 200)
	if count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE id=? AND observed_navigation_ms=0 AND navigation_ms IS NULL AND timing_quality='interrupted'", second.Attempt.ID) != 1 {
		t.Fatal("initially hidden epoch inferred visibility")
	}
}

func TestCredentialResetBetweenVerificationAndSessionCreation(t *testing.T) {
	e := setup(t)
	var verifiedHash []byte
	var verifiedRole string
	if err := e.s.db.QueryRow("SELECT password_hash,role FROM users WHERE username='owner'").Scan(&verifiedHash, &verifiedRole); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(verifiedHash, []byte("long-test-password")); err != nil {
		t.Fatal(err)
	}
	// This is exactly the window between the login's expensive verification and
	// its session-creation transaction. No scheduler timing or sleeps are needed.
	if err := e.s.SetUser("owner", "new-reset-password", "analyst"); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.s.createAdminSession(context.Background(), "owner", verifiedHash, verifiedRole, "")
	var denied *apiError
	if !errors.As(err, &denied) || denied.status != 401 {
		t.Fatalf("stale credentials created a session: %v", err)
	}
	if count(t, e.s, "SELECT COUNT(*) FROM admin_sessions WHERE username='owner'") != 0 {
		t.Fatal("reset did not leave the account revoked")
	}
	call[map[string]any](t, e.owner, "GET", "/admin/api/runs", nil, 401)
	current := newClient(e.s)
	session := call[adminIdentity](t, current, "POST", "/admin/api/login", map[string]string{"username": "owner", "password": "new-reset-password"}, 200)
	if session.Role != "analyst" {
		t.Fatal("new login retained pre-reset role")
	}
	if err := e.s.db.QueryRow("SELECT password_hash,role FROM users WHERE username='owner'").Scan(&verifiedHash, &verifiedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec("UPDATE users SET role='owner' WHERE username='owner'"); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.s.createAdminSession(context.Background(), "owner", verifiedHash, verifiedRole, "")
	if !errors.As(err, &denied) || denied.status != 401 {
		t.Fatalf("changed role bypassed reset guard: %v", err)
	}
}

func TestOwnerExportIncludesAllAttemptLifecycles(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	// Make elapsed-wall-time assertions deterministic while leaving the real
	// database/handler path responsible for calculating and persisting metrics.
	start := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	if _, err := e.s.db.Exec("UPDATE attempts SET started_at=? WHERE id=?", start, a.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	type detail struct {
		Policy   Policy            `json:"policy"`
		Attempts []ownerAttempt    `json:"attempts"`
		Events   []json.RawMessage `json:"events"`
	}
	zero := call[detail](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/events", nil, 200)
	if len(zero.Attempts) != 1 || len(zero.Events) != 0 {
		t.Fatalf("zero-event attempt missing: %+v", zero)
	}
	if x := zero.Attempts[0]; x.ID != a.Attempt.ID || x.StartedAt != start || x.FinishedAt != nil || x.Outcome != nil || x.NodeID != nil || x.NextSeq != 1 || x.ServerElapsedMS < 2000 || x.TimingQuality != "not_started" || x.Policy != currentPolicy() {
		t.Fatalf("unfinished lifecycle: %+v", x)
	}
	call[Session](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "lifecycle-give-up", "gave_up", "", []Event{event(1, "tree_shown", "", 100, "lifecycle"), submit(2, "", 1100, "lifecycle", "gave_up")}), 200)
	second := startTask(t, c)
	final := call[detail](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/events", nil, 200)
	if len(final.Attempts) != 2 || len(final.Events) != 2 {
		t.Fatalf("lifecycle export dropped attempts: %+v", final)
	}
	if x := final.Attempts[0]; x.FinishedAt == nil || x.Outcome == nil || *x.Outcome != "gave_up" || x.NextSeq != 3 || x.NavigationMS == nil || *x.NavigationMS != 1000 || x.ObservedNavigationMS != 1000 || x.ServerElapsedMS < 2000 || x.TimingQuality != "complete" {
		t.Fatalf("final lifecycle: %+v", x)
	}
	if final.Attempts[1].ID != second.Attempt.ID || final.Attempts[1].NextSeq != 1 {
		t.Fatal("next zero-event attempt missing")
	}
	again := call[detail](t, e.owner, "GET", "/admin/api/runs/"+e.runID+"/events", nil, 200)
	if final.Attempts[0].ServerElapsedMS != again.Attempts[0].ServerElapsedMS {
		t.Fatal("final server timing was recomputed instead of frozen")
	}
	analyst := loginClient(t, e.s, "analyst")
	call[map[string]any](t, analyst, "GET", "/admin/api/runs/"+e.runID+"/events", nil, 403)
	rows := call[struct {
		Rows []AssignedRow `json:"rows"`
	}](t, analyst, "GET", "/admin/api/runs/"+e.runID+"/export?format=json", nil, 200)
	if rows.Rows[1].NavigationMS != nil || rows.Rows[1].ServerElapsedMS == nil || rows.Rows[2].ServerElapsedMS != nil {
		t.Fatal("unfinished and unreached timing were conflated")
	}
}

func TestPolicyPublicationIdentityAndUnsupportedDispatch(t *testing.T) {
	e := setup(t)
	snapshot, err := study.Validate(fixture())
	if err != nil {
		t.Fatal(err)
	}
	var hash, contentHash, policyData string
	if err := e.s.db.QueryRow("SELECT hash,content_hash,scoring_policy FROM versions WHERE id=?", e.version).Scan(&hash, &contentHash, &policyData); err != nil {
		t.Fatal(err)
	}
	if contentHash != snapshot.Hash || hash == contentHash || hash != publicationHash(contentHash, currentPolicy()) || policyData != policyJSON(currentPolicy()) {
		t.Fatal("publication identity did not freeze content plus policy")
	}
	duplicate, err := e.s.Import(fixture())
	if err != nil || duplicate != e.version {
		t.Fatalf("identical publication did not deduplicate: %s %v", duplicate, err)
	}
	unsupported := currentPolicy()
	unsupported.Timing = "future-timing-v2"
	if publicationHash(contentHash, unsupported) == hash {
		t.Fatal("policy change reused publication identity")
	}
	if _, err := insertVersionWithPolicy(context.Background(), e.s.db, snapshot, unsupported); err == nil {
		t.Fatal("unknown policy was silently published as v1")
	}
	if count(t, e.s, "SELECT COUNT(*) FROM versions") != 1 {
		t.Fatal("unsupported publication wrote a version")
	}
	data, _ := json.Marshal(snapshot)
	// Simulate a database produced by another version of the application. A
	// self-consistent future hash must still be rejected by this engine.
	futureID := randomID()
	if _, err := e.s.db.Exec("INSERT INTO versions(id,hash,content_hash,title,slug,snapshot_json,scoring_policy,created_at) VALUES(?,?,?,?,?,?,?,?)", futureID, publicationHash(contentHash, unsupported), contentHash, "Future", "future", string(data), policyJSON(unsupported), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshot(context.Background(), e.s.db, futureID); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("future policy was silently reinterpreted: %v", err)
	}
	call[map[string]any](t, e.owner, "POST", "/admin/api/runs", map[string]string{"version_id": futureID, "slug": "future-run", "mode": "real"}, 409)
	legacyID := randomID()
	if _, err := e.s.db.Exec("INSERT INTO versions(id,hash,title,slug,snapshot_json,scoring_policy,created_at) VALUES(?,?,?,?,?,?,?)", legacyID, contentHash, "Legacy", "legacy", string(data), "old unversioned prose", now()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshot(context.Background(), e.s.db, legacyID); err == nil || !strings.Contains(err.Error(), "unsupported legacy") {
		t.Fatalf("legacy policy was silently reinterpreted: %v", err)
	}
	duplicate, err = e.s.Import(fixture())
	if err != nil || duplicate != e.version || duplicate == legacyID {
		t.Fatal("bundle-only preview hash collided with policy-aware publication")
	}
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	if _, err := e.s.db.Exec("UPDATE attempts SET policy_json=? WHERE id=?", policyJSON(unsupported), a.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/api/runs/" + e.runID + "/results", "/admin/api/runs/" + e.runID + "/export?format=json", "/admin/api/runs/" + e.runID + "/events"} {
		response := call[map[string]any](t, e.owner, "GET", path, nil, 409)
		if !strings.Contains(response["error"].(string), "unsupported") {
			t.Fatalf("unclear policy failure: %v", response)
		}
	}
	call[map[string]any](t, c, "GET", "/api/public/test-run/session", nil, 409)
	call[map[string]any](t, c, "POST", "/api/public/test-run/finish", finishBody(a, "unsupported-finish", "skipped", "", []Event{submit(1, "", 0, "unknown", "skipped")}), 409)
	if count(t, e.s, "SELECT COUNT(*) FROM events") != 0 {
		t.Fatal("unsupported policy appended data")
	}
}
