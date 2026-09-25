---
name: hybrid-computer-use
description: Complete computer tasks with Bash, files, browser automation, application APIs, and the Computer Use AX and GUI tools, then verify each requested outcome.
---

# Hybrid Desktop Computer Use

Use the full hybrid environment. Bash, file editing, CLI tools, browser
automation, application APIs, accessibility, and GUI are complementary control
surfaces. Choose the most reliable and efficient combination that satisfies the
original instruction.

## Product Boundary

- Outcome-only instructions may be completed through any general-purpose
  channel, including direct code, document, database, preference, or
  configuration edits.
- Method matters when it is explicitly exclusive, is itself the durable user
  purpose, changes the delivered observable state, protects safety or honesty,
  or preserves unrelated user data. Ordinary wording such as "using Chrome",
  "use GIMP", "from the command line", or "search the Internet" does not by
  itself prohibit an equivalent general-purpose route. "Show the result in the
  terminal" still requires visible terminal output, and "leave the page open"
  still requires the requested browser state.
- It is fine to combine channels: use Bash to compute or edit, MCP to manipulate
  visible state, and either channel to verify.
- Do not inspect benchmark task definitions, task IDs, dataset configuration,
  evaluation examples, evaluator code or labels, gold artifacts, hidden
  checks, or cross-task solution memory. This prohibition applies even when
  those files or URLs are public or reachable through Bash. Work only from the
  user instruction and the observable task environment.
- Do not fake terminal output, files, screenshots, application state, or
  verification evidence.

## Choose The Track

Choose among Bash, browser automation, accessibility, and GUI according to the
required state.

- Use Bash, file editing, CLI tools, or application APIs for efficient durable
  state changes when the instruction does not require a specific visible
  workflow.
- Use the built-in browser runtime when it is available and can directly
  inspect or manipulate the relevant web content, navigation, download, or
  browser state. It is independent from AX and GUI; verify visible desktop state
  separately when visibility is part of the requested outcome.
- A documented local automation or debugging API already exposed by the running
  application is an application API, not a separate track. Discover it only
  from live process, configuration, help, or protocol information in the task
  environment. Use one bounded capability probe, then either use the API and
  verify the requested result or abandon that path.
  A liveness, version, or page-list response proves only that the interface is
  reachable. When the interface publishes a command or schema inventory, read
  that inventory once and look only for capabilities relevant to the requested
  outcome before falling back.
  A documented local-only IPC endpoint may be started when it is scoped to the
  current user and session, exposes no network listener, and cannot overwrite
  unsaved state in another instance. This is local application automation, not
  remote access. Do not assume an endpoint or method, enable network-exposed
  remote access, bypass authentication, or enumerate unrelated API surfaces.
- Prefer a channel that is already producing reliable evidence over opening
  several equivalent investigation paths. Once the exact requested state is
  verified by an authoritative final-state read, stop. Do not repeat the same
  fact through AX, GUI and Bash unless the evidence conflicts or the instruction
  separately requires semantic and visual state.
- Use structured parsers for archives, JSON, XML, databases, and compound
  documents. Inspect file magic and metadata before selecting a loader or
  converter; a filename extension and visible name do not prove the format.
- Use accessibility for semantic identity, names, values, states, focus, and
  labelled controls.
- AX observations use compact Playwright-style tree notation. Each deeper
  semantic level adds one leading space; quoted text is user-visible AX text,
  while roles, states, properties, and short refs remain unquoted.
- Treat partial AX coverage as a statement about omitted branches, not as
  failure of every returned target. A found ref and already-emitted input
  remain usable unless the tool reports that target stale, ambiguous, or
  unresolved. Do not change scope or repeat observe/find merely to eliminate a
  coverage warning. Use GUI after one targeted semantic lookup fails, or when
  the required evidence is inherently visual.
- Within accessibility, use `scope:"content"` when the next decision concerns
  one page, document, embedded web view, canvas, or terminal content plane. It
  omits surrounding application chrome and fails closed unless one content root
  is uniquely identifiable.
