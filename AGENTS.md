# Development guide

## Project overview

This repository provides a self-hosted documentation tree-testing application.
The Go backend stores studies and anonymous participant results in SQLite; the
embedded `web/` frontend provides participant and administrator interfaces.

### Repository layout

- `cmd/treetest/`: CLI commands and application entry point.
- `internal/study/`: study bundle parsing, tree parsing, and validation.
- `internal/server/`: SQLite persistence, assignment, participant APIs, admin
  APIs, reporting, authentication, and policy enforcement.
- `web/`: embedded static UI and Node test suites. There is no frontend build
  step.
- `studies/`: importable, version-controlled study fixtures.
- `integration/`: real-browser smoke test.
- `deploy/`: systemd and Caddy deployment examples.

## Development workflow

- Use Go 1.27.1 or newer. Format Go changes with `gofmt`.
- Run `make test` for race-tested Go tests plus browser-independent Node tests.
- Run `make check` before submitting substantive changes; it also runs `go vet`.
- Build the production-style binary with `make build`.
- Do not commit runtime SQLite databases or their `-wal`/`-shm` files. Treat
  databases, browser storage, exports, passwords, identity keys, invitations,
  and participant data as sensitive.
- Preserve the participant/admin separation and blinding boundaries. In
  particular, public participant responses must not expose variant names, source
  paths, answer keys, or correctness before completion.

## Creating a study

Each study lives in `studies/<study-slug>/` and contains:

```text
studies/<study-slug>/
  study.json
  <variant-one>.md
  <variant-two>.md
  README.md                 # recommended: sources, rationale, and review notes
```

`studies/prometheus/` is the complete draft example. It compares two complete
Prometheus documentation menus, has twelve tasks (four per difficulty), and
uses six balanced six-task panels. Its README records pinned sources and why
each accepted destination is sufficient. It is a fixture to review and adapt,
not a real published study.

### `study.json`

- Set `schema_version` to `1`; use lowercase, hyphenated IDs for the study,
  variants, tasks, tree placement IDs, and content IDs.
- Include a participant-facing `title`, neutral `instructions`,
  `tasks_per_session`, at least two variants, tasks, and panels. `provenance` is
  optional but should record source URLs, immutable revisions/dates, extraction
  method, scope, and author review status.
- A variant has an `id`, author-facing `name`, and the filename of its sibling
  Markdown tree. Study loading rejects unknown JSON fields, missing trees, and
  unreferenced tree files.
- `tasks_per_session` must be a multiple of three from 3 through 30. Every
  panel must contain exactly that many distinct tasks and exactly one third each
  of `easy`, `medium`, and `hard`. Every task must have the same positive number
  of appearances across all panels.
- Each task needs a neutral, information-seeking `prompt`, one of those three
  difficulty labels, and non-empty `answers` for *every* variant. Answers are
  accepted content IDs, not displayed labels or placement IDs. Include every
  reviewed destination that genuinely provides enough guidance for the complete
  prompt, even when it is not the expected path.

### Tree Markdown

Use a heading optionally, then a nested list with two spaces per level:

```markdown
# Optional author metadata

- [Documentation](group:documentation)
  - [Install](page:install/install-guide)
  - [Configure](page:configure/configuration-guide)
```

- `group:placement-id` is an expandable, non-selectable branch.
- `page:placement-id/content-id` is a selectable destination. Pages may have
  children; a content ID may occur at multiple placements in the same tree.
- Placement IDs are unique within a tree. Content IDs represent the underlying
  destination independently of label or menu position.
- Labels are plain text. Do not use URLs, inline HTML, tabs, empty groups, or
  skipped indentation levels.

### Authoring checklist

1. Capture each variant's effective navigation from comparable, pinned source
   revisions. Keep the scope consistent (for example, exclude global links and
   version selectors from every variant).
2. Define content IDs from stable source identity rather than label or route
   where possible, then map every task answer separately for each variant.
3. Review the destination content, not just its title, before accepting it as
   an answer. Record task-to-source rationale and any arm differences in the
   study README.
4. Pilot the bundle before real enrollment; revise wording, answer keys, and
   difficulty based on review and pilot findings. Published versions are frozen,
   so content changes require a new version/run.
5. Validate before import:

   ```sh
   go run ./cmd/treetest validate --study studies/<study-slug>
   go test ./studies/<study-slug>
   ```

Study imports and admin uploads use the same parser and validator. A valid
bundle can then be imported with `treetest import`; create a pilot run before a
separate real-enrollment run.
