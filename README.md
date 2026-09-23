# Documentation tree test

A self-hosted, Go-based tree-testing app for comparing documentation menus.
Participants follow one link, practice, and complete six destination-finding
tasks using one randomly assigned navigation structure. Results accumulate in
SQLite on your server. A web admin manages Markdown trees, task banks, study
versions, pilot/real enrollment, and blinded exports.

The included study compares the current Prometheus documentation menu with the
effective navigation of `feature/proposed-docs-structure`. It covers the whole
menu, with twelve draft tasks spanning easy, intermediate, and complex needs.
Review the task prompts and accepted destinations, then pilot them before
opening real enrollment. See [fixture provenance](studies/prometheus/README.md).

## Quick start

Requires Go 1.25 or newer. The application embeds its frontend; there is no
JavaScript build step. One process and a persistent local SQLite file are enough.

```sh
go build -trimpath -o bin/treetest ./cmd/treetest
./bin/treetest validate --study studies/prometheus
./bin/treetest user --db data/study.sqlite --username owner --role owner
./bin/treetest import --db data/study.sqlite --study studies/prometheus
./bin/treetest serve --db data/study.sqlite --public-url http://127.0.0.1:8080
```

The `user` command prints a generated password. To choose your own, supply
`--password-file /path/to/password` or the `TREETEST_PASSWORD` environment
variable. Repeating the command resets that account's credentials.

Open **http://127.0.0.1:8080/admin**, log in, create a **pilot** run from the
imported version, then open enrollment. Share its `/s/{run-slug}` link. Create a
separate **real** run once the content and protocol are ready.

An optional independent analyst can have a separate account:

```sh
./bin/treetest user --db data/study.sqlite --username analyst --role analyst
```

## Authoring a study

A study directory contains `study.json` and one Markdown file per variant.
The web admin accepts the same files together through **Import study files**, and
supports editing/pasting their text individually.
The owner can save a draft, validate it, preview trees, publish a frozen version,
and create runs. Draft saves use revisions to detect concurrent edits.

### Markdown menu dialect

```markdown
# Navigation

- [Prometheus](group:prometheus)
  - [Querying](group:querying)
    - [Basics](page:query-basics/promql-basics)
    - [Functions](page:query-functions/promql-functions)
- [Learn](group:learn)
  - [Query basics](page:learn-query-basics/promql-basics)
```

- Use two spaces per nesting level. Sibling order is preserved.
- `group:LOCATION_ID` is an expandable, non-selectable heading.
- `page:LOCATION_ID/CONTENT_ID` is a selectable destination and can have children.
- Location IDs must be unique within each tree. Content IDs identify the actual
  destination independently of its label and position; repeated placements may
  share one content ID, as above.
- IDs use lowercase letters, digits and hyphens, starting with a letter.
- Labels are plain text. The optional leading heading is author metadata.
- Ordinary URLs, inline HTML, arbitrary Markdown, empty groups, tabs and skipped
  indentation levels are rejected. Tree links are metadata, not external links.

### Tasks and assignment

The complete example is in [study.json](studies/prometheus/study.json). Its main
fields are:

```json
{
  "schema_version": 1,
  "slug": "prometheus-navigation",
  "title": "Finding information in Prometheus documentation",
  "instructions": "Choose where you would expect to find the information.",
  "tasks_per_session": 6,
  "variants": [
    {"id": "current", "name": "Current menu", "tree": "current.md"},
    {"id": "candidate", "name": "Candidate menu", "tree": "candidate.md"}
  ],
  "tasks": [
    {
      "id": "example-task",
      "prompt": "Where would you look up how to select time series by their labels?",
      "difficulty": "medium",
      "answers": {"current": ["promql-basics"], "candidate": ["promql-basics"]}
    }
  ],
  "panels": [],
  "provenance": {"source": "Record pinned source revisions here"}
}
```

This abbreviated illustration needs a full balanced task bank and panels to
validate. Each task's `answers` lists accepted **content IDs for each variant**.
Every listed answer must be reachable and genuinely contain sufficient guidance.
All placements of accepted content are credited. Groups and ancestors are not
automatically correct. Additional genuinely sufficient pages can be accepted.