- Keep `scope:"window"` for address bars, tabs, toolbars, menus, settings,
  dialogs, download UI, and other application chrome. Content scope is still an
  accessibility projection; it does not expose hidden DOM, prove the current
  URL, verify network requests, or replace visual evidence.
- Use GUI for full-desktop pixels, canvases, spatial layout, dragging,
  cross-window interaction, and content absent from accessibility.
- A visible transient window may be absent from the application's accessibility
  provider. Treat that as unavailable semantic evidence, not proof that the
  window is absent. Use one fresh full-desktop GUI screenshot instead of
  repeatedly requesting the same accessibility tree. Do not return to AX unless
  that screenshot shows a new AX-visible state or the next action requires a
  semantic target.
- If a direct edit changes state already open in an application, reload,
  reopen, or otherwise synchronize the visible application before final
  verification when the instruction requires that state to remain visible.
- Never pass target-window accessibility coordinates to GUI. GUI coordinates
  come only from its full-desktop screenshot.
- Failure of one track does not prove the application or content is absent.
  Inspect another available channel before declaring infeasibility.

Respect state boundaries reported by the MCP. If GUI reports input emission,
possible partial input, or desktop/window mutation, refresh accessibility state
before the next accessibility action. A rejected GUI batch that emitted nothing
does not itself invalidate accessibility state.

If an accessibility action reports `action_outcome=not_executed` and
`input_events_emitted=false`, it did not cause any later UI change. Do not
describe a later disappearance, dialog transition, or completed state as
possible success of that failed action. Inspect `current_ui_returned`,
`final_observation_returned`, and `observation_required`. Continue from current
refs returned in the same response when available. Call `observe` or `find`
only when `observation_required=true`; for transient dialogs, prefer `find` to
obtain a fresh `rN` ref instead of guessing a numeric index or rebuilding a
selector.

An explicit stop response takes precedence over ordinary refresh and retry
guidance. Stop desktop automation and report the uncertainty; do not replay
input or issue recovery observations. Known zero requested input does not
prove that an earlier focus or activation request cannot still take effect.

If the failure is `stale_element_ref`, treat every target from the failed call
as expired. Never resubmit the old ref unchanged. If the same response includes
a current observation, continue from its refs. Otherwise start recovery with
one fresh `AX` `observe` or `find`. If that read shows the
expected next or main state and the old target is absent, continue by verifying
the requested result instead of reopening or dismissing a dialog merely for
cleanup. If a replacement dialog is present, use only the newly returned `rN`
from the fresh read. Do not alternate the same observe-and-act retry indefinitely:
after a repeated equivalent replacement, inspect durable state or switch to
another general-purpose evidence or control channel.

## Efficient Interaction Loop

Use the smallest observation that answers the next decision.
Once the source, destination and operation are known, perform the mutation
before optional diagnostics. Further investigation must answer a concrete
blocker or a still-unproven requirement; do not postpone a standard operation
to compare equivalent tools, inspect unrelated internals, or plan cleanup.

1. Inspect the foreground or named application once with `AX` mode
   `observe`. Request `scope:"content"` for page/document work when one content
   plane is expected; otherwise keep the default `scope:"window"`.
   For `observe`, `find`, and `assert_ax`, use exactly one identity source:
   bootstrap `app`, returned `app_ref`, or returned `window_ref`. Once a
   `window_ref` is available, omit `app` and `app_ref`; the window ref already
   owns the application identity.
   Normal AX acquisition budgets are server-owned. Do not try to tune raw text,
   node, or depth limits. When one branch is already known, request
   `root:{"focused":true}` for nearby focus context or use one read-only
   index/ref/selector target as `root` for that exact canonical subtree.
2. Use `AX` mode `find` when the target role, name, state, or value is already
   known. Use the same scope as the surface that should contain the target.
   Use `name_equals` or `text_equals` for a complete field and
   `name_contains` or `text_contains` for a substring. `find.query` is a search
   grammar; read-only roots and assertions may use exact selectors, but actions
   accept refs only. Prefer a ref returned by find instead of rebuilding a
   selector.
   The default working tree may omit neutral layout/text-contribution lines or
   summarize repeated items, but mode `find` still searches those canonical
   records. Projection summaries are context, not targets. Provider facts are
   internal; use `find` or a rooted observation to resolve ambiguity.
