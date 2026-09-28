package server

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func invitationToken(t *testing.T, value string) string {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Fragment, "invite=")
}

func TestRecruitmentBoundsSuspensionAndRetentionPurge(t *testing.T) {
	e := setup(t)
	// Enforce enums and the configured enrollment cap before allocating storage.
	first := participant(t, e.s)
	call[map[string]any](t, first, "POST", "/api/public/test-run/join", map[string]string{"experience": "freeform", "docs_familiarity": "regularly"}, 422)
	if _, err := e.s.db.Exec("UPDATE runs SET enrollment_cap=1 WHERE id=?", e.runID); err != nil {
		t.Fatal(err)
	}
	enroll(t, first)
	second := participant(t, e.s)
	call[map[string]any](t, second, "POST", "/api/public/test-run/join", map[string]string{}, 429)

	// Invitation enrollment stores only a digest, is single-use, and works with
	// the participant cookie established by the landing page.
	invitedRun := call[run](t, e.owner, "POST", "/admin/api/runs", map[string]any{"version_id": e.version, "slug": "invite-run", "mode": "real", "recruitment": "invitation", "enrollment_cap": 2}, 200)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+invitedRun.ID, map[string]string{"status": "open"}, 200)
	invited := participant(t, e.s)
	call[map[string]any](t, invited, "POST", "/api/public/invite-run/join", map[string]string{}, 403)
	created := call[map[string]string](t, e.owner, "POST", "/admin/api/runs/"+invitedRun.ID+"/invitations", map[string]int{"expires_in_hours": 1}, 200)
	token := invitationToken(t, created["invite_url"])
	enrollInvite := call[Session](t, invited, "POST", "/api/public/invite-run/join", map[string]string{"invite_token": token}, 200)
	if enrollInvite.ID == "" {
		t.Fatal("invitation did not create a session")
	}
	other := participant(t, e.s)
	call[map[string]any](t, other, "POST", "/api/public/invite-run/join", map[string]string{"invite_token": token}, 403)

	current := call[Session](t, invited, "GET", "/api/public/invite-run/session", nil, 200)
	started := call[Session](t, invited, "POST", "/api/public/invite-run/start", map[string]string{"command_id": "invite-start", "task_id": current.Task.ID}, 200)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+invitedRun.ID, map[string]string{"status": "suspended"}, 200)
	call[map[string]any](t, invited, "POST", "/api/public/invite-run/events", map[string]any{"attempt_id": started.Attempt.ID, "events": []Event{event(1, "tree_shown", "", 0, "suspended")}}, 423)

	// A closed run can be previewed and explicitly purged, but no automatic
	// deletion occurs while a run is merely overdue.
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": "closed"}, 200)
	previews, err := e.s.PurgeCandidates(t.Context(), e.runID, false)
	if err != nil || len(previews) != 1 || previews[0].Sessions != 1 {
		t.Fatalf("purge preview: %+v %v", previews, err)
	}
	if err := e.s.PurgeRun(t.Context(), e.runID); err != nil {
		t.Fatal(err)
	}
	if count(t, e.s, "SELECT COUNT(*) FROM sessions WHERE run_id=?", e.runID) != 0 {
		t.Fatal("manual purge retained participant sessions")
	}
}

func TestRunScopedIdentitiesAndCompactStartReceipts(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	prompt := enroll(t, c)
	request := map[string]string{"command_id": "compact-start", "task_id": prompt.Task.ID}
	first := c.request("POST", "/api/public/test-run/start", request)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	if retry := c.request("POST", "/api/public/test-run/start", request); retry.Code != 200 || retry.Body.String() != first.Body.String() {
		t.Fatal("compact start receipt did not replay")
	}
	call[map[string]any](t, c, "POST", "/api/public/test-run/start", map[string]string{"command_id": "new-start", "task_id": prompt.Task.ID}, 409)

	secondRun := call[run](t, e.owner, "POST", "/admin/api/runs", map[string]string{"version_id": e.version, "slug": "identity-run", "mode": "real"}, 200)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+secondRun.ID, map[string]string{"status": "open"}, 200)
	secondSession := call[Session](t, c, "POST", "/api/public/identity-run/join", map[string]string{}, 200)
	var firstHash, secondHash string
	if err := e.s.db.QueryRow("SELECT identity_hash FROM sessions WHERE id=?", prompt.ID).Scan(&firstHash); err != nil {
		t.Fatal(err)
	}
	if err := e.s.db.QueryRow("SELECT identity_hash FROM sessions WHERE id=?", secondSession.ID).Scan(&secondHash); err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash || len(firstHash) != 64 || len(secondHash) != 64 {
		t.Fatalf("participant identities remain linkable: %q %q", firstHash, secondHash)
	}
}