The initial bank has four tasks in each of `easy`, `medium`, and `hard`. Six
panels each contain two tasks of each difficulty; each task appears in three
panels. An allocation block contains each **variant × panel** combination once,
in random order. Each participant's task order is shuffled independently.
Completed blocks balance assigned exposure; dropouts are never reassigned.
Larger balanced banks and task counts (multiples of three, up to 30) are supported.

Publishing freezes the original bundle, source hash, prompts, answer mappings,
panels and machine-readable scoring/allocation policy. Version identity includes
the policy as well as the content; an unsupported policy cannot silently switch
to a newer algorithm. Changing content produces a new version. Runs and
existing participants stay pinned to the version they started with. Closing or
pausing enrollment prevents new enrollments while allowing existing ones to finish.

## Participant behavior and recording

- An unrelated practice tree precedes enrollment.
- A random first-party browser cookie identifies an anonymous enrollment. No
  participant login is required. Reloads resume the same tree, tasks and order.
- Each prompt has a separate navigation-start action; the tree resets per task.
- Choosing a location requires confirmation. Participants can give up or mark a
  task as not understood. Correctness is not revealed during the study.
- Navigation events are saved with IDs and sequence numbers. The browser retains
  an outbox until acknowledged; final submissions are retry-safe and advance only
  after the server saves them. Conflicting tabs must reload instead of merging.
  Starting a task checks the expected task ID, and finishing requires a sequenced
  submission event captured when the participant confirms their response.
- Server timestamps, client clock epochs and explicit visibility preserve
  interruption evidence. Client measurements are observational; server elapsed
  time also includes network delays. Partial visible-time observations remain
  available with quality flags, while precise navigation times and their medians
  exclude interrupted attempts.

The browser must initially load the application and contact the server to enroll
or move between tasks. Its outbox tolerates interrupted connectivity during a task.
Clearing cookies or using another browser creates a new identity; an anonymous
public link cannot prove that every enrollment belongs to a different person.

## Blinding and results

Participants receive only their assigned tree with opaque node IDs. The public
API omits variant identities, source paths, answer mappings and correctness.
Owner and analyst accounts can access blinded summaries and CSV/JSON exports.
Only the owner can edit or preview trees, manage runs, or download the separate
variant key/source bundle and detailed events. Pilot and real runs have separate
enrollment and allocation pools; real runs are the default reporting view.

Operational counts and comparative results have separate views. The blinded
export is intended for an analyst who has not authored or inspected the trees.
Authors necessarily know the variants; experienced participants may recognize the
current menu. This supports assignment/hypothesis blinding, rather than claiming
recognition is impossible.

### Outcomes and denominators

Every assigned task has a row, including tasks whose navigation a participant
never started. Distinguish correct, incorrect, gave up, did not understand,
started unfinished, and not started. Unassigned tasks are absent. The wire names
`shown` and `unreached` refer to whether `/start` created an attempt; the prompt
may have been read before navigation starts. They do not establish whether an
unstarted prompt was actually seen. A comprehension skip starts and immediately
finalizes an attempt without inventing a tree-view event.

- **Successful-find yield:** correct submissions divided by assigned tasks.
  Because sessions have equal length, the arm total is the mean participant
  score (correct out of six for the example).
- **Started-task success:** correct submissions divided by started attempts,
  including comprehension skips (`shown_success_rate` in the API).
- **Completion:** all assigned tasks have a terminal response, including skips.
- **Direct success:** a correct answer reached without detours/backtracking.
- **Timing:** `observed_navigation_ms` preserves the sum of measured visible
  intervals, including the final decision until confirmation. `navigation_ms`
  is present only for complete, uninterrupted selected/gave-up attempts.
  `timing_quality`, `clock_epochs` and `server_elapsed_ms` support interpretation
  of partial observations. Medians exclude partial timings and skips.
- Timing, first choices and backtracking help explain success and failure;
  successful-only timing must not be interpreted in isolation from success rates.

For comparisons, calculate uncertainty at the **participant** level, not as
though six answers from one person were six independent participants. Sampled
tasks reach half the participants per arm. Decide the stopping rule and intended
comparisons before recruitment; a conference sample describes its recruited
audience. Keep comprehension skips and interruptions visible in interpretation.