3. Prefer semantic actions when the tree identifies the control; use GUI only
   when the required fact or action is genuinely spatial or visual.
4. Use `AX` mode `act` when consecutive actions in one application are
   already determined from the current semantic state. Element mutations accept
   only current `rN` refs and window mutations accept only current `wN` refs.
   Obtain them from `observe` or `find`; never submit an index or selector to
   `act`, and never invent placeholder refs such as `r??`. Missing, stale or
   cross-window refs fail before input. A targeted `rN` or `wN` already owns
   its application and window: omit `app`, `app_ref`, and `window_ref`.
   `window_ref` is only for a batch made entirely of untargeted keyboard
   actions.
   AX and GUI both use `type` and `keypress`, such as
   `{"type":"keypress","keys":["CTRL","S"]}`. AX may attach a target and verify
   focus before input; GUI always uses current desktop focus. Use AX `click`
   for one ordinary semantic activation. Provider-specific action names and
   click-routing options are not public.
   A semantic `click` may invoke the control's default activation. On list,
   table, file-picker, or other selectable items, do not assume it is a
   selection-only primitive when activation would have a different effect.
   An action postcondition can stop dependent later input, but it cannot undo
   activation already caused by the guarded click.
   Window `activate` and `close` require the short AX window ref (`wN`) returned
   by the current observation; `close` must be the final action because it may
   expose a dialog, replace the root or exit. Element refs are short `rN` refs.
   Never construct or decode refs yourself.
   Batch only actions whose earlier steps are expected to preserve the identity,
   focus owner, and availability of later targets. A deterministic key/text
   stream in one focused control, repeated actions on one stable target, and
   stable form-field updates followed by a known submit control can be valid
   batches. Every ref is re-resolved before its action, and the server stops
   before the next input when logical-window continuity is changed or unknown.
   When a later action depends on an earlier selectable or state-changing
   action, attach the expected predicate to the earlier action as its
   `postcondition`. The server polls that guard before emitting the dependent
   action and returns the sampled current state if the guard does not pass.
   Do not add a separate `assert_ax` or `observe` between those actions.
   When a submit or confirmation is likely to open, close, replace, or navigate
   a window, make it the final action in the call.
   Split after an action that may open, close, replace, or navigate a window.
   Knowing the business step is not proof that an old ref will survive the
   mutation.
   `AX` act returns one bounded observation after its last action. For ordinary
   actions, omit `observe`; the default is a fresh post-action diff. Set it only
   for the exceptional `none` or `screenshot` cases below. If the first same-window
   snapshot cannot provide a useful diff, the server may perform one bounded
   no-input refresh and return the newly available working tree. It returns
   lifecycle evidence only after an explicit `close` and complete inventory
   prove that exact window absent. Do not
   immediately call an unrooted `observe` merely to refresh that response:
   unchanged or small output is still fresh. A successful response explicitly
   reports `observation_required=false` and
   `next_identical_observation_required=false`; do not repeat the same read.
   New-window refs may be stabilized in the same response. Continue directly
   when the returned diff, working tree, focused context, or lifecycle result
   answers the next decision. If required
   context is absent, escalate narrowly:
   use `find` for a known control or value, `assert_ax` for a known condition,
   then a focused or target-rooted `observe`; use an unrooted window observation
   only when those local reads cannot answer the next decision.
   In `mode:"act"`, `observe` accepts only `"none"`, `"diff"`, or
   `"screenshot"`; `"tree"` and `"full"` belong only to read-only
   `mode:"observe"`. Do not spell out `"diff"` routinely; omission is equivalent
   and keeps the action request smaller.
   Use `observe:"screenshot"`
   only for target-window visual evidence, and use `observe:"none"` only when
   no immediate readback is useful and the next step does not depend on new UI
   state. Every ref is re-resolved immediately before its action, and the final
   observation is sampled after action delivery.
   When the expected AX-visible result is known before acting, include it as
   `act.postcondition`; the server polls it in the same call and then returns
   the final observation. This is preferred over `act` followed by a separate
   `assert_ax` call. Use a separate `observe`, `find`, or `assert_ax` only when
   the result of the action determines what must be checked next.
   Pass the postcondition assertion predicate directly, for
   example `"postcondition":{"type":"state","target":{"ref":"r17"},
   "state":"checked","value":true,"timeout_ms":3000}`. Do not wrap it in an
   additional `assertion` object.
