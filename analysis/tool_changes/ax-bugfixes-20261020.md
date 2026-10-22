# AX editing, final evidence, recovery, and preflight fixes

Status: `merged`
Paths refer to the layout before the package split (a4006c5).
Created: 2026-10-20
Change ID: `ax-bugfixes-20261020`
Baseline: main `9dfaf36`, package VERSION 1.0.0.
Baseline image: `osw-hybrid:useful-main-20261020-a1`.
Candidate image: `osw-hybrid:ax-bugfix-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-bugfix-main-20261020`.
Executable: `artifacts/computer-use` in this worktree.
This is an unreleased source integration, not a new formal product release.

## Problem and evidence

The user authorized parallel fixes, a merge to main, and two further panel
rounds. The preceding read-only audit found:

1. A visible toggle projected as a combobox retained the toggle's provider
   path, while the Go snapshot inferred editable capability from its role.
   A real Find-field set_value opened a popup instead of confirming the
   requested text. A no-input reproduction selected the toggle for the
   keyboard fallback rather than its editable sibling.
2. Final wait and ordered-input paths rendered explicit tree/full requests
   as compact observations. One task attempt had 12 successful explicit
   tree requests return 10 focused subtrees and two diffs. The full path
   also omitted its screenshot request.
3. Coordinate ownership errors suggested generic discovery and the guide
   suggested retrying runtime errors. Two anonymous popup changes had no
   title or window ref, yet the hint requested a wait by title. Another
   change returned to an existing main window rather than opening one.
4. The previous generic canary checked tools/list and a GUI screenshot but
   missed an AX output contract failure in a historical baseline. The main
   runner's public probe only checked the tool list using an old protocol.
5. A provider-internal screenshot-only string claimed a before/after
   comparison, although that path captures one frame. The public MCP
   success response did not display the incorrect string.
6. The combined LibreOffice smoke found another confirmed defect: targeted
   type of `targeted type 字` inserted only `targeted type`. The server
   correctly reported `effect_unknown`, but the text was incomplete.
   AT-SPI insert_text takes a character offset and a UTF-8 byte length;
   the provider incorrectly passed the Python character count for length.
   It now passes the encoded byte count, retaining character offsets.
   Regression cases cover combo, ordinary entry and focused typing with
   Chinese, emoji, accents and combining characters.
7. The same smoke found that a list-only combo retained a hidden text
   placeholder with EDITABLE and EditableText. The first implementation
   mistook that placeholder for an editor. A valid partner now also requires
   SHOWING; the same check governs capability reporting and input validation.
   Real Find editors had SHOWING, while the Line Spacing placeholder did not.
   Hidden-editor and editor-becomes-hidden tests guard this distinction.

Evidence is under
`/datadrive2/hcu-eval/useful-main-eval-20261020/next-improvements-review-20261020.md`,
with separate observations, errors, and GUI audit reports in that directory.
The 29 AX errors affected 14 of the 40 baseline attempts, all of which
eventually passed. Errors are not counted as task failures or all treated
as product bugs.

## Intended behavior and implementation scope

- Provider/ref identity: keep toggle clicks, bind editing to a unique
  editable partner in the same composite control, revalidate its identity,
  and read back from that partner. Do not synthesize text into a toggle.
- Final AX evidence: honor explicit tree/full/screenshot/diff consistently
  after mutation, wait, and ordered input. Capture after the wait; retain
  the default diff and all failure/completed-prefix evidence.
- Error delivery and guide: give executable current-state recovery calls,
  preserve input and remaining-step evidence, and do not bypass window or
  stale-ref checks.
- Runner: use the rendered client command, arguments, environment, and an
  explicit current protocol for enabled-channel read-only probes. Record
  absent-window skips, rather than presenting them as tested reads. The
  independent panel canary must complete AX reads on a known open window.
- GUI: correct only the internal screenshot-only explanatory string.

Changed files, relative to the source root:

- Combo identity and routing: `computer-use/app_snapshot.go`,
  `ax_action_service.go`, `ax_ref_registry.go`, `ax_snapshot_v1.go`,
  `provider_request_builder.go`, `provider_types_v2.go`,
  `provider_wire_v2.go`, `runtime_protocol.go`; provider files
  `native/linux/provider/ax_actions.py`, `ax_dispatch.py`, `ax_records.py`,
  `element_resolver.py`, and `wire_v2.py` under `computer-use/`.
- Final evidence: `computer-use/ax_action_result.go`, `ax_batch.go`,
  `ax_batch_observation.go`, and `ax_batch_ordered_input.go`.
- Recovery and screenshot text: `computer-use/ax_evidence.go`, `ax_wait.go`,
  and `native/linux/provider/gui_dispatch.py` under `computer-use/`.
- Runner: `hcu/osworld/runtime.py`, `hcu/osworld/run_task.py`, `pyproject.toml`.
- Contract and guidance: `computer-use/tool_contract.go`,
  `computer-use/testdata/contract/public_contract.json`, `instructions/hybrid.md`,
  `docs/MCP.md`, `docs/OSWORLD.md`, `CHANGELOG.md`, and this record.
- Regression tests under `computer-use/`: `ax_combo_edit_test.go`,
  `ax_explicit_final_observation_test.go`, `ax_recovery_guidance_test.go`,
  `ax_batch_observation_test.go`, `runtime_test.py`,
  `runtime_tests/test_combo_edit.py`, `runtime_tests/test_gui.py`,
  `testdata/provider-v2/combo-edit.request.json`, and the corpus `manifest.json`.
  Runner regressions are in `tests/test_public_mcp_probe.py`.

