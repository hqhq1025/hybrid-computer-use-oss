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
- When a later step depends on a state that appears later, attach it as
  `until` instead of a separate read:

```json
{"mode":"act","actions":[{"type":"set_value","ref":"r31","value":"14"}],"until":{"type":"value","ref":"r31","value":"14"}}
{"mode":"act","actions":[{"type":"click","ref":"r5","until":{"type":"state","ref":"r5","state":"checked"}},{"type":"click","ref":"r9"}]}
{"mode":"observe","window_ref":"w1","until":{"type":"exists","selector":{"role":"dialog","name":"Print"},"timeout_ms":15000}}
```

  The first verifies a value. The second lets the later click run only after
  the earlier one took effect. The third waits for a dialog; with `app`
  instead of `window_ref`, it also waits for an application that is starting.
  A `selector` or `title` predicate checks every window of the application,
  so it finds a dialog or menu that opens as its own window.

- Take a GUI screenshot when the question is visual or AX lacks a visible
  element. Do not alternate screenshots and AX reads of an unchanged state.
- Combine related read-only shell checks into one command or script.

## When Something Fails

Every AX error starts with a `code`, a `hint` with a corrected call, and
whether input was sent. Next step by code:

- `invalid_argument`: nothing was sent. Fix the field named in `path` as the
  hint shows. The control is not missing.
- `stale_ref`: drop every ref from that result. Use refs from a newer result,
  or one fresh `find`.
- `window_changed`: the batch stopped after `completed` actions. Read the
  returned state and send only what remains.
- `until_failed`: the batch stopped at the action whose `until` failed; the
  actions before it ran. Read the returned state and revise the plan; do not
  resend the same batch.
- `until_unknown`: the predicate could not be checked. Check the requirement
  with another read or channel.
- `effect_unknown`: input may have been sent. Inspect state before any retry,
  so that nothing is applied twice.
- `ambiguous_window`, `window_not_found`: choose a `wN` from the listed windows.
- `no_focused_window`: start the batch with `activate`.
- `app_unavailable`: if the application is still starting, wait once with
  `observe`, `app`, and `until`; otherwise use GUI for it.
- `runtime_error`: retry once; if it repeats, use another channel.

A GUI stale-frame rejection sent nothing; plan again from the returned
screenshot. A stop response ends desktop automation: report the uncertainty
and stop.
After two attempts on the same hypothesis without new evidence, change the
mechanism or the channel. Do not nudge coordinates, add Enter, add waits, or
relaunch applications to escape uncertainty.
