package study

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxTreeBytes = 1 << 20

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,95}$`)
var menuLine = regexp.MustCompile(`^- \[([^\[\]<>]+)\]\((group|page):([a-z][a-z0-9-]{0,95})(?:/([a-z][a-z0-9-]{0,95}))?\)$`)

// ParseTree accepts a deliberately small Markdown dialect, preserving labels
// and sibling order. It never renders HTML or follows the links in the input.
func ParseTree(text string) ([]Node, error) {
	if len(text) > MaxTreeBytes || !utf8.ValidString(text) {
		return nil, fmt.Errorf("tree must be valid UTF-8 and at most %d bytes", MaxTreeBytes)
	}
	type entry struct {
		node        Node
		depth, line int
	}
	var entries []entry
	ids := make(map[string]bool)
	heading := false
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), MaxTreeBytes)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimRight(scanner.Text(), " \r")
		fail := func(message string) ([]Node, error) { return nil, fmt.Errorf("line %d: %s", lineNo, message) }
		if strings.ContainsRune(line, '\t') {
			return fail("tabs are not allowed; use two spaces per level")
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") && !heading && len(entries) == 0 {
			heading = true
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		spaces := len(line) - len(trimmed)
		if spaces%2 != 0 {
			return fail("indentation must be a multiple of two spaces")
		}
		depth := spaces / 2
		if depth > 12 {
			return fail("maximum nesting depth is 12")
		}
		if (len(entries) == 0 && depth != 0) || (len(entries) > 0 && depth > entries[len(entries)-1].depth+1) {
			return fail("nesting skips a level")
		}
		parts := menuLine.FindStringSubmatch(trimmed)
		if parts == nil {
			return fail("expected - [Label](group:id) or - [Label](page:location-id/content-id)")
		}
		label, kind, id, content := parts[1], parts[2], parts[3], parts[4]
		if strings.TrimSpace(label) != label || utf8.RuneCountInString(label) > 180 || strings.ContainsAny(label, "\x00\r\n") {
			return fail("labels must be plain text, without surrounding spaces, at most 180 characters")
		}
		if kind == "group" && content != "" {
			return fail("groups cannot have content IDs")
		}
		if kind == "page" && content == "" {
			return fail("pages require both location and content IDs")
		}
		if ids[id] {
			return fail("duplicate location ID: " + id)
		}
		ids[id] = true
		entries = append(entries, entry{Node{ID: id, Label: label, ContentID: content}, depth, lineNo})
		if len(entries) > 2000 {
			return fail("maximum node count is 2000")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("tree has no menu items")
	}
	index := 0
	var build func(int) ([]Node, error)
	build = func(depth int) ([]Node, error) {
		var nodes []Node
		for index < len(entries) && entries[index].depth == depth {
			e := entries[index]
			index++
			if index < len(entries) && entries[index].depth > depth {
				children, err := build(depth + 1)
				if err != nil {
					return nil, err
				}
				e.node.Children = children
			}
			if e.node.ContentID == "" && len(e.node.Children) == 0 {
				return nil, fmt.Errorf("line %d: group %q has no children", e.line, e.node.ID)
			}
			nodes = append(nodes, e.node)
		}
		return nodes, nil
	}
	return build(0)
}