func TestSuspensionCanResumeInEveryEnrollmentState(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	for i, status := range []string{"open", "paused", "closed"} {
		call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": "suspended"}, 200)
		eventType := "resume"
		if i == 0 {
			eventType = "tree_shown"
		}
		body := map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{event(i+1, eventType, "", int64(i), "suspension")}}
		call[map[string]any](t, c, "POST", "/api/public/test-run/events", body, 423)
		// Retention-only edits must not accidentally resume writes.
		call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]int{"retention_days": 30}, 200)
		call[map[string]any](t, c, "POST", "/api/public/test-run/events", body, 423)
		call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": status}, 200)
		call[map[string]any](t, c, "POST", "/api/public/test-run/events", body, 200)
	}
}

func TestLoginFailureCacheBoundExpiryAndDelays(t *testing.T) {
	l := newLoginLimiter()
	const source = "192.0.2.0/24"
	for i := 0; i < maxLoginFailures; i++ {
		l.failed(source, fmt.Sprintf("user-%d", i))
	}
	for i := 0; i < 5; i++ {
		l.failed(source, "user-0")
	}
	l.failed(source, "new-user")
	if len(l.failures) != maxLoginFailures || l.order.Len() != maxLoginFailures {
		t.Fatal("login failure tracking exceeded its capacity")
	}
	if l.failures[l.key(source, "user-1")] != nil {
		t.Fatal("oldest failure was not evicted")
	}
	if wait := l.blocked(source, "user-0"); wait <= 0 || wait > 30*time.Second {
		t.Fatalf("recent account lost its progressive delay at capacity: %v", wait)
	}
	l.succeeded(source, "user-0")
	if l.blocked(source, "user-0") != 0 || len(l.failures) != maxLoginFailures-1 {
		t.Fatal("successful login did not release its failure record")
	}
	for _, value := range l.failures {
		value.touched = time.Now().Add(-loginFailureLifetime - time.Second)
	}
	l.failed(source, "fresh-user")
	if len(l.failures) != 1 || l.order.Len() != 1 {
		t.Fatal("expired failure records were retained")
	}
	for i := 0; i < 12; i++ {
		l.failed(source, "fresh-user")
	}
	if wait := l.blocked(source, "fresh-user"); wait <= 0 || wait > 15*time.Minute {
		t.Fatalf("progressive delay is not bounded: %v", wait)
	}
	l.failures[l.key(source, "fresh-user")].touched = time.Now().Add(-loginFailureLifetime - time.Second)
	if l.blocked(source, "fresh-user") != 0 || len(l.failures) != 0 {
		t.Fatal("expired account delay was not cleared")
	}
}

