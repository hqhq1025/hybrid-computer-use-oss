# macOS provider startup connection cleanup

Status: `merged`
Paths refer to the layout before the package split (a4006c5).
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `macos-sidecar-compat-20261021`
Baseline: `origin/main`, package version `1.0.0`, as selected for this worktree.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; this change targets the native macOS provider.
This work creates no image or release.

## Problem and evidence

The Go wrapper can abandon startup while the provider connects to its socket.
`waitForDarwinProviderConnection` previously sent the accepted connection to a
buffered channel. If a launcher error or deadline ended the caller first,
the connection had no owner. Closing the listening socket did not close
that accepted connection.

A deterministic regression controls when `Accept` returns. The test first
fails the launcher, then releases the accepted connection. Against the
baseline, the peer read timed out instead of receiving EOF. This demonstrates
the leaked connection without macOS or a desktop mutation.

## Change and contract review

`computer-use/darwin_sidecar_transport.go` now transfers the accepted connection
through an unbuffered channel. Startup closes a cancellation channel when
it returns. If startup ends before accepting ownership, the accept goroutine
closes the connection. A successful transfer leaves cleanup to the caller.

`computer-use/darwin_sidecar_startup_cleanup_test.go` covers both abandoned
and successfully transferred connections. The successful path exchanges bytes
after startup returns, proving that cleanup preserves the live connection.

The fix applies to every macOS provider launch. It adds no benchmark or
application conditions. The MCP instructions, AX/GUI schemas, parameter
descriptions, public documentation, and evaluated-agent instructions need
no changes because startup ownership is private. Error classification,
request replay rules, and desktop evidence remain unchanged.

The change prevents an unowned connection after failed startup. It does
not change mutation authorization or permission checks. No token, score,
latency, or cost improvement has been measured.

## Validation and disposition

The new failure regression failed against the baseline with
`read pipe: i/o timeout`. After the fix, all `TestDarwin` tests passed under
the Go race detector with `-tags darwin_sidecar_test` on Linux.
The complete Go suite also passed with
`go test -race -tags darwin_sidecar_test ./...`.

The combined candidate passed `make test`: Go checks, 607 provider tests,
and 360 Python tests, with 28 environment-dependent skips. The macOS 26.3
VM built the native bundle with Swift 6.3.2 and SDK 26.5. The r1 deployment
completed MCP initialization, AX application enumeration and a real GUI
screenshot through LaunchServices. See
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-r1-smoke/`.

The deterministic race test proves connection cleanup. The VM smoke proves
normal startup; it does not reproduce the startup race in LaunchServices.
The r2 native build also passed. Benchmark comparison remains pending.
No formal release is promoted.