5. Treat GUI as a batch-first computer tool. When consecutive GUI actions are
   already determined from the same screenshot and current focus, put them in
   one ordered `actions` array. Split the batch only when an intermediate state
   must be observed before choosing the next action. One GUI call returns one
   final screenshot for the complete batch, not one screenshot per primitive.
   End the current GUI batch after any action that may open, close, switch,
   resize, or reposition a top-level window or dialog, or otherwise change
   which window owns a later coordinate. Do not place a coordinate action after
   such a click, keypress, or type action in the same batch. Use the returned
   final screenshot to recompute coordinates in a new batch. Deterministic
   pointer, keyboard, and text actions within one stable window remain good
   batching candidates.
   Use only the native action set: `screenshot`, `click`, `double_click`,
   `move`, `drag`, `scroll`, `type`, `keypress`, and fixed one-second `wait`.
   Right click is `click` with `button:"right"`; scrolling uses `scroll_x` and
   `scroll_y` as Computer-style deltas; mouse actions may carry held modifier
   `keys`. `click` is already
   one complete press-and-release gesture; never add a separate release action.
   Coordinates are pixels in the exact returned full-desktop screenshot, using
   its reported `screen.width` and `screen.height`. Keep the exact pixels and
   never rescale them from a
   second thumbnail or preview, the smaller image rendered in chat, or an
   application window size.
   A `wait` action only delays execution; it does not expose menu highlighting,
   focus, pixels, or semantic state between actions. Do not insert 0.1-0.3
   second waits between already-determined `keypress` or `type` actions because
   the runtime already applies a processing barrier. Normally omit `settle_ms`;
   it defaults to 800ms and is capped at 3000ms. Override it only to adjust
   final screenshot quiescence, never to wait for a semantic condition. If the
   desktop has not become still within 3 seconds, use the returned screenshot
   as current evidence rather than extending or stacking waits. For page loads,
   application startup, dialogs, or
   other asynchronous transitions with an observable condition, split the call
   and use `AX` mode `assert_ax` or a fresh observation. Keep an explicit
   fixed wait only when no reliable predicate is available. Do not stack waits
   to gain confidence: after one wait and its final screenshot, either act on
   new evidence, use a condition, or change approach.
   `{"actions":[{"type":"screenshot"}]}` samples the desktop immediately and
   does not accept `settle_ms`; use a fixed one-second `wait` action when a
   delayed visual sample is genuinely required. A `screenshot` action must be
   the only action in its call because GUI returns no intermediate screenshots.
   GUI has no app name, AX ref, window ID, desktop-layout object, or frame ID.
   It never activates or closes a named window. The server privately checks
   that coordinate-relevant layout still matches the most recent GUI
   screenshot. A stale-layout rejection emits no stale coordinate action and
   returns a fresh screenshot; recompute coordinates from that image.
   GUI `type` and `keypress` go to whichever control currently owns desktop
   focus. They do not identify or prove a semantic target. When a visible click
   is sufficient to establish focus, keep that click and its dependent text or
   key action in the same batch.
   Background Bash and a visible terminal are different execution contexts. If
   the instruction requires a command or result to be visible in a terminal,
   run the final command there and observe its output; captured Bash stdout does
   not satisfy that visible-state requirement.
6. Use a separate AX read only when the `act` response lacks evidence required
   for a later state-dependent step. Prefer `find` for a known target,
   `assert_ax` for a known condition, and a focused or rooted `observe` for
   local context before requesting an unrooted window tree. Use `GUI` when the
   needed evidence is visual.
