# Prometheus documentation tree study — DRAFT example

This is an initial, reviewable study fixture, not a published study. Review the
task wording, accepted destinations, difficulty assignments, and pilot results
before using it for real enrollment. The participant-facing title is **Finding
information in Prometheus documentation**. Arm names, source paths, IDs, and the
answer rationales below are author-only material.

## Files and study design

- `study.json` is a schema-version-1 **Config**, not a `{config, trees}` Bundle.
- `current.md` and `candidate.md` are its two sibling tree files. Each line is
  `- [label](group:occurrence-id)` or
  `- [label](page:occurrence-id/content-id)`, indented two spaces per level.
- `extract.py` reproducibly reconstructs and checks both menus using Python's
  standard library and read-only Git commands.
- `fixtures_test.go` validates the shipped files with the application's Go
  `study.LoadDir` and `study.Validate`, and checks within-difficulty pair balance.

There are four easy, four medium, and four hard tasks. Each session has six
tasks: two of each difficulty. The six panels expose every task three times and
every unordered pair within a difficulty exactly once. Cross-difficulty
combinations and listed starting difficulties vary. Panel membership does not
require the application to use this listed presentation order. Difficulty is an
initial judgment, not an empirical finding or a statistical-power claim.

For auditing panel membership, number the tasks within each band in their
`study.json` order:

| Panel | Easy pair | Medium pair | Hard pair |
| --- | --- | --- | --- |
| 1 | 1, 2 | 1, 3 | 2, 4 |
| 2 | 1, 3 | 2, 4 | 1, 2 |
| 3 | 1, 4 | 1, 2 | 3, 4 |
| 4 | 2, 3 | 3, 4 | 1, 4 |
| 5 | 2, 4 | 1, 4 | 2, 3 |
| 6 | 3, 4 | 2, 3 | 1, 3 |

## Pinned sources

Extraction and content review date: **2026-09-22**.

| Input | Revision |
| --- | --- |
| `prometheus/docs`, current `main` | `1fed0cfef668ede0f52461305d4a52e44f199562` |
| `prometheus/docs`, `feature/proposed-docs-structure` | `029b80a42ad22868dbfed338019cafd270fbfbda` |
| Shared docs merge base | `41fe86947454e377971c3cbd180951fe7ecdc349` |
| `prometheus/prometheus`, cached 3.14 release docs | `d7598b7141418fa35be2b5ec5d0fefb634199610` |
| `prometheus/alertmanager`, cached 0.34 release docs | `085f0ef7eb41da24cab8cd000f1345b6250f2edb` |

The local read-only source repository was
`/home/jfajersk/code/github.com/prometheus/docs`. The two upstream caches are
`generated/repo-docs/prometheus/prometheus/3.14` and
`generated/repo-docs/prometheus/alertmanager/0.34` beneath it. Their HEADs were
verified against the hashes above, and both cached worktrees had clean status.
The utility reads pinned Git blobs rather than trusting mutable working files.

`main` contains 25 commits absent from the proposal branch. Shared local pages
have the same navigation frontmatter at these two revisions; their content-only
differences are the Node.js library listing, LTS wording/status, and a Grafana
dashboard URL. These do not change the reviewed task answers. The candidate also
adds `about-the-project/index.md` and `querying/index.md`. Both arms use the same
pinned external release docs to isolate the menu comparison from version drift.
The cached release versions are a deliberate reproducible snapshot, not a claim
about whichever releases are latest when somebody runs this study later.

Sources can be inspected without checking out either branch, for example:

```sh
git -C ~/code/github.com/prometheus/docs show 029b80a42ad22868dbfed338019cafd270fbfbda:docs-routes.ts
git -C ~/code/github.com/prometheus/docs show 1fed0cfef668ede0f52461305d4a52e44f199562:docs/introduction/first_steps.md
```

The user's untracked `prometheus-proposed-docs-structure.md` was not an input.
No source-repository files, branches, worktrees, or commits were changed.

