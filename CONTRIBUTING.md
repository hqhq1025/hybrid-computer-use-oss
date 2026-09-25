# Contributing

## Development setup

- Go 1.22 or later for the MCP server.
- Python 3.10 or later. The provider tests also need PyGObject and the
  AT-SPI, GTK and GdkPixbuf typelibs (`python3-gi gir1.2-atspi-2.0
  gir1.2-gtk-3.0 gir1.2-gdkpixbuf-2.0` on Debian or Ubuntu).
- `pip install -e '.[test]'` for the runner and gateway tests.
- Docker for `make build`.
- Swift 6.2+ and the macOS SDK for the macOS provider; .NET 8 SDK on Windows
  for the Windows provider. See [native checks](docs/PLATFORMS.md).

## Checks

```bash
make test
```

runs gofmt, `go vet` and the Go tests in `computer-use/`, the provider tests
(`computer-use/runtime_test.py`) and the Python tests in `tests/`. Tests that
need an OSWorld-V2 checkout are skipped unless `OSWORLD_V2_ROOT` points at one
with the overlay installed. CI runs the same checks and also compiles the Go
tests for macOS and Windows. `make test-transports` exercises both native
worker transports with subprocess fixtures on Linux. Native CI also builds
and tests Swift/C# code; it does not prove interactive desktop permissions.

Put Go tests in the `_test.go` file of the source file they exercise,
provider tests under `computer-use/runtime_tests/`, and runner and gateway
tests under `tests/`. Prefer a test that runs the code over one that searches
source text.

## Rules that protect evaluations

- The tool schemas, descriptions, server instructions, error texts, skills and
  base instructions are model input. A change to any of them changes the
  benchmark condition. The contract golden test
  (`computer-use/contract_golden_test.go`) fails on MCP-visible changes;
  regenerate it with `go test -run TestPublicContractMatchesGolden
  -update-contract` and explain the change in the pull request.
- Keep `AX` and `GUI` as the only public MCP tools.
- Never copy, mount or inject this repository's `AGENTS.md` or other
  documentation into an evaluated agent.
- The Go server and native providers share a private protocol. Update affected
  providers together and keep `computer-use/testdata/provider-v2/` consistent.
- Run a setup canary before any change that needs a real task run to verify;
  real runs spend model quota.

## Pull requests

Keep a pull request to one topic, describe the behavior change and how you
verified it, and add an entry under "Unreleased" in `CHANGELOG.md` for
user-visible changes. Commit subjects follow `area: summary`, for example
`fix(gateway): ...` or `docs: ...`.

## Releases

`VERSION` is the only place that holds the release version. A release commit
updates `VERSION` and moves the changelog entries under the new version.

### Source archives

Export a reviewed, committed revision without Git history:

```bash
git archive --format=tar.gz --prefix=hybrid-computer-use/ \
  --output=/tmp/hybrid-computer-use-source.tar.gz HEAD
tar -tzf /tmp/hybrid-computer-use-source.tar.gz
```

Replace `HEAD` with the intended release tag when exporting a release. This
exports the selected committed tree, not uncommitted edits or untracked
files. Do not use it to claim delivery of changes still in the worktree.
Review the archive listing before sharing it.

Git archives contain no `.git` directory. The repository's `export-ignore`
attributes also exclude local artifacts, external checkouts, run evidence,
environments and caches even if those paths were accidentally tracked.
Agent binaries, the Node runtime, guest images and task assets are separate
downloads, not source-release contents. A recipient uses `make agents` and
the [OSWorld setup instructions](docs/OSWORLD.md) to obtain them.

## License

By contributing you agree that your contribution is licensed under the MIT
license. Files modified from OSWorld-V2 keep their Apache-2.0 header; see
`THIRD_PARTY_NOTICES.md`.
