package study

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validBundle() Bundle {
	return Bundle{Config: Config{
		SchemaVersion: 1, Slug: "example", Title: "Example", TasksPerSession: 3,
		Variants: []Variant{{ID: "old", Name: "Old", Tree: "old.md"}, {ID: "new", Name: "New", Tree: "new.md"}},
		Tasks: []Task{
			{ID: "a", Prompt: "Find A", Difficulty: "easy", Answers: map[string][]string{"old": {"one"}, "new": {"one"}}},
			{ID: "b", Prompt: "Find B", Difficulty: "medium", Answers: map[string][]string{"old": {"two"}, "new": {"two"}}},
			{ID: "c", Prompt: "Find C", Difficulty: "hard", Answers: map[string][]string{"old": {"three"}, "new": {"three"}}},
		}, Panels: [][]string{{"a", "b", "c"}},
	}, Trees: map[string]string{
		"old.md": "# Menu\n- [Group](group:root)\n  - [A](page:a/one)\n  - [B](page:b/two)\n  - [C](page:c/three)\n",
		"new.md": "- [Renamed A](page:other-a/one)\n  - [B](page:b/two)\n- [C](page:c/three)\n- [Another A](page:duplicate-a/one)\n",
	}}
}

func TestTreeIdentitiesAndNestedPages(t *testing.T) {
	snapshot, err := Validate(validBundle())
	if err != nil {
		t.Fatal(err)
	}
	nodes := snapshot.Trees["new"]
	if len(nodes) != 3 || nodes[0].ContentID != "one" || len(nodes[0].Children) != 1 || nodes[2].ContentID != "one" {
		t.Fatalf("lost content/occurrence identities: %#v", nodes)
	}
	if nodes[0].Label != "Renamed A" {
		t.Fatal("label was altered")
	}
}

func TestMalformedMenus(t *testing.T) {
	for name, text := range map[string]string{
		"tabs":                 "- [A](group:a)\n\t- [B](page:b/b)",
		"skipped level":        "- [A](group:a)\n    - [B](page:b/b)",
		"duplicate":            "- [A](page:a/a)\n- [Again](page:a/a)",
		"empty group":          "- [Empty](group:empty)",
		"external link":        "- [A](https://example.org)",
		"HTML":                 "- [<script>alert(1)</script>](page:a/a)",
		"page missing content": "- [A](page:a)",
		"group has content":    "- [A](group:a/b)",
		"odd indent":           "- [A](group:a)\n   - [B](page:b/b)",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTree(text); err == nil {
				t.Fatal("invalid tree accepted")
			}
		})
	}
}

func TestAnswersAndPanelsAreValidated(t *testing.T) {
	cases := map[string]func(*Bundle){
		"group answer":          func(b *Bundle) { b.Config.Tasks[0].Answers["old"] = []string{"root"} },
		"missing variant":       func(b *Bundle) { delete(b.Config.Tasks[0].Answers, "new") },
		"duplicate panel task":  func(b *Bundle) { b.Config.Panels[0][1] = "a" },
		"unbalanced difficulty": func(b *Bundle) { b.Config.Tasks[1].Difficulty = "easy" },
		"unexposed task": func(b *Bundle) {
			b.Config.Tasks = append(b.Config.Tasks, Task{ID: "d", Prompt: "D", Difficulty: "easy", Answers: map[string][]string{"old": {"one"}, "new": {"one"}}})
		},
		"path traversal": func(b *Bundle) { b.Config.Variants[0].Tree = "../old.md" },
		"unknown variant": func(b *Bundle) {
			delete(b.Config.Tasks[0].Answers, "new")
			b.Config.Tasks[0].Answers["typo"] = []string{"one"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := validBundle()
			change(&b)
			if _, err := Validate(b); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestSnapshotHashAndIndependence(t *testing.T) {
	b := validBundle()
	a, err := Validate(b)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Validate(b)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != other.Hash {
		t.Fatal("non-deterministic hash")
	}
	b.Config.Tasks[0].Answers["old"][0] = "two"
	b.Trees["old.md"] = "changed"
	if a.Bundle.Config.Tasks[0].Answers["old"][0] != "one" || a.Bundle.Trees["old.md"] == "changed" {
		t.Fatal("snapshot retains mutable author data")
	}
}

func TestLoadRejectsUnknownFieldsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "study.json"), []byte(`{"schema_version":1,"typo":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "study.json"), []byte(`{"variants":[{"tree":"linked.md"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("study.json", filepath.Join(dir, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("symlink accepted")
	}
}
