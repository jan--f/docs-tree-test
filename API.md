# HTTP API and study protocol

One Go binary, SQLite, embedded HTML/CSS/vanilla JS. Module `github.com/jan--f/docs-tree-test`.

## Study bundles

`internal/study`: types in types.go. Public functions `Validate(Bundle) (*Snapshot,error)`, `LoadDir(string) (Bundle,error)`, `ParseTree(string) ([]Node,error)`.
`study.json` contains Config; each Variant.Tree names a sibling Markdown file. Web uploads use Bundle `{config,trees:{filename:markdown}}`. Difficulty values: `easy`, `medium`, `hard`. Answers map private variant IDs to accepted **content IDs**. Six tasks/session, balanced panels (two per difficulty), twelve seed tasks. Schema supports other balanced banks. Panels are explicit; all tasks have equal panel exposure. Freeze source bundle, parsed trees and scoring policy on publication. No runtime reads of author files.

## HTTP conventions

JSON request/response, failures `{error:string}` with non-2xx status. Only same-origin mutations; admin mutations additionally require `X-CSRF-Token`. Login uses origin protection. Never expose variant names, source filenames, content IDs, answer keys or correctness in participant responses. Cookie-derived participant ownership on every API. Secure cookies when public URL is HTTPS. HTML routes `/`, `/s/{slug}`, `/admin`, static `/assets/`.

### Participant endpoints

- `GET /api/public/{slug}` -> `{title,instructions,mode,status}`; sets anonymous cookie without allocating.
- `POST /api/public/{slug}/join` `{experience,docs_familiarity}` -> Session. Allocate once per anonymous identity/run, resume existing even if enrollment closed.
- `GET /api/public/{slug}/session` -> Session, 404 if not enrolled.
- `POST /api/public/{slug}/start` `{command_id,task_id}` -> Session. Idempotently start the expected public task; reject stale task IDs with 409.
- `POST /api/public/{slug}/events` `{attempt_id,events}` -> `{next_seq}`.
- `POST /api/public/{slug}/finish` `{attempt_id,command_id,outcome,node_id?,events}` -> Session. Outcomes `selected`, `gave_up`, `skipped`. Scoring is server-only. Append final events and finalize atomically. Repeated commands return the original response, even after advancing. Invalid/missing events prevent finalization.

Session shape:
```
{id, title, completed, task_index, total_tasks,
 task: {id,prompt},
 tree: [{id,label,selectable,children:[]}],
 attempt: null | {id,next_seq,events:[],started_at}}
```
Task/tree/attempt can be absent on completion. Public node IDs must be opaque. `task_index` is zero-based. An attempt starts on `/start`; the UI can also start then immediately finish a comprehension skip. Events have `{id,seq,type,node_id?,elapsed_ms,epoch,visible?,outcome?}`. Sequence begins at 1; types: `tree_shown`, `enter`, `back`, `root`, `select`, `visibility_hidden`, `visibility_visible`, `resume`, `submit`. `elapsed_ms` is monotonic within a page epoch; never subtract across epochs. Every initial epoch event carries `visible`; resume must not imply foreground visibility. The frontend supplies visibility on all events.

Every finish includes a new terminal `submit` event as the final event. Its outcome and selected node match the finish payload. It captures the confirmation timestamp, is accepted only by `/finish`, and participates in sequence validation so stale tabs cannot silently finalize another tab's stream. A comprehension skip can have a sole `submit` event without a fictitious `tree_shown`. Freeze the finish event and command before network retries; retries replay the same bytes. Store server receipt time. The UI writes an outbox before sending; duplicates with identical payloads are acknowledged, changed duplicates rejected. Attempt.events enables resume navigation. If another tab conflicts, ask to reload rather than merge streams.

### Admin endpoints