JSON downloads contain assigned-task `rows`; CSV downloads contain the same
observations. Arms and tasks use neutral codes. The owner key maps those codes
to the authored study. Codes are local to a run: always keep the run ID when
combining files, rather than treating the same code in two runs as the same arm
or task. Each row includes run/version identifiers, the frozen
version hash, pilot/real mode, and difficulty, so the analyst can distinguish
datasets and compare difficulty bands without opening the variant key.
Owner event downloads include node mappings and all
attempt lifecycles, including attempts with zero events, for path and timing
audits.

Exports contain pseudonymous session identifiers and optional familiarity
answers. Keep the database and variant key with the study operator; share blinded
exports with the analyst. State the intended use and retention in the study's
participant instructions.

## Deployment

Use a single Linux server with persistent **local disk**, with HTTPS terminated
by a reverse proxy. Set `--public-url` to the exact external origin; HTTPS enables
secure cookies and the origin is used for mutation protection. Mount the app at
the domain root. Example configuration:

- [systemd service](deploy/treetest.service): unprivileged `treetest` account,
  database in `/var/lib/treetest`, executable in `/usr/local/bin/treetest`.
- [Caddyfile](deploy/Caddyfile): replace `study.example.org` with your domain.
- [Dockerfile](Dockerfile): static Go binary and example studies, non-root runtime.

For systemd, create the service account/state directory, install the binary and
unit, bootstrap an owner using that account, and edit the domain in the unit and
Caddyfile. Keep the database outside application release directories. A reverse
proxy's access logs are separate from the pseudonymous study dataset.

Container example (initialize the bind mount for UID/GID 65532):

```sh
docker build -t docs-tree-test .
mkdir -p data
sudo chown 65532:65532 data
docker run --rm -v "$PWD/data:/data" docs-tree-test user --db /data/study.sqlite --username owner
docker run --rm -v "$PWD/data:/data" docs-tree-test import --db /data/study.sqlite --study /studies/prometheus
docker run -d --name treetest --restart unless-stopped \
  -p 127.0.0.1:8080:8080 -v "$PWD/data:/data" docs-tree-test \
  serve --db /data/study.sqlite --listen 0.0.0.0:8080 --public-url https://study.example.org
```

### Backups and restore

```sh
./bin/treetest backup --db /var/lib/treetest/treetest.sqlite --out /secure/backups/study-2026-09-22.sqlite
```

The command creates a consistent SQLite backup, including committed data that is
still in the WAL. Use a new output filename, and store a copy on another machine.
To restore, stop the service, retain the old database and its `-wal`/`-shm` files
together, place the backup at the configured database path with service-account
ownership, and restart. Do not mix an old WAL with a restored database. Rehearse
restore with a copied backup and confirm the expected study versions and counts.

## Development

```sh
CGO_ENABLED=1 go test -race ./...
go vet ./...
node --test web/tests/*.test.mjs
CGO_ENABLED=0 go build -trimpath -o bin/treetest ./cmd/treetest
```

`internal/study` implements parsing and authoring validation; `internal/server`
owns SQLite, assignment, collection, access control and reporting; `web` embeds
the participant and admin UI; `cmd/treetest` supplies operational commands.
[API.md](API.md) describes the wire protocol and package boundaries.

The race detector requires a C compiler for development; the production binary
is built with CGO disabled and needs no C toolchain.

### Real-browser smoke check

`integration/browser-smoke.mjs` uses Node 22+ and Chromium's DevTools protocol
without npm dependencies. Start the application with a disposable database,
import the Prometheus fixture, and create `owner` and `analyst` accounts with the
same test password. Run Chromium with a separate temporary profile:

```sh
chromium --headless --disable-gpu --remote-debugging-port=9222 \
  --user-data-dir=/tmp/opencode/docs-tree-test-chromium about:blank
```

In another terminal, set `TREETEST_TEST_PASSWORD` to that test password and run:

```sh
node integration/browser-smoke.mjs
```

The check creates pilot data and a draft, exercises mobile navigation, an offline
outbox, a lost submission response and reload, web publication, and analyst
permissions. Screenshots go to `/tmp/opencode/docs-tree-test-browser`. Optional
environment variables are `TREETEST_TEST_URL`, `CHROME_DEBUG_URL`, and
`TEST_ARTIFACTS`.

License: [AGPL-3.0-only](LICENSE). The Prometheus navigation fixtures include
[Apache-2.0](LICENSES/Apache-2.0.txt) material; see [NOTICE](NOTICE) for
attribution.
