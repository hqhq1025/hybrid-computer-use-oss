# GUI-Only Computer Use

The only Computer Use MCP tool available is GUI. Shell, file editing, browser
automation, and documented application APIs remain available when exposed by
the agent. AX trees, refs, and `wait` steps are unavailable. Follow the
current GUI schema for argument shapes and supported actions.

## Choose A Channel

- Prefer structured file editing, CLI tools, or APIs for durable data. Inspect
  actual format and metadata before choosing a parser. Use an available browser
  runtime for web content and browser state; it does not add AX semantics.
- Discover application APIs from live process, configuration, help, or protocol
  information. Make one bounded probe and inspect relevant documented commands.
  Do not guess endpoints, bypass authentication, or expose remote access.
  Local-only automation must remain scoped to the user/session and preserve
  other instances' unsaved state.
- Use GUI for visible state, canvases, layout, pointer input, dragging, scrolling,
  and focused keyboard input. Missing semantics is a channel boundary, not proof
  that a target or application is absent.
- Act once source, destination, and operation are known. Extra investigation
  must resolve a concrete blocker; prefer a route already giving reliable evidence.

## Observe And Act

- Start with a screenshot when desktop state is unknown. A screenshot action
  must stand alone. Use exact returned full-desktop pixels and reported image
  dimensions, not a chat preview, thumbnail, window size, or independent capture.
- Resolve ambiguity, clipping, occlusion, and offscreen targets by inspecting
  or changing visible state before input. GUI does not accept app names,
  semantic refs, window IDs, or model-authored frame IDs.
- Batch actions determined from the same screenshot and stable focus. A call
  returns one final screenshot, not intermediate observations.
- End a batch after an action that may open, close, switch, resize, reposition,
  or navigate a window/dialog. Recompute subsequent coordinates from the final
  screenshot in a new call.
- Text and keys go to current desktop focus. A visible focus click and its
  dependent input can share a batch when the click cannot change top-level
  layout. A click already includes press and release.
- Stale-layout rejection returns fresh pixels without emitting the rejected
  coordinate action. Recompute coordinates; do not replay earlier actions that
  already ran in that batch.

## Waiting And Recovery

- Known consecutive keyboard actions already receive a processing barrier.
  Normally keep the default `settle_ms`; it measures final visual stillness,
  not semantic readiness. A still image alone does not prove an async task done.
- A fixed wait only delays execution and exposes no intermediate state. Use
  observable conditions through an available channel or a fresh screenshot.
  When no predicate is available, use one delayed sample, then act on evidence
  or change approach instead of stacking waits.
- An explicit stop response overrides normal recovery. Stop desktop automation
  and report uncertainty; a new observation or worker restart is not proof that
  a pending OS request cannot still take effect.
- After partial or uncertain input, inspect returned evidence before deciding
  whether any action remains necessary. Input emission does not prove it
  reached the intended target or completed the task.
- After two attempts with the same visual hypothesis and no new evidence,
  take one targeted observation and change mechanism. Do not repeatedly nudge
  coordinates, add Enter, or alternate click and double-click speculatively.
- Do not launch replacement application instances merely to escape uncertainty.
  They make focus, ownership, persistence, and final verification ambiguous.

## Efficient Evidence

- Combine related read-only shell checks. Prefer parsers or APIs for bulk data.
  If clipboard transfer is necessary, keep its owner alive through paste;
  after one focus-verified failure, change mechanism.
- Use final screenshots for required appearance and visible state, structured
  artifact reads for exact data and files, and effective runtime state for
  settings/services. Pixels do not prove hidden state or persistence; background
  reads do not prove a result is visibly displayed.
- Reuse the final screenshot from an action when it directly proves the visual
  requirement. Add another check only for a remaining requirement or conflicting
  evidence. Visual ambiguity alone is not a hard blocker when another available
  general-purpose channel can complete and verify the task.
