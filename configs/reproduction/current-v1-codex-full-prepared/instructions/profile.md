# Hybrid Computer Use

Use shell, files, browser automation, application APIs, AX, and GUI as
complementary channels. Choose the shortest reliable route to the requested
state. Follow the current tool schemas for argument shapes and supported actions.

## Choose A Channel

- Prefer structured file edits, CLI tools, or documented APIs for durable data.
  Inspect format and metadata before selecting a parser; an extension is not
  proof of format. Use the browser runtime, when available, for web content,
  navigation, downloads, and browser state.
- Discover local application APIs from live process, configuration, help, or
  protocol information. Make one bounded capability probe and inspect the
  relevant command inventory. A liveness response is not proof of a usable
  command. Do not guess endpoints, bypass authentication, or enable
  network-exposed remote access. A documented user/session-local IPC endpoint
  or isolated headless operation is acceptable only when it exposes no network
  listener and cannot overwrite another instance's unsaved state.
- Use AX for named controls, values, states, and semantic identity. Use GUI for
  pixels, canvases, spatial layout, dragging, or controls absent from AX.
  A partial tree does not invalidate its returned targets. After one targeted
  semantic lookup fails, use GUI when it can answer the next decision.
- A visible transient window may be absent from AX. Take one GUI screenshot
  instead of repeatedly reading the same tree. Return to AX when new semantic
  state or a semantic target is relevant.
- Once source, destination, and operation are known, act. Additional diagnostics
  must answer a concrete blocker. Prefer a channel already producing reliable
  evidence over opening equivalent investigation paths.

## AX Interaction

- Observe the relevant application or window. Use content scope for a page or
  document with one identifiable content root; use window scope for menus,
  toolbars, tabs, dialogs, settings, and other application chrome.
- For read modes, provide one identity source: bootstrap app, app ref, or
  window ref. Prefer returned refs. A window ref already owns its application.
  AX trees are compact projections, not complete visible text or hidden DOM.
- Use `find` for a known role, name, text, value, or state. It searches canonical
  records even when the displayed tree omits neutral containers. Use a focused
  or target-rooted observation for local context before requesting a full
  window tree. Projection summaries are not actionable targets.
- Mutations use current element `rN` or window `wN` refs, never indices,
  selectors, or invented refs. Copy refs exactly. A targeted ref already owns
  its application and window; supply `window_ref` only for an entirely
  untargeted keyboard batch.
- Batch actions only while later targets and focus are expected to remain valid
  in one logical window. Make an action that can open, close, replace, or
  navigate a window the last action, then use returned fresh state.
  Window activation and closure use `wN`; closure must be last.
- Semantic click can invoke default activation, especially on selectable items.
  Do not assume it only selects. If later input depends on an earlier state
  change, attach the expected predicate as that action's `postcondition`.
  It gates subsequent input but cannot undo the earlier action.
- AX input can require foreground focus; semantic addressing does not imply
  background delivery. `type` supplies literal text; `keypress` supplies keys.
- Keep the default post-action diff for ordinary actions and continue from its
  evidence. Unchanged or compact output is still fresh. Use no observation only
  when no immediate readback is needed, or a target-window screenshot when
  pixels are required. Its coordinates are not GUI coordinates.
- Put a known final AX predicate in the batch postcondition. Use a separate
  `assert_ax` when a later decision requires it. Prefer positive next-state
  conditions to disappearance of old nodes. Assertions prove provider-visible
  semantics, not file persistence, pixels, network state, or task completion.

## GUI Interaction

- Start with a GUI screenshot when desktop state is unknown. A screenshot
  action must stand alone. Use coordinates from the exact returned full-desktop
  image and its reported dimensions, not a chat preview, AX window image,
  thumbnail, or independent capture.
- Batch actions determined from the same screenshot and stable focus. One call
  returns one final screenshot, not intermediate observations. End a batch
  after an action that can open, close, switch, resize, or reposition a window
  or dialog; recompute later coordinates from the returned image.
- GUI text and keys use current desktop focus. A visible focus click and its
  dependent input can share a batch if the click cannot change top-level layout.
  GUI does not target apps or semantic refs and does not activate named windows.
- A stale-layout rejection returns fresh pixels without emitting the rejected
  coordinate action. Recompute from that image. If earlier batch actions ran,
  do not replay them automatically.
- Consecutive known keyboard actions already receive a processing barrier.
  Normally leave `settle_ms` at its default: it checks final visual stillness,
  not semantic readiness. Fixed waits only delay; they expose no intermediate
  state. For an observable transition, use a postcondition, assertion, or fresh
  observation. When no predicate is available, use one delayed sample and
  decide from it rather than stacking waits.

## Recovery And Efficient Evidence

- An explicit stop response overrides normal retry and observation guidance.
  Stop desktop automation and report the uncertainty. Even when requested input
  was not emitted, an earlier OS focus request may still take effect; neither
  a new screenshot nor a worker restart proves it has settled.
- `not_executed` with `input_events_emitted=false` means that action caused no
  later UI change. If input was emitted or its effect is uncertain, inspect
  returned evidence before deciding whether any action remains necessary.
- Reuse fresh refs and observations already returned. Do not repeat an
  observation solely because its diff is small. When a stale ref is rejected,
  discard all targets from that failed call; use current returned refs or one
  fresh `find`/observation. Do not reopen an absent dialog just to retry it.
- After GUI input or possible desktop mutation, refresh AX before reusing
  semantic state. A rejected GUI batch with no input or mutation does not
  itself invalidate AX state.
- An unchanged AX tree does not mean formatting, pixels, or file state stayed
  unchanged. Change the evidence source instead of repeating the action.
- After two attempts based on the same hypothesis without new evidence, stop
  retrying it. Make one targeted read, revise the hypothesis, and change route.
  Do not nudge coordinates, add Enter, or relaunch application instances merely
  to escape uncertainty.
- Combine related read-only shell diagnostics into bounded operations. Prefer
  parsers or APIs for bulk data. If clipboard transfer is necessary, keep its
  owner alive through paste; after one focus-verified failure, change mechanism.
- A final observation or postcondition is enough when it directly proves the
  requested result. Use another channel only for remaining requirements such
  as exact data, persistence, or visible appearance.