7. After structural changes, dialogs, window switches, or GUI input, obtain
   fresh state and use newly returned refs before mutating again. A read-only
   selector is scoped to its observed top-level window; observe the intended
   dialog or window first rather than expecting it to search sibling windows
   owned by the same application.

For Bash diagnostics, combine related read-only checks into one bounded command
or script when they answer the same decision. Prefer one structured read of
process, file, configuration, and metadata state over a sequence of separate
`ps`, `pgrep`, `find`, `stat`, and `jq` calls that each trigger another model
round. Keep mutations explicit and independently verifiable.

For bulk structured data, prefer a file parser or documented application API to
a shell-owned clipboard. Clipboard selection ownership and advertised text
targets may disappear when a helper or shell session exits. If clipboard
transfer is necessary, keep its owner alive through the paste and do not consume
or terminate it during a preliminary readback. After one focus-verified paste
failure, switch to a structured edit or application API instead of retrying
clipboard formats and manual cell entry.

Do not repeat an action merely because the tree is unchanged. Formatting,
canvas state, file persistence, and some application operations may change
without changing the tree. Switch evidence sources or assert a specific state.
When an AX mutation reports uncertain effect after emitting input, inspect the
fresh recovery observation returned in that same response when present. It is
current-state evidence, not proof that the failed action caused the state.
Issue another observation only when the response still says
`observation_required=true`.

Two mutation attempts aimed at the same control or state without new evidence
are enough to stop repeating that approach. Do not continue by nudging the
coordinate, adding Enter, double-clicking, or inserting waits while keeping the
same hypothesis. Take at most one fresh GUI screenshot and one relevant
accessibility or durable-state read, state a different hypothesis, then switch
control channel or mechanism. Keep GUI actions in original full-screen
screenshot pixels; window-local geometry, thumbnails, chat previews, and
independently captured images are diagnostic evidence, not alternate GUI
coordinate systems.

Do not launch replacement application instances, profiles, or sessions merely
to escape an uncertain dialog. First establish that the current instance is
unusable; parallel instances make focus, ownership, persistence, and final
verification ambiguous. This does not prohibit one bounded local automation or
headless process for a known durable artifact when it is isolated from unsaved
application state and exposes no network listener.

Stop once the requested logical entity or setting is changed and one
authoritative final-state check confirms it. Removing an account, bookmark,
document, calendar item, or similar entity does not authorize deleting related
history, caches, saved credentials, recent-item records, transport settings, or
other independently managed data unless the user explicitly asks for that
broader cleanup.

Before recursive delete, move, overwrite, ownership, or permission changes,
resolve an explicit absolute target, verify it is inside the instructed scope,
and preview the affected paths. Never perform a recursive mutation from an
inferred working directory.

## Verification

Use `AX` mode `assert_ax` for an independent read-only retry loop over one
semantic predicate. It proves only the requested state visible through the
accessibility provider. It cannot prove document formatting, image pixels,
file contents, durable persistence, or whole-task completion.

Canonical forms:

```json
{"mode":"assert_ax","app":"chrome","assertion":{"type":"exists","target":{"selector":{"role":"heading","name":"Results"}},"value":true,"timeout_ms":10000}}
```

```json
{"mode":"assert_ax","assertion":{"type":"state","target":{"ref":"r17"},"state":"checked","value":true,"timeout_ms":5000}}
```

```json
{"mode":"assert_ax","app":"chrome","assertion":{"type":"window_title_contains","text":"Settings","exists":true,"timeout_ms":10000}}
```

Verification is requirement-scoped, not a ritual after every tool call. One
authoritative final-state check per requirement is enough. Prefer a structured
artifact read for durable files, effective runtime state for settings and
services, and visible application state when visibility or application behavior
is the requested outcome. Do not add another evidence channel merely because it
is available.

If Bash, a file edit, or an application API changed state already open in an
application, account for caches, locks, running processes, and reload behavior.
Synchronize the existing application only when it can overwrite the change or
when the requested final state must be visible there. Do not reopen an
application solely to reconfirm a file fact already proved by an authoritative
parser.

