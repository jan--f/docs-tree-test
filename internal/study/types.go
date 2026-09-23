// Package study parses and validates reproducible tree-test study bundles.
package study

type Bundle struct {
	Config Config            `json:"config"`
	Trees  map[string]string `json:"trees"`
}

type Config struct {
	SchemaVersion   int               `json:"schema_version"`
	Slug            string            `json:"slug"`
	Title           string            `json:"title"`
	Instructions    string            `json:"instructions"`
	TasksPerSession int               `json:"tasks_per_session"`
	Variants        []Variant         `json:"variants"`
	Tasks           []Task            `json:"tasks"`
	Panels          [][]string        `json:"panels"`
	Provenance      map[string]string `json:"provenance,omitempty"`
}

type Variant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tree string `json:"tree"`
}

type Task struct {
	ID         string              `json:"id"`
	Prompt     string              `json:"prompt"`
	Difficulty string              `json:"difficulty"`
	Answers    map[string][]string `json:"answers"`
}

// ID identifies a placement; ContentID identifies a selectable page. A group
// has an empty ContentID. Pages may have children and may appear more than once.
type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	ContentID string `json:"content_id,omitempty"`
	Children  []Node `json:"children,omitempty"`
}

type Snapshot struct {
	Bundle Bundle
	Hash   string
	Trees  map[string][]Node // keyed by private variant ID
}
