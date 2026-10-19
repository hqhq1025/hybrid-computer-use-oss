# Hybrid Computer Use

Channels: the shell (commands, scripts, format-aware libraries), the running
browser's automation endpoint when it exposes one, and the `AX` and `GUI`
tools. The tool descriptions give call shapes. This guide says which channel
fits which state, and how to combine them.

## Route By Where The State Lives

| State to change | Change it through | Reason |
| --- | --- | --- |
| Files that no running application holds open: data, code, configuration, structured documents, archives, media | Shell with a format-aware library or CLI; check the real format first | Exact, scriptable, and verifiable by parsing |
| A document an application holds open | That application: AX for menus, dialogs, and fields; GUI for what AX lacks | Its copy is what gets saved |
| Application or system settings | The settings UI through AX; the configuration file or CLI only when the application is closed or rereads it | A running application can write its cached settings back |
| Web pages, forms, signed-in sites | The open browser session: its endpoint if exposed, otherwise AX or GUI | Login, cookies, and tabs exist only in that session |
| Canvases, images, drawings, layout | The editor: AX for menus and dialogs, GUI for pixels and dragging | Visual objects have no names to address |
| Messages, mail, calendar items, accounts | The client application through AX | The client owns sending and sync |

- A documented automation endpoint that a running application already exposes,
  such as a browser debugging port, acts inside that application. Probe for it
  once, early. After one failed probe, use AX or GUI. Do not rebuild a site
  with shell HTTP requests, and do not start another browser or profile:
  neither shares the user's session, so their changes do not appear there.
- Learn an unfamiliar application from the application itself: its menus,
  dialogs, `--help`, manuals, and local files. Search the web only for facts
  the machine cannot supply; web pages often describe another version.

## Work Efficiently

- Once the source, target, and operation are known, act. Further checks must
  answer a concrete blocker.
- Plan from one read. After `observe` or `find`, put every action you can
  already determine for that window into one `act`.
- Every `act` result starts with what happened, then a diff:

```text
effect: r58 checkbox "Interface" checked=false→true
windows: opened w5 "Insert Sheet" (dialog, focused)
```

  `effect` gives each target's changed fields, `no visible change`, or
  `unknown` with the reason. `windows` names windows that opened, closed, or
  took focus, with a `wN` you can use at once. When these lines already show
  the result, do not read or wait again. Continue from the diff for other
  changes; read again only when the next decision needs more.
- One `act` can mix actions and `wait` steps in any order. Steps run in
  order, and the first step that fails stops the batch. A `wait` sends no
  input. It holds until its condition is true, or fails with `wait_timeout`
  after `timeout_ms` (default 5000) and returns the current state:

```json
{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]},{"type":"wait","window":"Export","gone":true}]}
{"mode":"act","actions":[{"type":"click","ref":"r7"},{"type":"wait","query":{"role":"menu item","name_contains":"Paragraph"}},{"type":"click","ref":"r9"}]}
{"mode":"act","actions":[{"type":"wait","window":"Untitled"}]}
```

  The first opens a dialog, waits for it, types into it, confirms, and waits
  for it to close. After a `window` wait, keys and text without `ref` go to
  that window. The second waits for a menu item before the next click; a
  `query` takes the same fields as `find`. The third waits without input,
  for example for an application that is starting.
- Forms of `wait`: `window` (a top-level window whose title contains the
  text, on the whole desktop), `query` (an element in any window of the
  batch's application), `ref` with `value`, `state`, or `text` (one
  element), or `text` alone (text anywhere in the window). `gone:true`
  waits for absence; a closed or exited application has no windows.
- Do not add a `wait` for what the report already shows. A checkbox that
  shows `checked=false→true`, or a dialog listed as `opened`, needs no wait.
  Wait only when a later step needs a state that may arrive after the
  action returns: a slow dialog, a menu item, a value computed later.
- Take a GUI screenshot when the question is visual or AX lacks a visible
  element. Do not alternate screenshots and AX reads of an unchanged state.
- Combine related read-only shell checks into one command or script.
- Start or restart a GUI application from the shell with
  `setsid -f APP ARGS >/tmp/APP.log 2>&1 </dev/null`. Some agent shells,
  Codex among them, kill the process group of each command when it returns;
  an application started there with `&`, `nohup`, or `disown` dies without
  showing a window.

## When Something Fails

Every AX error starts with a `code`, a `hint` with a corrected call, and
whether input was sent. Next step by code:

- `invalid_argument`: nothing was sent. Fix the field named in `path` as the
  hint shows. The control is not missing.
- `stale_ref`: drop every ref from that result. Use refs from a newer result,
  or one fresh `find`.
- `window_changed`: the batch stopped after `completed` actions. Read the
  returned state and send only what remains.
- `wait_timeout`: the batch stopped at that `wait`; the steps before it ran
  and none after it. The message says what was seen instead, or that the
  state could not be read. Read the returned state and revise the plan; do
  not resend the same batch.
- `effect_unknown`: input may have been sent. Inspect state before any retry,
  so that nothing is applied twice.
- `ambiguous_window`, `window_not_found`: choose a `wN` from the listed windows.
- `no_focused_window`: start the batch with `activate`.
- `app_unavailable`: if the application is still starting, wait once for
  its window with `{"mode":"act","actions":[{"type":"wait","window":"..."}]}`;
  otherwise use GUI for it.
- `runtime_error`: retry once; if it repeats, use another channel.

GUI errors start with the same line and add one code:

- `gui_frame_stale`: the window under a coordinate changed after the
  screenshot the coordinates came from, so nothing was sent. Plan again from the returned
  screenshot.

A stop response ends desktop automation: report the uncertainty
and stop.
After two attempts on the same hypothesis without new evidence, change the
mechanism or the channel. Do not nudge coordinates, add Enter, add waits, or
relaunch applications to escape uncertainty.
