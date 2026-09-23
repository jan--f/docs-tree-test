package study

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Validate checks the full experimental contract and creates an independent
// snapshot. The hash covers the canonical JSON bundle, including source text.
func Validate(bundle Bundle) (*Snapshot, error) {
	c := bundle.Config
	if c.SchemaVersion != 1 {
		return nil, fmt.Errorf("schema_version must be 1")
	}
	if !identifier.MatchString(c.Slug) {
		return nil, fmt.Errorf("invalid study slug")
	}
	if strings.TrimSpace(c.Title) == "" || len(c.Title) > 240 {
		return nil, fmt.Errorf("title is required and must be at most 240 bytes")
	}
	if len(c.Instructions) > 8000 {
		return nil, fmt.Errorf("instructions exceed 8000 bytes")
	}
	if c.TasksPerSession < 3 || c.TasksPerSession > 30 || c.TasksPerSession%3 != 0 {
		return nil, fmt.Errorf("tasks_per_session must be a multiple of three between 3 and 30")
	}
	if len(c.Variants) < 2 || len(c.Variants) > 10 {
		return nil, fmt.Errorf("configure between 2 and 10 variants")
	}
	if len(c.Tasks) < c.TasksPerSession || len(c.Tasks) > 200 {
		return nil, fmt.Errorf("task bank must contain between tasks_per_session and 200 tasks")
	}
	if len(c.Panels) == 0 || len(c.Panels) > 1000 {
		return nil, fmt.Errorf("configure between 1 and 1000 task panels")
	}
	trees := make(map[string][]Node)
	contents := make(map[string]map[string]bool)
	usedFiles := make(map[string]bool)
	for _, v := range c.Variants {
		if !identifier.MatchString(v.ID) || trees[v.ID] != nil {
			return nil, fmt.Errorf("invalid or duplicate variant ID %q", v.ID)
		}
		if strings.TrimSpace(v.Name) == "" || len(v.Name) > 240 {
			return nil, fmt.Errorf("variant %q needs a name of at most 240 bytes", v.ID)
		}
		if !safeTreeName(v.Tree) {
			return nil, fmt.Errorf("variant %q: tree must name a sibling .md file", v.ID)
		}
		text, ok := bundle.Trees[v.Tree]
		if !ok {
			return nil, fmt.Errorf("missing tree file %q", v.Tree)
		}
		parsed, err := ParseTree(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", v.Tree, err)
		}
		trees[v.ID] = parsed
		usedFiles[v.Tree] = true
		contents[v.ID] = make(map[string]bool)
		var walk func([]Node)
		walk = func(nodes []Node) {
			for _, n := range nodes {
				if n.ContentID != "" {
					contents[v.ID][n.ContentID] = true
				}
				walk(n.Children)
			}
		}
		walk(parsed)
	}
	for name := range bundle.Trees {
		if !usedFiles[name] {
			return nil, fmt.Errorf("unused tree file %q", name)
		}
	}
	tasks := make(map[string]Task)
	for _, task := range c.Tasks {
		if !identifier.MatchString(task.ID) {
			return nil, fmt.Errorf("invalid task ID %q", task.ID)
		}
		if _, exists := tasks[task.ID]; exists {
			return nil, fmt.Errorf("duplicate task %q", task.ID)
		}
		if strings.TrimSpace(task.Prompt) == "" || len(task.Prompt) > 4000 {
			return nil, fmt.Errorf("task %q: prompt is required and must be at most 4000 bytes", task.ID)
		}
		if task.Difficulty != "easy" && task.Difficulty != "medium" && task.Difficulty != "hard" {
			return nil, fmt.Errorf("task %q has invalid difficulty", task.ID)
		}
		if len(task.Answers) != len(c.Variants) {
			return nil, fmt.Errorf("task %q requires answers for exactly the configured variants", task.ID)
		}
		for _, variant := range c.Variants {
			answers := task.Answers[variant.ID]
			if len(answers) == 0 {
				return nil, fmt.Errorf("task %q has no accepted answers for %q", task.ID, variant.ID)
			}
			seen := make(map[string]bool)
			for _, answer := range answers {
				if !contents[variant.ID][answer] {
					return nil, fmt.Errorf("task %q: answer %q is not reachable page content in %q", task.ID, answer, variant.ID)
				}
				if seen[answer] {
					return nil, fmt.Errorf("task %q has duplicate accepted answer %q", task.ID, answer)
				}
				seen[answer] = true
			}
		}
		tasks[task.ID] = task
	}
	exposure := make(map[string]int)
	for i, panel := range c.Panels {
		if len(panel) != c.TasksPerSession {
			return nil, fmt.Errorf("panel %d must contain %d tasks", i+1, c.TasksPerSession)
		}
		seen, difficulty := make(map[string]bool), make(map[string]int)
		for _, id := range panel {
			task, ok := tasks[id]
			if !ok || seen[id] {
				return nil, fmt.Errorf("panel %d has an unknown or duplicate task %q", i+1, id)
			}
			seen[id] = true
			exposure[id]++
			difficulty[task.Difficulty]++
		}
		for _, band := range []string{"easy", "medium", "hard"} {
			if difficulty[band] != c.TasksPerSession/3 {
				return nil, fmt.Errorf("panel %d must contain %d %s tasks", i+1, c.TasksPerSession/3, band)
			}
		}
	}
	expected := exposure[c.Tasks[0].ID]
	for _, task := range c.Tasks {
		if exposure[task.ID] == 0 || exposure[task.ID] != expected {
			return nil, fmt.Errorf("all tasks must have equal, positive exposure across panels (task %q has %d, expected %d)", task.ID, exposure[task.ID], expected)
		}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(raw) > 8<<20 {
		return nil, fmt.Errorf("bundle exceeds 8 MiB")
	}
	var frozen Bundle
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	return &Snapshot{Bundle: frozen, Hash: hex.EncodeToString(hash[:]), Trees: trees}, nil
}

func safeTreeName(name string) bool {
	return len(name) <= 128 && !strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name && strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, ".")
}

// LoadDir loads study.json and its explicitly referenced sibling trees. It
// rejects unknown JSON fields and trailing JSON, making typos visible to authors.
func LoadDir(dir string) (Bundle, error) {
	var bundle Bundle
	file, err := os.Open(filepath.Join(dir, "study.json"))
	if err != nil {
		return bundle, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return bundle, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return bundle, fmt.Errorf("study.json must be a regular file of at most 2 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (2<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle.Config); err != nil {
		return bundle, fmt.Errorf("study.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return bundle, fmt.Errorf("study.json: trailing data or excessive size")
	}
	bundle.Trees = make(map[string]string)
	for _, variant := range bundle.Config.Variants {
		if !safeTreeName(variant.Tree) {
			return bundle, fmt.Errorf("invalid tree filename %q", variant.Tree)
		}
		path := filepath.Join(dir, variant.Tree)
		info, err := os.Lstat(path)
		if err != nil {
			return bundle, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxTreeBytes {
			return bundle, fmt.Errorf("%s must be a regular file of at most %d bytes", variant.Tree, MaxTreeBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return bundle, err
		}
		bundle.Trees[variant.Tree] = string(data)
	}
	return bundle, nil
}
