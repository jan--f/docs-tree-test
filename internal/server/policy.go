package server

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jan--f/docs-tree-test/internal/study"
)

// Policy is part of a publication's identity, not mutable server configuration.
// Algorithm identifiers are safe to include in blinded exports.
type Policy struct {
	Version    string `json:"version"`
	Allocation string `json:"allocation"`
	Scoring    string `json:"scoring"`
	Timing     string `json:"timing"`
}

func policyV1() Policy {
	return Policy{"v1", "balanced-block-v1", "membership-v1", "visible-terminal-v1"}
}

func currentPolicy() Policy { return policyV1() }

type frozenSnapshot struct {
	study.Snapshot
	PublicationHash string
	Policy          Policy
}

type policyEngine struct {
	allocate func(study.Config) ([]allocation, error)
	order    func([]string) error
	correct  func(study.Task, string, string) bool
	metrics  func([]Event, map[string]indexedNode, string, string, bool) attemptMetrics
}

func resolvePolicy(p Policy) (policyEngine, error) {
	// Dispatch explicitly by the complete frozen policy. A future release must
	// retain old implementations or reject their versions, never reinterpret them.
	if p.Version != "v1" || p != policyV1() {
		return policyEngine{}, problem(409, "unsupported frozen study policy; this server cannot process this version")
	}
	return policyEngine{
		allocate: allocateV1,
		order:    shuffle[string],
		correct: func(task study.Task, variant, content string) bool {
			for _, accepted := range task.Answers[variant] {
				if accepted == content {
					return true
				}
			}
			return false
		},
		metrics: eventMetricsV1,
	}, nil
}

func decodePolicy(data string) (Policy, error) {
	var p Policy
	d := json.NewDecoder(strings.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, problem(409, "unsupported legacy frozen policy; republish the source bundle as a new version")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, problem(409, "unsupported frozen policy encoding")
	}
	_, err := resolvePolicy(p)
	return p, err
}

func policyJSON(p Policy) string { data, _ := json.Marshal(p); return string(data) }

func publicationHash(contentHash string, p Policy) string {
	data, _ := json.Marshal(struct {
		ContentHash string `json:"content_hash"`
		Policy      Policy `json:"policy"`
	}{contentHash, p})
	return digest(string(data))
}

func allocateV1(config study.Config) ([]allocation, error) {
	block := make([]allocation, 0, len(config.Variants)*len(config.Panels))
	for _, variant := range config.Variants {
		for panel := range config.Panels {
			block = append(block, allocation{variant.ID, panel})
		}
	}
	return block, shuffle(block)
}

type attemptMetrics struct {
	NavigationMS         *int64 `json:"navigation_ms"`
	ObservedNavigationMS int64  `json:"observed_navigation_ms"`
	TimingQuality        string `json:"timing_quality"`
	ClockEpochs          int    `json:"clock_epochs"`
	Direct               bool   `json:"direct_success"`
	Backtracks           int    `json:"backtracks"`
}

func serverElapsed(started, ended string) (int64, error) {
	start, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return 0, fmt.Errorf("invalid stored attempt start: %w", err)
	}
	end, err := time.Parse(time.RFC3339Nano, ended)
	if err != nil {
		return 0, fmt.Errorf("invalid attempt end: %w", err)
	}
	ms := end.Sub(start).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return ms, nil
}