Verify the exact target identity, value, object type, scope, filename, and
destination. For a narrow edit, confirm that unrelated content and paths were
preserved; a correct requested change does not excuse collateral mutation.

If the application is starting and has not registered on the AX bus yet, one
valid `assert_ax` call polls that temporary condition within the assertion's
`timeout_ms`.
Do not replace it with alternating GUI screenshots, `pgrep`, fixed sleeps, and
repeated AX observations. Invalid arguments and invalid locator shapes still
fail immediately.

- For assertions, use one read-only target form exposed by the schema: current
  index, `rN`, or exact selector. Do not invent free-form locator strings such
  as `role:document`; call `find` when a fresh actionable ref is needed.
- Use `scope:"content"` when asserting a page/document control or result, and
  keep `scope:"window"` for tabs, address bars, menus, settings, dialogs, and
  other application chrome.
- Use exactly one typed assertion predicate.
- Require value and state targets to resolve unambiguously.
- Prefer waiting for a positive next-state element or value over waiting for an
  old node to disappear. Single-page applications may retain stale nodes.
- To wait for an application window whose title has dynamic prefixes or
  suffixes, use assertion type `window_title_contains` instead of an exact
  frame selector.
- Treat a failed AX assertion as observed semantic evidence, not as proof that
  no input was emitted or that another evidence channel cannot see the result.

Keep three evidence layers separate:

1. **Delivery evidence:** the tool accepted or emitted an action.
2. **Local UI evidence:** a control, dialog, tree node, or pixel region changed.
3. **Task evidence:** the requested document, setting, file, or durable state is
   actually correct.

Never promote delivery or local UI evidence into task completion without an
independent check.

## Persistence And Final State

For documents and persistent settings:

- Ensure the durable file, setting, service, or application state is committed.
- Treat `default`, `preference`, and `setting` as application- or user-profile
  scope unless the instruction explicitly limits them to the current document
  or object. Changing an existing document's style is not evidence that a
  persistent application default changed.
- If using the visible application, commit Apply, OK, save, and confirmation
  dialogs.
- If using Bash or an API, account for application caches, locks, running
  processes, and reload behavior.
- If an application must be restarted to commit or expose the result, verify
  the requested state after restart.
- Re-read the relevant durable state once after the final mutation.
- Verify a persistent default from its effective configuration or with a newly
  created blank object. Do not use only the preexisting object that was edited.
- Verify the exact requested object, value, formatting, scope, filename, and
  output path.
- Do not substitute a nearby or similarly named setting.

Clean up only temporary state created by the task that can affect the result,
application lifecycle, or user data. Do not spend additional rounds diagnosing
harmless zombies, lock remnants, temporary files, or neutral UI after the
requested outcome is already authoritatively verified.

## Completion Gate

Before replying `TASK_COMPLETE`, verify every requirement from the original
instruction:

- exact target object or application;
- exact requested value, text, formatting, or setting;
- correct scope, including all requested pages, slides, cells, or items;
- required persistence, filename, destination, and save operation;
- no unresolved confirmation dialog or accidental unrelated change.

Obtain one fresh, read-only, requirement-matched check after the last relevant
mutation. A postcondition or final observation sampled after the mutation in
the same tool call satisfies this gate when it directly covers the requirement;
do not repeat it in a separate call. The final evidence must not come only from
the mutation's own success message. Use semantic evidence for identity and
values, visual evidence for layout or canvas appearance, durable artifact or
runtime evidence for files, settings and services, and multiple channels only
when the instruction requires multiple kinds of final state.

Never declare a state-changing or navigation task complete without first
observing the task environment. If the requested state already exists, verify
it directly; do not infer it from setup text or benchmark metadata.

Do not declare completion while any checklist item is supported only by an
action success message or an inference about what should have happened.

Use `TASK_INFEASIBLE` only for a demonstrated hard blocker such as a missing
application, permission, credential, capability, or contradictory requirement.
Do not declare infeasibility merely because one track, selector, or attempted
workflow failed. Inspect the actual local input and one plausible installed
tool before claiming a routine local transformation is unavailable. Do not
infer missing permission or authority from a filename, title, or media content.
