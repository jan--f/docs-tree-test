# Browser interface

The three HTML entry points and `assets/` are embedded by `web.Assets`. Browser
modules use plain JavaScript; there is no frontend build or runtime dependency.

- `participant.js`: public instructions, local-only practice, enrollment,
  navigation, task submission, and recovery.
- `admin.js`: session-aware owner/analyst workspace. Owners author JSON/Markdown
  bundles and manage runs. Both roles can view blinded outcomes and exports.
- `core.mjs`: event reconciliation, navigation replay, advisory tree parsing,
  and the exact summary-field definitions used by the UI.
- `common.js`: safe DOM construction, same-origin requests, dialogs, and live
  announcements.

## Participant recovery

Each study link has one local-storage journal (`tree-study:v1:{slug}`). Events
are written there before any request. The journal keeps unacknowledged events,
the next sequence, and in-flight start/finish commands. Finish payloads are
immutable across retries, including retries after a reload. The next task or
completion screen is revealed only after the command is acknowledged.

Start commands include the displayed public `task_id`, so a stale prompt cannot
start an unseen task. Confirmation appends a visible `submit` event with its
outcome and selected node, then freezes and persists the complete finish command
before awaiting an in-flight checkpoint. Checkpoints never contain `submit`.
Their acknowledgments can trim the live outbox but cannot change the frozen
finish payload or its confirmation timestamp.

A prompt comprehension skip preserves its confirmation clock and visibility in
the pending start command, then submits a sole first `submit` event after start
is acknowledged. It never synthesizes a `tree_shown` event.

On reload, accepted server events and pending local events are compared by
identity and sequence, then replayed to restore the current location and
selection. A new page UUID identifies a new `performance.now()` epoch. Resume
and visibility boundaries are recorded; every event carries explicit visibility,
including an initial hidden-page `tree_shown` or `resume`. Sequence conflicts pause the interface
and require an explicit reload of authoritative server progress.

## Checks

```sh
node --test web/tests/*.test.mjs
go test ./web
```

The dependency-free tests cover sequence reconciliation, navigation recovery,
lost start/finish acknowledgments, the completion acknowledgment barrier,
practice isolation, confirmation timing during in-flight checkpoints, explicit
visibility, stale task IDs, whole-bundle file import, owner optimistic saves/CSRF,
and analyst route restrictions.
The small DOM adapter runs the actual controllers but does not simulate layout.

For browser integration, check:

1. Create a draft and use **Import study files** to select `study.json` together
   with all referenced Markdown files. Confirm replacement of the complete
   bundle, validate its previews/mappings, save, publish, and create a
   pilot run. Open enrollment and follow its participant link.
2. Use the practice tree before joining. Complete selected, cannot-find, and
   comprehension-skip tasks. Check the page-with-children controls, breadcrumbs,
   and keyboard focus at a mobile-width viewport.
3. Disconnect while browsing and while submitting; reload and reconnect. Check
   the saved location and that a lost finish response advances exactly once.
4. Open the same study in a second tab and check the paused/reload conflict UI.
5. Check operational counts separately from blinded results. Confirm pilots are
   filtered out by default and download both formats.
6. Sign in as an analyst and confirm only runs/activity, results, and blinded
   exports are available; inspect network requests for owner-route isolation.

The bundle preview accepts the same `group:id` / `page:placement-id/content-id`
Markdown shape as the backend parser. Publication always relies on authoritative
backend validation. Owner key/source and detailed-event downloads are explicitly
separate from the blinded exports.
