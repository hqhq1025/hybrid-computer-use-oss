---
name: computer-use-desktop-ax-only
description: Complete desktop tasks with Bash, files, browser automation, application APIs, and the Computer Use AX tool when GUI is unavailable.
---

# AX-Only Desktop Computer Use

Use the available hybrid environment with exactly one Computer Use MCP tool:
`AX`. Bash, file editing, CLI tools, browser automation, and documented
application APIs remain valid general-purpose channels. No full-desktop `GUI`
tool is available; do not search for it, request it, or describe it as a
recovery option.

## Product Boundary

- Complete outcome-only instructions through any available general-purpose
  channel.
- Method matters when it is explicitly exclusive, is itself the durable user
  purpose, changes the delivered observable state, protects safety or honesty,
  or preserves unrelated user data. Ordinary wording such as "using Chrome",
  "use GIMP", "from the command line", or "search the Internet" does not by
  itself prohibit an equivalent general-purpose route. Visible terminal output
  must actually remain visible in a terminal.
- Do not inspect benchmark task definitions, task IDs, dataset configuration,
  evaluator code or labels, gold artifacts, hidden checks, or cross-task
  solution memory.
- Do not fake terminal output, files, screenshots, application state,
  verification evidence, or completion.
- A missing visual MCP channel is a capability boundary, not evidence that an
  application, control, or requested state is absent.

## Choose The Available Channel

- Prefer Bash, structured file editing, CLI tools, or documented application
  APIs for durable state when the requested method does not require visible UI
  interaction.
- Use the built-in browser runtime when it is available and can directly
  inspect or manipulate the relevant web content, navigation, download, or
  browser state. It does not provide the missing full-desktop GUI channel.
- Use structured parsers for archives, JSON, XML, databases, and compound
  documents. Inspect file magic and metadata before selecting a parser.
- Use `AX` for semantic identity, names, values, states, focus, labelled
  controls, application windows, and keyboard interaction.
- A documented local automation or debugging API exposed by the running
  application is an application API. Discover it only from live process,
  configuration, help, or protocol information in the task environment. Use
  one bounded capability probe and inspect only relevant commands.
- Prefer a channel already producing reliable evidence. Stop once every
  requirement has one authoritative final-state check.
- If a direct edit changes state already open in an application, reload or
  reopen it when the application could overwrite the edit or the requested
  final state must remain visible.

## AX Observation

- Begin with `AX` mode `observe` for the foreground or named application.
- For `observe`, `find`, and `assert_ax`, use exactly one identity source:
  bootstrap `app`, returned `app_ref`, or returned `window_ref`.
- Once a `window_ref` is available, omit `app` and `app_ref`; the window ref
  owns the application identity.
- Use `scope:"content"` for a page, document, embedded web view, canvas, or
  terminal content plane when one content root is uniquely identifiable.
- Use `scope:"window"` for address bars, tabs, toolbars, menus, settings,
  dialogs, download UI, and other application chrome.
- AX observations use compact Playwright-style tree notation. Quoted text is
  user-visible text; roles, states, properties, and short refs are unquoted.
- Partial coverage means some branches were omitted. It does not invalidate a
  returned target.
- Use `root:{"focused":true}` for nearby focus context, or one current
  index/ref/exact selector as a read-only root for a known subtree.
- Do not tune provider text, node, depth, retry, or polling limits that are not
  in the public schema.
- Accessibility does not expose hidden DOM, prove network activity, prove file
  persistence, or establish full-desktop spatial layout.

## Find And Addressing

- Use `AX` mode `find` when the target role, name, text, value, or state is
  known.
- Use `name_equals` or `text_equals` for complete fields and
  `name_contains` or `text_contains` for substrings.
- `find.query` is a search grammar. Read-only roots and assertions may use
  exact selectors, but mutations accept refs only.
- Prefer a current ref returned by `find` over reconstructing a selector.
- Mutation targets use `rN` for elements and `wN` for windows. Never invent,
  decode, or edit refs.
- A ref owns its application and logical window. Do not repeat `app`,
  `app_ref`, or `window_ref` with a targeted ref.
- An all-keyboard batch without a targeted action requires an explicit current
  `window_ref`.

## AX Actions

