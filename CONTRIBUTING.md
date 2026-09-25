# Contributing

## Development setup

- Go 1.22 or later for the MCP server.
- Python 3.10 or later. The provider tests also need PyGObject and the
  AT-SPI, GTK and GdkPixbuf typelibs (`python3-gi gir1.2-atspi-2.0
  gir1.2-gtk-3.0 gir1.2-gdkpixbuf-2.0` on Debian or Ubuntu).
- `pip install -e '.[test]'` for the runner and gateway tests.
- Docker for `make build`.

## Checks

```bash
make test
```

runs gofmt, `go vet` and the Go tests in `computer-use/`, the provider tests
(`computer-use/runtime_test.py`) and the Python tests in `tests/`. Tests that
need an OSWorld-V2 checkout are skipped unless `OSWORLD_V2_ROOT` points at one
with the overlay installed. CI runs the same checks and also compiles the Go
tests for macOS and Windows.

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
- The Go server and the Python provider share a private protocol. Change both
  sides together and update the fixtures in `computer-use/testdata/provider-v2/`.
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

## License

By contributing you agree that your contribution is licensed under the MIT
license. Files modified from OSWorld-V2 keep their Apache-2.0 header; see
`THIRD_PARTY_NOTICES.md`.