## Effective navigation extraction

Both complete menus include unchanged sections and destinations, not just the
pages used as task answers:

| Tree | Top-level branches | All nodes | Selectable pages | Nonselectable groups |
| --- | ---: | ---: | ---: | ---: |
| Current | 11 | 110 | 94 | 16 |
| Candidate | 12 | 107 | 91 | 16 |

The reconstruction follows the pinned versions of:

1. **`docs-config.ts`**: repository sources in order (Prometheus, Alertmanager),
   followed by local `docs/`. The separately fetched governance file is not
   inserted into the documentation collection and is not a docs-menu node.
2. **`scripts/fetch-repo-docs.ts`**: recursively discover files in sorted directory
   entry order; ignore external root `index.md`; read `title`, `nav_title`,
   `sort_rank`, and `hide_in_nav`. Missing allowed ranks are zero. For the
   candidate, require `docs-routes.ts` entries, apply their slug/title/rank
   overrides, and omit `redirectTo` entries exactly as the branch fetcher does.
3. **`src/docs-collection.ts`**: strip index suffixes as the fetcher does, assign
   each route to its nearest existing ancestor after a stable depth sort, then
   stably sort children and roots by numeric `sortRank`. No intermediate route
   segments are invented as groups.
4. **`src/app/docs/LeftNav.tsx`**: use `navTitle ?? title`, preserve source spelling
   and capitalization, filter hidden children, and project repository documents
   to the `latest` route alias. The control's first-repository-child version
   selection and the candidate's per-repository filtering both give this menu
   when viewed from an unversioned page or the latest docs. No hidden entries are
   present in the pinned effective menus.

Product-version selectors and numbered duplicates of latest are excluded.
**Protocol** versions remain distinct pages: OpenMetrics 1.0/2.0 and Remote
Write 1.0/2.0. This is the full latest-only **documentation side navigation**;
global website links, article headings, search, and in-article links are not tree
nodes.

LeftNav renders any node with children as a non-navigating branch. Those become
`group:` nodes even if an index Markdown file contains prose. Leaf destinations
become selectable `page:` nodes. In particular, the outer Configuration and
Alertmanager branches remain groups and their identically named actual pages
remain selectable. No landing-page answers are invented for these groups.

Stable ties matter in the control:

- Instrumenting precedes Operating (both rank 5).
- Agent Mode precedes Querying (both rank 4; depth-ordered discovery).
- HTTP configuration for promtool precedes Unit testing for rules (rank 6).
- HTTP API precedes Remote Read API (rank 7).
- High Availability precedes Notification Integrations (rank 4).
- Consoles and dashboards precedes Instrumentation (rank 3).
- The nine rank-zero Guides precede the four rank-one Guides; each subset keeps
  recursive discovery order. The two rank-zero command-line pages also keep
  their original order.

Candidate labels come from effective branch metadata, including retained labels
such as **Basic auth**, **HTTP SD**, **Command Line**, **Migration**, and
**Notification Integrations**. A rewritten route slug is not a navigation label.

### Generated-cache cross-check

`generated/docs-collection.json` contains candidate routes, not a valid current
baseline. It was **never used to construct either fixture**. After source-based
extraction, all 107 candidate local/latest entries matched that file for route,
source identity, effective label, rank, visibility, and insertion/discovery
order. This proves the relevant menu projection agrees with the pinned branch;
it does not make the generated file a source of current-main content.

### Identity and redirects

Occurrence IDs (`current-001`, `candidate-001`, etc.) identify individual menu
placements and are unique across both trees. Content IDs identify source
documents and survive changes to menu route, label, or placement:

- `site-` + local path under `docs/`, without `.md`, with punctuation replaced by
  hyphens; e.g. `docs/practices/remote_write.md` becomes
  `site-practices-remote-write`.
- `prometheus-` or `alertmanager-` + the corresponding repository's path under
  `docs/` transformed the same way.

