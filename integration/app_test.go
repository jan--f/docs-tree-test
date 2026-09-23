package integration_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jan--f/docs-tree-test/internal/server"
	"github.com/jan--f/docs-tree-test/internal/study"
	"github.com/jan--f/docs-tree-test/web"
)

// Exercise the actual Prometheus bundle, embedded frontend, enrollment protocol,
// all six responses, admin reporting, and a restored database as one workflow.
func TestPrometheusStudyEndToEnd(t *testing.T) {
	bundle, err := study.LoadDir("../studies/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := study.Validate(bundle); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(nil)
	origin := "http://" + httpServer.Listener.Addr().String()
	app, err := server.Open(filepath.Join(t.TempDir(), "study.sqlite"), web.Assets, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	httpServer.Config.Handler = app
	httpServer.Start()
	defer httpServer.Close()
	versionID, err := app.Import(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SetUser("owner", "Integration-owner-password-123", "owner"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	request := func(method, path string, body any) []byte {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			reader = bytes.NewReader(raw)
		}
		req, e := http.NewRequest(method, origin+path, reader)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, raw)
		}
		return raw
	}
	decode := func(raw []byte) map[string]any {
		t.Helper()
		var value map[string]any
		if e := json.Unmarshal(raw, &value); e != nil {
			t.Fatalf("decode: %v\n%s", e, raw)
		}
		return value
	}
	for _, path := range []string{"/", "/admin"} {
		raw := request("GET", path, nil)
		if !bytes.Contains(raw, []byte("<html")) {
			t.Fatalf("missing embedded HTML at %s", path)
		}
	}
	login := decode(request("POST", "/admin/api/login", map[string]string{"username": "owner", "password": "Integration-owner-password-123"}))
	csrf, _ = login["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("missing admin CSRF token")
	}
	run := decode(request("POST", "/admin/api/runs", map[string]string{"version_id": versionID, "slug": "integration-pilot", "mode": "pilot"}))
	runID, ok := run["id"].(string)
	if !ok || runID == "" {
		t.Fatalf("invalid run: %#v", run)
	}
	request("PATCH", "/admin/api/runs/"+runID, map[string]string{"status": "open"})
	request("GET", "/s/integration-pilot", nil)
	request("GET", "/api/public/integration-pilot", nil)
	state := decode(request("POST", "/api/public/integration-pilot/join", map[string]string{"experience": "regular", "docs_familiarity": "some"}))
	for index := 0; index < 6; index++ {
		if state["completed"] == true || int(state["task_index"].(float64)) != index {
			t.Fatalf("unexpected progress: %#v", state)
		}
		taskID := state["task"].(map[string]any)["id"].(string)
		state = decode(request("POST", "/api/public/integration-pilot/start", map[string]string{"command_id": "start-" + string(rune('a'+index)), "task_id": taskID}))
		attempt := state["attempt"].(map[string]any)
		attemptID := attempt["id"].(string)
		outcome := "gave_up"
		if index%2 == 0 {
			outcome = "skipped"
		}
		epoch := "epoch-" + string(rune('a'+index))
		var events []map[string]any
		if outcome != "skipped" {
			events = append(events, map[string]any{"id": "event-" + string(rune('a'+index)), "seq": 1, "type": "tree_shown", "elapsed_ms": 0, "epoch": epoch, "visible": true})
		}
		events = append(events, map[string]any{"id": "submit-" + string(rune('a'+index)), "seq": len(events) + 1, "type": "submit", "outcome": outcome, "elapsed_ms": 2500, "epoch": epoch, "visible": true})
		finish := map[string]any{"attempt_id": attemptID, "command_id": "finish-" + string(rune('a'+index)), "outcome": outcome, "events": events}
		first := request("POST", "/api/public/integration-pilot/finish", finish)
		retry := request("POST", "/api/public/integration-pilot/finish", finish)
		if !bytes.Equal(bytes.TrimSpace(first), bytes.TrimSpace(retry)) {
			t.Fatal("finish retry changed receipt")
		}
		if bytes.Contains(first, []byte(`"answers"`)) || bytes.Contains(first, []byte(`"correct"`)) || bytes.Contains(first, []byte(`"content_id"`)) {
			t.Fatal("participant response leaked scoring metadata")
		}
		state = decode(first)
	}
	if state["completed"] != true {
		t.Fatal("study did not complete")
	}
	stats := decode(request("GET", "/admin/api/runs/"+runID+"/results", nil))
	health := stats["health"].(map[string]any)
	if health["enrolled"] != float64(1) || health["completed"] != float64(1) {
		t.Fatalf("incorrect counts: %#v", health)
	}
	rows, err := csv.NewReader(strings.NewReader(string(request("GET", "/admin/api/runs/"+runID+"/export?format=csv", nil)))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("want one header and six assigned task rows, got %d", len(rows))
	}
	request("PATCH", "/admin/api/runs/"+runID, map[string]string{"status": "closed"})
	resumed := decode(request("POST", "/api/public/integration-pilot/join", map[string]string{}))
	if resumed["completed"] != true {
		t.Fatal("closed run lost existing enrollment")
	}
	backup := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := app.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored, err := server.Open(backup, web.Assets, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	req := httptest.NewRequest("GET", origin+"/api/public/integration-pilot/session", nil)
	u, _ := url.Parse(origin)
	for _, cookie := range jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	restored.ServeHTTP(response, req)
	if response.Code != 200 || decode(response.Body.Bytes())["completed"] != true {
		t.Fatalf("restored backup failed to resume: %d %s", response.Code, response.Body.String())
	}
}