## Generality and model-contract review

No application names, benchmark task IDs, coordinates, or task recipes are
added to the runtime or model instructions. Composite-control ownership is
derived from provider identity, visibility and capabilities. Public tools remain AX
and GUI. The edit-target binding is private to the Go/provider protocol.

The AX observe parameter description and docs/MCP.md explain explicit
tree/full results. The hybrid guide changes only its recovery section;
channel-selection and batching strategy remain the baseline's. Error text
uses actual current refs or a GUI observation. The ordinary server
instructions retain the same tool surface. Provider-only screenshot text
does not add public facts or change image capture.

The public contract fixture and any affected private provider fixtures must
match the intended change. Developer/operator docs distinguish environment
setup canaries from actual tool-response checks.

## Safety, cost, and compatibility

Preserve canonical ref/window ownership, whole-batch argument validation,
fresh input-target checks, partial-input evidence, and final screenshots.
Newly recognized edit-partner replacement invalidates the old editing
target; it does not silently redirect a ref to another field.

Explicit tree/full can return more text than the previous incorrect compact
result. This is an evidence-contract fix, not a promised token reduction.
Probe calls add bounded setup work without model usage. No performance or
success-rate gain is claimed before the new pair seals.

The safe save/close/file-edit/reopen routing proposal is not included: it is
a separate strategy experiment. GTK nested-cell selection remains a
reproduction candidate, not a verified implementation fix. No new batch
limits or cross-window permissions are introduced.

## Validation and evaluation

Integrated checks passed on 2026-10-20:

- `make test`: Go format, vet and tests; 607 provider tests; 360 runner tests
  passed, 28 skipped. The test command uses the existing Python test venv,
  the OSWorld overlay, and `/usr/lib/python3/dist-packages` for GI.
  The first invocation lacked GI and stopped before provider tests; the
  corrected environment passed without modifying tests to hide that failure.
- `make test-transports` and Darwin/Windows cross-platform `go vet` passed.
- Both example queue configs passed dry runs with the existing OSWorld-V2
  virtual environment at `/datadrive2/repos/osw-hybrid/OSWorld-V2/.venv`.
- The intended public contract description and private provider fixture passed.
  `git diff --check` passed. The document checker findings in preexisting text
  do not affect this change; new wording was reviewed.

Real desktop smoke passed in fresh isolated containers:

- GTK: explicit tree/full after waits and ordered input returned the complete
  requested tree; full included a screenshot and default diff stayed compact.
  An anonymous popup stopped a two-step batch after step 1. The remaining
  text was absent, and the suggested GUI screenshot call succeeded.
- LibreOffice: Find/Replace set_value and targeted Unicode type passed
  independent AT-SPI readback. Clicking the Find toggle opened its history.
  Line Spacing no longer advertised editable capability; both set_value and
  type rejected before input and left independent text/value snapshots intact.
  Clicking that same control still opened its options, including Double.
- Evidence: `/datadrive2/hcu-eval/bugfix-smoke-20261020/gtk-candidate-a1/`
  and `lo-final-a1/` in that same directory. Earlier failed diagnostic roots
  preserve the two additional defects and fixture setup corrections.

The authorized comparison completed against main `9dfaf36`. The implementation
commit is `f847d1a`; both run manifests bind that source and the candidate image.
The fixed 20-task panel used GPT-5.4 xhigh, 100 successful responses per task,
a 5,400-second timeout, official/original evaluator, no Judge, and overlapping
rounds of 20 tasks, total concurrency 40. Both rounds passed setup/GUI,
actual Codex child AX, and live budget checks before model execution.

Run IDs are `hcu-paper-ax-bugfix-main-panel20-20261020T1002Z-r1` and `-r2`.
Their roots are under `/datadrive2/results/`. Both sealed successfully with
RAW 19/20 and 20/20, 39/40 normal full scores, zero budget truncations and zero
task-level errors. All owned task containers and proxies were cleaned up.

Compared with the baseline's 40 attempts, mean responses rose 9.8%, input
tokens 38.9%, estimated cost 36.1%, and Task Agent time 2.4%. The paired
median cost on 19 common fully solved tasks rose 4.0%. AX errors were
34/710 versus 29/639. This bundle did not demonstrate an efficiency or
overall error-rate gain.

All 176 successful explicit act tree requests returned the actual tree,
without compact substitution or unchanged-tree notices. The baseline had
29 successful explicit tree/full calls return focused/diff. No act full
request occurred in the new panel; its screenshot fix is validated by
the desktop smoke. The positive composite Find/Replace editing path also
has desktop evidence, rather than demonstrated use in this panel.

The sole RAW0 was a434992a in round 1. The final file had correct text,
size and colors, but two identical-format text runs instead of the
evaluator's expected one. Preserve the official score; do not relabel it
Judge PASS. The earlier same-task baseline failure was a different color
mismatch. The audit found no evidence to attribute this result to the
new editing-target or explicit-observation mechanisms.

The report and exact metric definitions are in
`/datadrive2/hcu-eval/bugfix-eval-20261020/final-report.md`, alongside the
call audit, failure audit, full-attempt statistics and execution report.

Disposition: keep the validated fixes on main under the user's authorization.
Status remains testing in the release namespace; no formal product version
is assigned. A separate follow-up should examine unnecessary full-tree requests
and selection-recovery long tails. The image was rebuilt during prefreeze
smoke fixes; no model run used those intermediate builds. After freezing
`f847d1a`, source behavior, image and run configuration remained unchanged.