These are document identities, not byte hashes. The pinned commits identify the
exact bytes. Distinct articles covering the same topic keep distinct IDs, and a
real document placed more than once may share a content ID. Scoring accepts any
occurrence of an accepted content ID. The latest/numbered alias exclusion means
these particular trees need no repeated placements of the same document.

The candidate fetcher drops the following selectable pages because their routes
redirect; their bodies are not merged into the destination:

| Removed source | Redirect target route | Consequence |
| --- | --- | --- |
| local `tutorials/getting_started.md` | `get-started/quickstart` | Loses this tutorial's separately selectable self-scrape and Node Exporter walkthrough. |
| Prometheus `getting_started.md` | `get-started/quickstart` | Loses the upstream starter's separately selectable self-scrape and example host-target walkthrough. |
| local `tutorials/instrumenting_http_server_in_go.md` | `guides/go-application` | Loses the separately selectable HTTP-server instrumentation example. |

There are also redirects for the local Tutorials index and the upstream Querying
index; they are groups, not additional accepted pages. Older Alertmanager source
names such as `clients.md` have candidate routes, but a route does not create a
node when that source file is absent from pinned 0.34 docs.

The answer keys therefore differ by arm where appropriate. First steps is not
credited with host-monitoring instructions merely because two old starters
redirect to it. The proposal tests both organization and the actual removal of
these duplicate-topic destinations; it is not a pure relabeling experiment.

## Task-to-source and answer audit

The prompts describe information needs without supplying a menu path or the
technical name of the intended answer. Product/language names necessary to the
situation (Prometheus, Go, Python, Grafana, Alertmanager) remain. Unchanged
destinations are represented too, notably Concepts, Visualization → Grafana,
Alertmanager → Configuration, and the specification pages.

Acceptance is based on the article's substantive help for the **full prompt**,
not just a matching title. For a how-to, a mere internal link to another article
does not normally suffice. The Python task explicitly asks for a package, so a
direct link to the matching external package/documentation does suffice. These
are deliberately inclusive initial keys for author review; multiple genuine
destinations are not marked wrong just because one was the expected route.

In the lists below, local paths are in `prometheus/docs` at both pinned docs
revisions, Prometheus paths are at the pinned 3.14 commit, and Alertmanager paths
are at the pinned 0.34 commit. A listed ID is accepted in **both** arms unless
marked **current only**.

### `easy-first-run`

- `site-introduction-first-steps` — local `introduction/first_steps.md`,
  “Downloading Prometheus”, “Configuring Prometheus”, and “Starting Prometheus”:
  binary download/run and an explicit `localhost:9090` self-scrape job.
- `prometheus-getting-started` — Prometheus `getting_started.md`, “Configuring
  Prometheus to monitor itself” and “Starting Prometheus”. **Current only.**
- `site-tutorials-getting-started` — local `tutorials/getting_started.md`, “Show me
  how it is done”: download, self-scrape YAML, and startup command. **Current only.**

The installation article offers binaries/Docker and a sample configuration but
does not explain the requested self-collection settings. A guide about installing
an exporter without configuring self-scraping does not satisfy this prompt.

### `easy-variable-value`

Each accepted article directly explains that a gauge can rise and fall:

- `site-concepts-metric-types` — local `concepts/metric_types.md`, “Gauge”, with
  concurrent-request examples.
- `site-tutorials-understanding-metric-types` — local
  `tutorials/understanding_metric_types.md`, “Gauge”.
- `site-practices-instrumentation` — local `practices/instrumentation.md`,
  “Counter vs. gauge, summary vs. histogram”, including in-progress requests.
- `site-instrumenting-writing-clientlibs` — local
  `instrumenting/writing_clientlibs.md`, “Gauge”, including increment/decrement.
- `site-instrumenting-writing-exporters` — local
  `instrumenting/writing_exporters.md`, “Types”: a decrementable counter is a
  gauge, with additional cautions about translating existing exporter types.