func TestOversizedLoginNamesDoNotAllocateAccountTracking(t *testing.T) {
	s, err := Open(":memory:", nil, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := newClient(s)
	for _, username := range []string{strings.Repeat("x", 129), strings.Repeat("x", 8000)} {
		call[map[string]any](t, c, "POST", "/admin/api/login", map[string]string{"username": username, "password": "wrong"}, 401)
	}
	if len(s.loginLimits.failures) != 0 || len(s.limits.windows) != 1 {
		t.Fatal("invalid username allocated account-specific limiter state")
	}
}

func TestSuspendedLoginSourcePreservesOtherNetworksAccountBudget(t *testing.T) {
	e := setup(t)
	// Model an attacker already in the longest progressive suspension.
	for i := 0; i < 12; i++ {
		e.s.loginLimits.failed("198.51.100.0/24", "owner")
	}
	login := func(peer, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", testOrigin+"/admin/api/login", strings.NewReader(fmt.Sprintf(`{"username":"owner","password":%q}`, password)))
		r.RemoteAddr = peer
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 10; i++ {
		w := login("198.51.100.1:1234", "wrong")
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("suspended source: %d %s", w.Code, w.Body.String())
		}
	}
	w := login("203.0.113.1:1234", "long-test-password")
	if w.Code != 200 {
		t.Fatalf("unrelated network's valid login was blocked: %d %s", w.Code, w.Body.String())
	}
}

func TestTrustedProxySourceNetworks(t *testing.T) {
	for _, tc := range []struct {
		name, peer, forwarded, want string
		proxies                     []string
	}{
		{"loopback default", "127.0.0.1:42", "198.51.100.10", "198.51.100.0/24", nil},
		{"untrusted bridge", "172.17.0.1:42", "198.51.100.10", "172.17.0.0/24", nil},
		{"trusted bridge", "172.17.0.1:42", "198.51.100.10", "198.51.100.0/24", []string{"172.17.0.1"}},
		{"trusted subnet", "172.18.0.2:42", "203.0.113.10", "203.0.113.0/24", []string{"172.18.0.0/24"}},
		{"untrusted spoof", "192.0.2.10:42", "198.51.100.10", "192.0.2.0/24", []string{"172.17.0.1"}},
		{"trust disabled", "127.0.0.1:42", "198.51.100.10", "127.0.0.0/24", []string{}},
		{"invalid header", "172.17.0.1:42", "invalid", "172.17.0.0/24", []string{"172.17.0.1"}},
		{"IPv6", "[::1]:42", "2001:db8:abcd:1234::a", "2001:db8:abcd:1234::/64", nil},
		{"mapped peer", "[::ffff:172.17.0.1]:42", "198.51.100.10", "198.51.100.0/24", []string{"172.17.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxies, err := parseTrustedProxies(tc.proxies)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{trustedProxies: proxies}
			r := httptest.NewRequest("POST", "/api/public/test-run/join", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := s.sourceNetwork(r); got != tc.want {
				t.Fatalf("source = %s, want %s", got, tc.want)
			}
		})
	}
	if _, err := OpenWithOptions(":memory:", nil, testOrigin, Options{TrustedProxies: []string{"not-an-address"}}); err == nil {
		t.Fatal("invalid proxy configuration was accepted")
	}
	s, err := OpenWithOptions(":memory:", nil, testOrigin, Options{TrustedProxies: []string{"172.17.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := httptest.NewRequest("POST", "/api/public/test-run/join", nil)
	r.RemoteAddr = "172.17.0.1:42"
	r.Header.Set("X-Forwarded-For", "198.51.100.10")
	for i := 0; i < 60; i++ {
		if err := s.limitPublicSource(httptest.NewRecorder(), r); err != nil {
			t.Fatal(err)
		}
	}
	if s.limitPublicSource(httptest.NewRecorder(), r) == nil {
		t.Fatal("source quota was not enforced")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	if err := s.limitPublicSource(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("unrelated proxy client was throttled: %v", err)
	}
}

func TestRetentionEditsUseClosureTime(t *testing.T) {
	e := setup(t)
	path := "/admin/api/runs/" + e.runID
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "closed"}, 200)
	var initialClosed, initialTarget string
	if err := e.s.db.QueryRow("SELECT closed_at,retention_target_at FROM runs WHERE id=?", e.runID).Scan(&initialClosed, &initialTarget); err != nil {
		t.Fatal(err)
	}
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "closed"}, 200)
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND closed_at=? AND retention_target_at=?", e.runID, initialClosed, initialTarget) != 1 {
		t.Fatal("repeated close reset the retention clock")
	}
	// Model a run closed 60 days ago under its original 90-day policy.
	closed := time.Now().UTC().AddDate(0, 0, -60)
	if _, err := e.s.db.Exec("UPDATE runs SET closed_at=?,retention_target_at=? WHERE id=?", closed.Format(time.RFC3339Nano), closed.AddDate(0, 0, 90).Format(time.RFC3339Nano), e.runID); err != nil {
		t.Fatal(err)
	}
	call[map[string]any](t, e.owner, "PATCH", path, map[string]int{"retention_days": 30}, 200)
	candidates, err := e.s.PurgeCandidates(t.Context(), e.runID, true)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("shortened retention did not make the run overdue: %+v %v", candidates, err)
	}
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "suspended"}, 200)
	call[map[string]any](t, e.owner, "PATCH", path, map[string]int{"retention_days": 120}, 200)
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND write_suspended=1 AND closed_at=? AND retention_target_at=?", e.runID, closed.Format(time.RFC3339Nano), closed.AddDate(0, 0, 120).Format(time.RFC3339Nano)) != 1 {
		t.Fatal("suspended retention edit changed the closure anchor or resumed writes")
	}
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "closed"}, 200)
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND closed_at=?", e.runID, closed.Format(time.RFC3339Nano)) != 1 {
		t.Fatal("resuming a closed run reset its closure time")
	}
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "open"}, 200)
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND closed_at IS NULL AND retention_target_at IS NULL", e.runID) != 1 {
		t.Fatal("reopened run retained its previous retention period")
	}
	before := time.Now().UTC()
	call[map[string]any](t, e.owner, "PATCH", path, map[string]string{"status": "closed"}, 200)
	var closedAt, targetAt string
	if err := e.s.db.QueryRow("SELECT closed_at,retention_target_at FROM runs WHERE id=?", e.runID).Scan(&closedAt, &targetAt); err != nil {
		t.Fatal(err)
	}
	newClosed, err := time.Parse(time.RFC3339Nano, closedAt)
	if err != nil || newClosed.Before(before) || targetAt != newClosed.AddDate(0, 0, 120).Format(time.RFC3339Nano) {
		t.Fatalf("new closure did not start a fresh retention period: %s %s %v", closedAt, targetAt, err)
	}
}

