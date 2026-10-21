# Strict CLI input

Status: `testing`
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `cli-strict-input-20261021`
Baseline: main `b11041d`, package version 1.0.0.
Baseline diagnostic image: `osw-hybrid:sdk-cli-code-mode-20261020-a1`.
Source: `/datadrive2/repos/hcu-cli-strict-input-20261021`.
No new image, model run, or formal release is created.

## Problem

The CLI parser accepted any option name. Command builders then ignored names
they did not read. A misspelled `click r2 --wiat ...` could therefore click
without waiting as requested. JSON arguments were decoded once,
without checking the rest of the input. Both trailing garbage and a second
JSON value could be ignored.

Earlier black-box evidence is in `/tmp/hcu-cli-audit-20261020/report.md`.
Inspection of the baseline confirmed the same parser and decoder behavior.

## Change and contract review

`computer-use/cli.go` checks command-specific option names before connecting
to the daemon. Unknown names, including inline `--name=value`, produce a
usage error. Shorthands retain their shared socket/session/output options.
Batch, schema, and daemon commands use the same name check. Batch reports
the actual parser error before its usage text.

The JSON argument decoder now requires EOF after its first value. Trailing
whitespace remains valid. Wait steps, appended waits, AX JSON, and GUI JSON
share this decoder and reject trailing content before execution.
The CLI-only `readArguments` and `readCallSequence` functions in `main.go`
also reuse it, covering `call --args`/`--args-file`, `call --calls`/`--calls-file`,
and batch input.
Their existing error exit classification remains unchanged.

Review found a collision in an allowed option: the global boolean `text`
flag caused `find --text VALUE` to consume no value and reject VALUE as an
extra positional argument. The parser now treats find's text filter as a
value option while retaining batch's boolean output switch.

`docs/CODE.md` documents exit 64 and the no-connection boundary. AX/GUI public
schemas, MCP instructions, provider behavior, daemon protocol, SDK, and
benchmark instructions remain unchanged. The existing CLI help documents
the accepted options and usage exit status. No task or application-specific
rules are added. The core still validates desktop argument values.

This deliberately rejects previously ignored options and malformed JSON.
Valid calls retain their existing output and input semantics. The change
does not promise score, token, or latency improvements.

## Validation and disposition

Seven focused regression groups passed against a real Go daemon and a fake
desktop. Invalid commands produced no new daemon connections and no input.
Tests cover each JSON entry point, files/stdin, valid trailing whitespace,
large integer precision, normal wait execution, valid option forms, and
request-ID replay. The same two core tests against baseline cli.go failed:
both returned success, connected to the daemon, and emitted one input.

Independent review and existing CLI/MCP compatibility checks passed. Logs
and the baseline overlay are under
`/datadrive2/hcu-eval/cli-strict-input-20261021/`. Full `make test` passed:
Go formatting/vet/tests, 607 provider tests, and 360 harness tests with 28
skips. Document checks reported no hard issues. Source integration awaits
CI. No model evaluation is needed to establish these deterministic parser
and no-input properties.