- `GET /admin/api/session` -> `{authenticated,username?,role?,csrf_token?}`.
- `POST /admin/api/login` `{username,password}` -> same session shape.
- `POST /admin/api/logout` -> `{ok:true}`.
- `POST /admin/api/validate` Bundle -> `{hash,content_hash,policy,node_counts:{variant_id:count},task_count,panel_count}`. Owner only. `content_hash` covers the source bundle; publication `hash` includes the frozen policy.
- `GET /admin/api/drafts` -> `{drafts:[{id,revision,title,updated_at}]}`. Owner only.
- `POST /admin/api/drafts` Bundle -> `{id,revision}`. Owner only.
- `GET /admin/api/drafts/{id}` -> `{id,revision,bundle}`. Owner only.
- `PUT /admin/api/drafts/{id}` `{revision,bundle}` -> `{id,revision}`; reject stale revisions with 409. Owner only.
- `POST /admin/api/drafts/{id}/publish` `{revision}` -> `{id,hash,content_hash,policy}` for frozen version. Owner only.
- `GET /admin/api/versions` -> `{versions:[{id,hash,content_hash,policy,title,slug,created_at}]}`. Owner only.
- `POST /admin/api/runs` `{version_id,slug,mode}` -> `{id,slug,version_id,mode,status}`. Mode `pilot` or `real`; initial status `paused`. Owner only.
- `GET /admin/api/runs` -> `{runs:[{id,slug,version_id,mode,status,created_at}]}`. Both roles. Analysts must not access version bundles, private filenames or semantic tree metadata.
- `PATCH /admin/api/runs/{id}` `{status}` (`open`,`paused`,`closed`) -> `{ok:true}`. Owner only. Existing participants can finish.
- `GET /admin/api/runs/{id}/results` -> `{health:{enrolled,completed,incomplete,last_event_at},arms:[Summary],tasks:[Summary]}`. Both roles, blinded.
- `GET /admin/api/runs/{id}/export?format=csv|json` -> blinded assigned-task rows, including unreached tasks. Both roles. Schema/denominators described in README.
- `GET /admin/api/runs/{id}/key` -> mapping + immutable source bundle, owner only.
- `GET /admin/api/runs/{id}/events` -> detailed event JSON, owner only (paths can reveal arms).

Summary fields: `code`, optional `task_id`, `participants`, `completed`, `assigned`, `shown`, `correct`, `incorrect`, `gave_up`, `skipped`, `unfinished`, `unreached`, `success_yield`, `shown_success_rate`, `median_navigation_ms`, `direct_successes`, `backtracks`, `interrupted_attempts`. The historical wire name `shown` means `/start` created an attempt; UI labels it Started. The prompt may have been read even when navigation was never started. Fractions use [0,1]. Missing timing values may be null. Navigation medians include only complete uninterrupted selected/gave-up attempts, not partial observations or skips.

Assigned-task exports include server elapsed time, observed foreground duration, clock-epoch counts and explicit timing quality. `navigation_ms` is nullable when an attempt is interrupted, unfinished, skipped or not started; `observed_navigation_ms` preserves partial observations. Owner-only events also include all attempt lifecycle records, including attempts with no events. Publication pins machine-readable scoring/allocation policy identifiers; policy is included in version identity and is checked when executing an existing run. Unsupported policies must not silently use newer behavior. Summary values must be derived consistently from assigned task rows. Uncertainty analysis uses participants, not individual attempts, as independent units.

## Backend embedding interface

`internal/server.Open(dbPath string, assets fs.FS, publicURL string) (*Server,error)` opens/migrates database; `Server` implements `http.Handler`; `Close() error`; `SetUser(username,password,role string) error`; `Import(study.Bundle) (versionID string,error)`; `Backup(path string) error`. `web.Assets` is an embed.FS containing index.html, participant.html, admin.html, assets/*. Server serves fixed HTML files, with JS reading path slugs; do not require template data. Bootstrap and import are CLI-only helper methods; web admin handles ordinary management.

Assigned-task exports retain run/version identifiers, version hash, pilot/real mode, difficulty and policy metadata on every row. Neither authored task IDs nor semantic node paths are part of the blinded export. The owner-only key resolves arm/task codes; detailed events resolve node IDs.