func TestThirtyTaskStudyWithCommandRetries(t *testing.T) {
	e := setup(t)
	b := fixture()
	b.Config.TasksPerSession = 30
	base := b.Config.Tasks[0]
	b.Config.Tasks = nil
	panel := []string{}
	for i := 0; i < 30; i++ {
		task := base
		task.ID = fmt.Sprintf("long-task-%d", i)
		task.Difficulty = []string{"easy", "medium", "hard"}[i%3]
		b.Config.Tasks = append(b.Config.Tasks, task)
		panel = append(panel, task.ID)
	}
	b.Config.Panels = [][]string{panel}
	version, err := e.s.Import(b)
	if err != nil {
		t.Fatal(err)
	}
	r := call[run](t, e.owner, "POST", "/admin/api/runs", map[string]string{"version_id": version, "slug": "long-run", "mode": "pilot"}, 200)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+r.ID, map[string]string{"status": "open"}, 200)
	c := participant(t, e.s)
	current := call[Session](t, c, "POST", "/api/public/long-run/join", map[string]string{}, 200)
	var startBody map[string]string
	var finalBody map[string]any
	for i := 0; i < 30; i++ {
		startBody = map[string]string{"command_id": fmt.Sprintf("start-%d", i), "task_id": current.Task.ID}
		current = call[Session](t, c, "POST", "/api/public/long-run/start", startBody, 200)
		for retry := 0; retry < 2; retry++ {
			got := call[Session](t, c, "POST", "/api/public/long-run/start", startBody, 200)
			if !reflect.DeepEqual(current, got) {
				t.Fatal("start replay changed its receipt")
			}
		}
		finalBody = finishBody(current, fmt.Sprintf("finish-%d", i), "skipped", "", []Event{submit(1, "", 0, "skip", "skipped")})
		current = call[Session](t, c, "POST", "/api/public/long-run/finish", finalBody, 200)
		for retry := 0; retry < 2; retry++ {
			got := call[Session](t, c, "POST", "/api/public/long-run/finish", finalBody, 200)
			if !reflect.DeepEqual(current, got) {
				t.Fatal("finish replay changed its receipt")
			}
		}
	}
	if !current.Completed || count(t, e.s, "SELECT COUNT(*) FROM attempts WHERE session_id=?", current.ID) != 30 || count(t, e.s, "SELECT COUNT(*) FROM commands WHERE session_id=?", current.ID) != 60 {
		t.Fatal("retries prevented completion or allocated additional attempts/receipts")
	}
	// Only matching receipts bypass the exhausted participant budget.
	changedStart := map[string]string{"command_id": startBody["command_id"], "task_id": "different-task"}
	call[map[string]any](t, c, "POST", "/api/public/long-run/start", changedStart, 429)
	finalBody["outcome"] = "gave_up"
	call[map[string]any](t, c, "POST", "/api/public/long-run/finish", finalBody, 429)
	finalBody["outcome"] = "skipped"
	call[Session](t, c, "POST", "/api/public/long-run/finish", finalBody, 200)

	// Replays still spend the per-run quota even with no writes left to make.
	for i := 91; i < 500; i++ {
		e.s.limits.allow("public:run:start:"+r.ID, 500, time.Minute)
	}
	w := c.request("POST", "/api/public/long-run/start", startBody)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("replay escaped run throttling: %d %s", w.Code, w.Body.String())
	}
	// The source limiter also remains in front of receipt lookup.
	for i := 92; i < 120; i++ {
		call[Session](t, c, "POST", "/api/public/long-run/finish", finalBody, 200)
	}
	w = c.request("POST", "/api/public/long-run/finish", finalBody)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("replay escaped source throttling: %d %s", w.Code, w.Body.String())
	}
}

