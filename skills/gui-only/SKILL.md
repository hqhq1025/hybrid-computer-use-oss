---
name: computer-use-desktop-gui-only
description: Complete desktop tasks with Bash, files, browser automation, application APIs, and the Computer Use GUI tool when AX is unavailable.
---

# GUI-Only Desktop Computer Use

Use the available hybrid environment with exactly one Computer Use MCP tool:
`GUI`. Bash, file editing, CLI tools, browser automation, and documented
application APIs remain valid general-purpose channels. No `AX` accessibility
tool is available; do not search for semantic refs, selectors, trees,
assertions, application refs, or window refs.

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
- Missing accessibility semantics are a capability boundary, not evidence that
  an application, control, or requested state is absent.

## Choose The Available Channel

- Prefer Bash, structured file editing, CLI tools, or documented application
  APIs for durable state when the requested method does not require visible UI
  interaction.
- Use the built-in browser runtime when it is available and can directly
  inspect or manipulate the relevant web content, navigation, download, or
  browser state. It does not provide the missing AX semantic channel.
- Use structured parsers for archives, JSON, XML, databases, and compound
  documents. Inspect file magic and metadata before selecting a parser.
- Use `GUI` for full-desktop observation, pointer input, dragging, scrolling,
  focused text entry, key chords, canvases, spatial layout, and cross-window
  interaction.
- A documented local automation or debugging API exposed by the running
  application is an application API. Discover it only from live process,
  configuration, help, or protocol information in the task environment. Use
  one bounded capability probe and inspect only relevant commands.
- Prefer a channel already producing reliable evidence. Stop once every
  requirement has one authoritative final-state check.
- If a direct edit changes state already open in an application, reload or
  reopen it when the application could overwrite the edit or the requested
  final state must remain visible.

## GUI Observation And Coordinates

- Start with `{"actions":[{"type":"screenshot"}]}` when the current desktop
  state is unknown.
- A screenshot action is a complete observation call and must be the only
  action in that call.
- Coordinates are pixels in the exact full-desktop screenshot returned by
  `GUI`, using its reported `screen.width` and `screen.height`.
- Never rescale coordinates from a chat preview, thumbnail, application window
  size, or independently captured image.
- The server privately binds coordinate actions to the most recent GUI layout.
  A stale-layout rejection emits no stale coordinate action and returns a fresh
  screenshot. Recompute coordinates from that image.
- GUI does not identify semantic controls, applications, or windows. It has no
  app argument, ref, selector, window ID, or model-supplied frame ID.
- Infer a target only from current visible pixels. If labels are clipped,
  ambiguous, covered, or off-screen, adjust the visible state before acting.

## GUI Actions

- Treat `GUI` as batch-first. Put consecutive actions in one ordered `actions`
  array when they are determined from the same screenshot and stable focus.
- Use only `screenshot`, `click`, `double_click`, `move`, `drag`, `scroll`,
  `type`, `keypress`, and fixed one-second `wait`.
- Right click is `click` with `button:"right"`.
- Scrolling uses `scroll_x` and `scroll_y` Computer-style deltas.
- Mouse actions may include held modifier `keys`.
- `click` is one complete press-and-release gesture. Do not add a release.
- `type` and `keypress` go to the control that currently owns desktop focus.
  Establish focus visibly before sending text or keys.
- A visible focus click and its dependent text or key action may share one
  batch when the click cannot change the top-level layout.
- Split after an action that may open, close, switch, resize, reposition, or
  navigate a top-level window or dialog.
- Do not put a coordinate action after such a transition in the same batch.
  Use the returned screenshot and recompute coordinates.
- One GUI call returns one final screenshot for the complete batch, not one
  screenshot per primitive.
- An emitted action proves delivery, not that it reached the intended target or
  completed the task.

## Waiting And Recovery

- Ordinary consecutive keyboard actions already receive a processing barrier.
  Do not insert short waits between known key or text actions.
- Normally omit `settle_ms`; it defaults to 800 ms and is capped at 3000 ms.
  It controls final screenshot settling, not semantic readiness.
- A `wait` action delays exactly one second and does not expose intermediate
  pixels. Use it only when no observable condition or immediate final
  screenshot can answer the next decision.
- Do not stack waits to gain confidence. After one delayed screenshot, act on
  new evidence or change approach.
- If the returned screenshot shows a new window, dialog, menu, or layout,
  begin a new call before further coordinate input.
- If a batch reports partial input or an emission failure, inspect the returned
  screenshot before deciding whether any remaining action is still needed.
- Two attempts based on the same visual hypothesis without new evidence are
  enough. Take one fresh screenshot, state a different hypothesis, and change
  the route.
- Do not nudge coordinates repeatedly, add Enter speculatively, or switch
  between click and double-click without new visual evidence.
- Do not launch replacement application instances merely to escape uncertain
  state; parallel instances make focus, ownership, persistence, and final
  verification ambiguous.

## Efficient Work

- Perform the mutation once the source, destination, and operation are known.
  Additional investigation must answer a concrete blocker.
- Combine related read-only Bash checks into one bounded command or script.
- For bulk structured data, prefer a parser or documented API over a
  shell-owned clipboard.
- If clipboard transfer is necessary, keep its owner alive until paste
  completes. After one focus-verified paste failure, change mechanism.
- Background Bash output does not satisfy an instruction requiring visible
  terminal output. Run the final command in a visible terminal and verify the
  displayed result with a screenshot.
- Before recursive delete, move, overwrite, ownership, or permission changes,
  resolve an absolute target and preview the affected paths.

## Verification

- Verification is requirement-scoped. One authoritative final-state check per
  requirement is enough.
- Use a fresh GUI screenshot for visible layout, canvas appearance, dialog
  state, focus, and application-visible results.
- Use structured artifact reads for durable files and effective runtime reads
  for settings and services.
- Do not infer exact text, hidden state, persistence, or object identity from
  pixels when a structured file or runtime check is available.
- Conversely, a background file or process check does not prove that a result
  is visibly displayed when visibility is part of the instruction.
- Verify exact identity, value, type, scope, filename, destination, and
  persistence. Confirm unrelated content is preserved for narrow edits.
- Keep delivery evidence, visible UI evidence, and task evidence distinct.

## Persistence And Final State

- Commit Apply, OK, save, and confirmation dialogs when using an application.
- Account for caches, locks, running processes, and reload behavior after
  direct edits.
- Verify persistent defaults from effective configuration or a newly created
  blank object, not only from an object edited before the setting changed.
- Re-read durable state once after the final mutation.
- Clean up only temporary state created by the task that can affect the result,
  application lifecycle, or user data.

## Completion Gate

Before replying `TASK_COMPLETE`, verify:

- the exact target object or application;
- the exact requested value, text, formatting, or setting;
- every requested page, slide, cell, item, or scope;
- required persistence, filename, destination, and save operation;
- no unresolved confirmation dialog or accidental unrelated mutation.

Use one fresh requirement-matched check after the final mutation. A final
screenshot from the mutation call is sufficient when it directly proves the
visible requirement; use a separate durable check when pixels cannot prove
persistence or exact data.

Use `TASK_INFEASIBLE` only for a demonstrated hard blocker such as a missing
application, permission, credential, required semantic capability, or
contradictory requirement. Visual ambiguity alone is not infeasibility when
Bash, a durable artifact, or a documented application API can complete the
task.
