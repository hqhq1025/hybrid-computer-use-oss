# Hybrid Computer Use From Code

Draft profile for agents that drive the desktop from code: the `computer-use`
command line in the shell, or the `hcu_sdk` Python module. It extends
`hybrid.md`; channel routing and error recovery are the same. The calls are
the same as the `AX` and `GUI` tools, and every call returns one JSON record.

## When To Script

- Write a script when you already know several steps, when you must read or
  filter many elements, or when you must repeat one step over a list. Print
  only what the next decision needs.
- Use single calls while you are still exploring an unfamiliar window: each
  result shows what to do next.
- Never script blind input. Each action that should change state is followed
  by a `wait` step, or the script reads the state before it continues.

## Calls And Records

```python
import hcu_sdk as hcu
d = hcu.connect()
w = next(x.ref for x in d.apps().windows if "Report" in x.title)
for el in d.find_all(w, role="button"):        # follows next_cursor
    print(el.ref, el.name, el.states)
d.click("r12", wait={"ref": "r12", "state": "checked"})  # click, then wait
d.wait({"window": "Export"})                    # wait without input
values = d.read(["r31", "r32"]).read          # live value and states, no tree
```

```bash
computer-use apps --json
computer-use find w1 --role button --name-has Save --json
computer-use click r12 --wait '{"window":"Saved"}'; echo $?
computer-use wait '{"window":"Export","gone":true}
```

- Refs (`aN`, `wN`, `rN`) stay valid across commands and scripts. Copy them
  from a record; never build them.
- `act` returns no tree by default. Read state with `find`, `read`, or
  `observe` when the next step needs it.
- `observe` returns every element, never truncated. Pass `since` with an
  earlier `snapshot_id` to get only what changed.
- `find` returns at most `limit` matches and a `next_cursor`; `find_all`
  follows it.
- Screenshots are files; the record holds the path. Open the image only when
  the question is visual.

## Retries

- An act that the connection dropped may have run. The module resends it with
  the same `request_id`; the second answer is the first result, with
  `replayed: true`, and no input is sent twice. When you resend by hand, pass
  the same `request_id`.
- Errors are exceptions named after the code, such as `hcu.StaleRef` and
  `hcu.WaitTimeout`. They carry `code`, `hint`, `input_emitted`, and
  `completed`. On the command line the exit status says the same thing:
  0 success, 2 bad argument, 3 a `wait` step timed out, 4 stale ref or
  stale screenshot, 5 effect unknown, 6 window or application not found,
  10 runtime error, 64 bad command line. Codes 2, 4, and 6 sent no input.
- A `wait` step that does not hold stops the call with `wait_timeout`; its
  message says what the wait saw. `completed` counts the steps that ran.

## Do Not

- Do not send desktop input through other shell utilities or scripted
  accessibility calls; they bypass the window checks, and the refs and
  diffs then describe a state that no longer exists.
- Do not loop on the same failing call. After two attempts on one
  hypothesis, change the mechanism or the channel.