func TestPurgeSanitizesDatabaseAndRetriesBlockedWAL(t *testing.T) {
	e := setup(t)
	c := participant(t, e.s)
	enroll(t, c)
	a := startTask(t, c)
	marker := "unique-sensitive-purge-event-" + randomID()
	ev := event(1, "tree_shown", "", 0, "purge")
	ev.ID = marker
	call[map[string]any](t, c, "POST", "/api/public/test-run/events", map[string]any{"attempt_id": a.Attempt.ID, "events": []Event{ev}}, 200)
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": "closed"}, 200)
	if _, err := e.s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(e.db)
	if err != nil || !bytes.Contains(data, []byte(marker)) {
		t.Fatalf("fixture marker missing from database: %v", err)
	}
	reader, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var n int
	if err := readTx.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.PurgeRun(t.Context(), e.runID); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("blocked cleanup reported success: %v", err)
	}
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND purge_pending=1 AND purged_at IS NULL", e.runID) != 1 {
		t.Fatal("failed cleanup was marked complete")
	}
	call[map[string]any](t, e.owner, "PATCH", "/admin/api/runs/"+e.runID, map[string]string{"status": "open"}, 409)
	previews, err := e.s.PurgeCandidates(t.Context(), "", true)
	if err != nil || len(previews) != 1 || previews[0].RunID != e.runID {
		t.Fatalf("pending cleanup was not retryable: %+v %v", previews, err)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.s.PurgeRun(t.Context(), e.runID); err != nil {
		t.Fatal(err)
	}
	if count(t, e.s, "SELECT COUNT(*) FROM runs WHERE id=? AND purge_pending=0 AND purged_at IS NOT NULL", e.runID) != 1 {
		t.Fatal("successful cleanup was not marked complete")
	}
	if count(t, e.s, "SELECT COUNT(*) FROM sessions WHERE run_id=?", e.runID) != 0 || count(t, e.s, "SELECT COUNT(*) FROM versions") != 1 {
		t.Fatal("purge did not preserve only study definitions")
	}
	for _, path := range []string{e.db, e.db + "-wal"} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("purged event remains in %s", path)
		}
	}
	previews, err = e.s.PurgeCandidates(t.Context(), e.runID, false)
	if err != nil || len(previews) != 1 {
		t.Fatalf("explicit repeat cleanup unavailable: %+v %v", previews, err)
	}
	if err := e.s.PurgeRun(t.Context(), e.runID); err != nil {
		t.Fatal(err)
	}
}