- Use `AX` mode `act` for ordered semantic and keyboard actions within one
  logical window.
- Use public action names only: `activate`, `close`, `click`, `keypress`,
  `scroll`, `set_value`, and `type`.
- Use `type` for literal text and `keypress` with a `keys` array for key chords.
- Prefer semantic `click` for ordinary activation. A click on a selectable item
  may invoke its default action, so do not assume it only changes selection.
- Window `activate` and `close` require a current `wN`. Make `close` the final
  mutation because it may expose a dialog, replace the root, or exit.
- Batch deterministic actions only while later targets, focus, and window
  identity are expected to remain valid.
- Split after an action that may open, close, replace, or navigate a window.
- When a later action depends on an earlier state change, put the expected
  predicate on the earlier action as its `postcondition`. Do not add a separate
  assertion between already-batched actions.
- `AX` act defaults to one fresh compact diff after the final action. Omit
  `observe` for ordinary actions.
- Use `observe:"none"` only when no immediate readback is useful.
- Use `observe:"screenshot"` only when target-window pixels are necessary and
  the AX schema supports that evidence. It is not a full-desktop observation.
- An action success proves delivery, not the requested task outcome.

## Recovery

- If an action reports `action_outcome=not_executed` and
  `input_events_emitted=false`, it caused no later UI change.
- Continue from current refs returned in the same response when available.
  Call `observe` or `find` only when `observation_required=true`.
- On `stale_element_ref`, every target from the failed call is expired. Never
  submit the old ref unchanged.
- Prefer `find` for a known replacement control, `assert_ax` for a known
  condition, and a focused or rooted observation for local context.
- If a transient window is visible to the user but absent from AX, record that
  semantic coverage is unavailable. Use Bash, durable state, or an application
  API when those channels can satisfy the instruction. Do not repeatedly query
  the same tree or invent a semantic target.
- Two mutation attempts based on the same hypothesis without new evidence are
  enough. Reinspect durable or semantic state, form a different hypothesis,
  and change mechanism.
- Do not launch replacement application instances merely to escape uncertain
  state; parallel instances make ownership and persistence ambiguous.

## Efficient Work

- Perform the mutation once the source, destination, and operation are known.
  Additional investigation must answer a concrete blocker.
- Combine related read-only Bash checks into one bounded command or script.
- For bulk structured data, prefer a parser or documented API over a
  shell-owned clipboard.
- If clipboard transfer is necessary, keep its owner alive until paste
  completes. After one focus-verified paste failure, change mechanism.
- Background Bash output does not satisfy an instruction requiring visible
  terminal output.
- Before recursive delete, move, overwrite, ownership, or permission changes,
  resolve an absolute target and preview the affected paths.

## Verification

Use `AX` mode `assert_ax` for one bounded semantic predicate:

```json
{"mode":"assert_ax","app":"chrome","assertion":{"type":"exists","target":{"selector":{"role":"heading","name":"Results"}},"value":true,"timeout_ms":10000}}
```

```json
{"mode":"assert_ax","assertion":{"type":"state","target":{"ref":"r17"},"state":"checked","value":true,"timeout_ms":5000}}
```

- `assert_ax` proves only state visible through the accessibility provider.
  It cannot prove file contents, durable persistence, image pixels, document
  formatting, or whole-task completion.
- Prefer a structured artifact read for files, effective runtime state for
  settings and services, and AX state for semantic application behavior.
- Verify exact identity, value, type, scope, filename, destination, and
  persistence. Confirm unrelated content is preserved for narrow edits.
- Keep delivery evidence, local UI evidence, and task evidence distinct.

## Completion Gate

Before replying `TASK_COMPLETE`, verify:

- the exact target object or application;
- the exact requested value, text, formatting, or setting;
- every requested page, slide, cell, item, or scope;
- required persistence, filename, destination, and save operation;
- no unresolved confirmation state or accidental unrelated mutation.

Use one fresh requirement-matched check after the final mutation. A
postcondition or final observation from the same call is sufficient when it
directly proves the requirement.

Use `TASK_INFEASIBLE` only for a demonstrated hard blocker such as a missing
application, permission, credential, required visual capability, or
contradictory requirement. AX failure alone is not proof of infeasibility when
Bash, a durable artifact, or a documented application API can complete the
task.