- `site-specs-om-open-metrics-spec` — local `specs/om/open_metrics_spec.md`,
  “Metric Types / Gauge”: current values may increase or decrease.
- `site-specs-om-open-metrics-spec-2-0` — local
  `specs/om/open_metrics_spec_2_0.md`, “MetricFamily Types / Gauge”, same property.

Formal specification destinations are accepted despite being unlikely easy-task
choices. A page merely displaying `# TYPE ... gauge` is insufficient.

### `easy-python-package`

- `site-instrumenting-clientlibs` — local `instrumenting/clientlibs.md`, the
  language list directly links `prometheus/client_python`.
- `site-concepts-metric-types` — local `concepts/metric_types.md`, multiple
  “Instrumentation library usage documentation” lists directly link the Python
  package documentation for counters, gauges, histograms, and summaries.
- `site-instrumenting-writing-exporters` — local
  `instrumenting/writing_exporters.md`, “Collectors”, directly identifies and
  links the Python client's custom-collector documentation.
- `site-instrumenting-pushing` — local `instrumenting/pushing.md`, “For use from
  Python”, directly links that same Python package's Pushgateway documentation.

The latter two are specialist routes but genuinely identify the requested
package. This broad discovery task does not require a particular export mode or
claim that the Pushgateway is the preferred architecture for a long-lived service.

### `easy-host-measurements`

- `site-guides-node-exporter` — local `guides/node-exporter.md`: download/start
  the exporter, scrape it, and inspect CPU/filesystem/network measurements.
- `site-guides-file-sd` — local `guides/file-sd.md`: names the exporter, configures
  a `node` job and target file, verifies collection, and explicitly starts an
  exporter in “Changing the targets list dynamically”. Its initial installation
  subsection links the dedicated guide, but this prompt only requires startup
  and collection instructions; both are present in the article itself.
- `site-tutorials-getting-started` — local `tutorials/getting_started.md`, the
  machine-metrics exporter startup and scrape configuration. **Current only.**
- `prometheus-getting-started` — Prometheus `getting_started.md`, “Starting up
  some sample targets” and “Configure Prometheus to monitor the sample targets”,
  including Node Exporter startup, jobs, and CPU data. **Current only.**

The exporter catalogue/FAQ identify a tool without providing the requested
startup and collection instructions. First steps only links to the dedicated
guide. The Docker Swarm guide requires a different deployment context and its
worked collector is for containers, not this machine-monitoring setup.

### `medium-go-example`

- `site-guides-go-application` — local `guides/go-application.md`, “Adding your own
  metrics”: custom counter creation/update, `promhttp`, and scrape configuration.
- `site-tutorials-instrumenting-http-server-in-go` — local
  `tutorials/instrumenting_http_server_in_go.md`: increments a custom counter on
  `/ping`, exposes `/metrics`, and supplies the scrape job. **Current only.**

Both are worked, sufficient examples even though their scenarios differ. The
prompt deliberately does not require counting HTTP requests, which would make
the surviving Go application's simulated-work example an unfair answer.

### `medium-protect-ui`

- `site-guides-basic-auth` — local `guides/basic-auth.md`: password hashing,
  `basic_auth_users`, `--web.config.file`, and a test request.
- `prometheus-configuration-https` — Prometheus `configuration/https.md`: the
  same startup flag and web-config schema, explicitly including bcrypt passwords
  and usernames for access to the web server.

The general Prometheus scrape configuration's `basic_auth` describes outgoing
requests, not authentication of users coming to the UI. TLS-only instructions,
Alertmanager's similar web settings, and the security overview do not supply the
complete requested Prometheus setup.

### `medium-time-range-request`

- `prometheus-querying-api` — Prometheus `querying/api.md`, “Range queries”:
  `/api/v1/query_range`, `query`, `start`, `end`, `step`, and a worked HTTP request.

PromQL basics explain query evaluation but not the requested HTTP request
contract. The command-line `promtool query range` interface is not that endpoint.

### `medium-connect-grafana`

