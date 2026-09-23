package prometheus_test

import (
	"sort"
	"testing"

	"github.com/jan--f/docs-tree-test/internal/study"
)

// Exercise the actual application parser and validator against the shipped
// author files, independently of the Python source-extraction check.
func TestStudyFixture(t *testing.T) {
	bundle, err := study.LoadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := study.Validate(bundle); err != nil {
		t.Fatal(err)
	}
	config := bundle.Config
	if len(config.Tasks) != 12 || len(config.Panels) != 6 || config.TasksPerSession != 6 {
		t.Fatal("expected the twelve-task, six-panel study design")
	}
	difficulties := make(map[string]string)
	for _, task := range config.Tasks {
		difficulties[task.ID] = task.Difficulty
	}
	// Validate enforces equal exposure and panel balance; additionally require
	// every pair within each of the three four-task bands exactly once.
	for _, band := range []string{"easy", "medium", "hard"} {
		pairs := make(map[[2]string]int)
		for _, panel := range config.Panels {
			var ids []string
			for _, id := range panel {
				if difficulties[id] == band {
					ids = append(ids, id)
				}
			}
			if len(ids) != 2 {
				t.Fatalf("panel lacks two %s tasks", band)
			}
			sort.Strings(ids)
			pairs[[2]string{ids[0], ids[1]}]++
		}
		if len(pairs) != 6 {
			t.Fatalf("%s: expected six unique pairs, got %d", band, len(pairs))
		}
		for pair, count := range pairs {
			if count != 1 {
				t.Fatalf("%s pair %v appears %d times", band, pair, count)
			}
		}
	}
}