- `site-visualization-grafana` — local `visualization/grafana.md`, “Creating a
  Prometheus data source”: select Prometheus, set the server URL, Save & Test.
- `site-tutorials-visualizing-metrics-using-grafana` — local
  `tutorials/visualizing_metrics_using_grafana.md`, “Adding Prometheus as a Data
  Source in Grafana”: equivalent connection instructions before dashboard steps.

These are separate articles in both arms, even after the latter moves to Guides.

### `hard-rule-behavior`

- `prometheus-configuration-unit-testing-rules` — Prometheus
  `configuration/unit_testing_rules.md`: `promtool test rules`, `input_series`,
  `eval_time`, `exp_alerts`, `promql_expr_test`, and complete examples.

Syntax checks and command-line help alone do not show how to describe synthetic
inputs and expected outcomes. Live alerting walkthroughs do not perform this check.

### `hard-delivery-resources`

- `site-practices-remote-write` — local `practices/remote_write.md`, “Resource
  usage” and “Parameters”: queue/shard behavior, memory proportional to shard
  count times `(capacity + max_samples_per_send)`, backlogs, and tuning trade-offs.

Prometheus `configuration/configuration.md` documents `queue_config` fields and
buffering for throughput, but omits the requested analysis of memory effects.
Storage and protocol specifications likewise do not substitute for this guidance.

### `hard-team-notifications`

- `alertmanager-configuration` — Alertmanager `configuration.md`, `<route>` and
  its example: `matchers`, per-team receivers, `group_by`, inherited settings,
  and notification timing. The example includes `team="frontend"`.

Alertmanager's overview page explains grouping conceptually but delegates setup
to the configuration article. The basic alerting tutorial sends everything to a
single webhook and does not implement team selection plus grouping.

### `hard-service-percentile`

- `site-practices-histograms` — local `practices/histograms.md`, “Quantiles”:
  explicitly rejects averaging per-replica quantiles and shows the combined
  0.95-over-5m expression for classic and native histograms.
- `prometheus-querying-functions` — Prometheus `querying/functions.md`,
  `histogram_quantile()`: quantile/time-window arguments, rate before sum, and
  aggregate-all/by-job examples for classic and native histograms.
- `site-specs-native-histograms` — local `specs/native_histograms.md`, the
  `histogram_quantile()` discussion in PromQL functions: classic/native examples
  combine `rate`, `sum by (job[, le])`, and quantiles. Its percentile and window
  can be adapted to the task just as in the function reference.

The introductory metric-type articles and OpenMetrics specifications describe
histograms or non-aggregatable summaries, but do not show the requested
cross-replica calculation. The task asks for an appropriate calculation, not an
exact example metric name or a literal `0.95` code search.

## Reproduce and validate

From this project's root, with the pinned source objects and cached worktrees
available:

```sh
python studies/prometheus/extract.py --cross-check-cache
go test ./studies/prometheus
```

Without the optional generated collection, omit `--cross-check-cache`. Use
`--docs-repo /path/to/prometheus/docs` if the read-only source clone is elsewhere.
The checks fail if the upstream cache HEADs differ from the pins. They do not
fetch, reset, clean, switch branches, or run the website's fetching scripts.

For a detailed audit of every node (original source, route, rank, label,
occurrence ID, content identity, and selectability):

```sh
python studies/prometheus/extract.py --inventory current
python studies/prometheus/extract.py --inventory candidate
```

To preview regenerated tree text use `--tree current` or `--tree candidate`.
To materialize it, the exact command is:

```sh
python studies/prometheus/extract.py --write
```

That command writes only `current.md` and `candidate.md` next to the utility,
then checks the config. Prompts and answer keys remain manually reviewed author
data. The utility's scalar-frontmatter and route readers intentionally support
the syntax at these pinned revisions rather than being general YAML/TypeScript
parsers. Changes to source pins or navigation algorithms need a new extraction
review. Source blobs can be read offline; missing Git objects fail instead of
being lazily fetched into the read-only clone.
