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

- The tool schemas, descriptions, server instructions, error texts, base policy
  and selected interaction guide are model input. A change to any of them
  changes the benchmark condition. The contract golden test
  (`computer-use/contract_golden_test.go`) fails on MCP-visible changes;
  regenerate it with `go test -run TestPublicContractMatchesGolden
  -update-contract` and explain the change in the pull request.
- Product instruction edits require a new evaluation attempt; keep instruction
  revisions separate and do not infer performance from a wording change.
  `instructions/base.md` owns common safety and outcome policy; the selected
  profile owns interaction guidance. The runner alone supplies `TASK_COMPLETE`,
  `INFEASIBLE` and `ASK_USER` markers. See [Instructions](docs/INSTRUCTIONS.md).
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

For a review attachment that includes current fixes and untracked source files,
export the working tree directly. This does not require or claim a commit:

```bash
python3 scripts/export-submission.py export --output /tmp/submission.zip
python3 scripts/export-submission.py verify /tmp/submission.zip \
  --work-dir /tmp/submission-check --python-tests
```

Both destinations must be new. Stop concurrent source edits before export;
the exporter rejects detected changes during capture. `SUBMISSION_MANIFEST.json`
records only the included paths, package format, module and product version.
It contains no timestamps, transformation log or working-tree provenance. ZIP is the review-attachment
format used by CI; `.tar.gz` is also supported.

The source attachment includes only product introductions and usage guides as
documentation. Keep development history, review findings, defect notes, test
reports and experiment records outside that attachment. `CHANGELOG.md`,
`CONTRIBUTING.md`, `AGENTS.md`, CI workflows, Git configuration, the exporter
and its tests, and generated experiment directories remain local.

The allowlist includes source types under `computer-use/`, `hcu/`,
`osworld-overlay/`, `tests/`, `scripts/`, `docs/`, `instructions/`, `licenses/`,
Dockerfiles and selected root files. Only the build, install, download and MCP
smoke scripts listed in `USER_SCRIPTS` are included. Configurations are limited
to `configs/env.example`, the agent lock, `configs/osworld-v1-tasks.json`,
`configs/osworld-v1-excluded-tasks.json`, `*.example.json` and
the usage guide `configs/reproduction/README.md`. User documentation is selected
by the explicit `USER_DOCS` list in the exporter; new pages require review before
inclusion. Raw task directories, preparation manifests, results, logs,
credentials and images remain excluded. Allowed source and tests are included
even when untracked. Other types require reviewing the allowlist in the exporter.
Credentials, Git metadata, external
checkouts, results, downloaded binaries and build caches are excluded.
Symlinks in selected source trees are rejected; supported credential and
host-path patterns cause export to fail rather than silently modifying code.
Review the manifest and content before delivery; pattern checks cannot detect
every secret or identifying detail.

Only the exported copy receives the anonymous Go module path, including matching
references/imports. Notices retain the upstream source and all license/copyright
text; project fork locators and their identifying revisions are not required
attribution. The checkout is unchanged. `--identity MARKER` adds a
case-insensitive rejection check for project identity outside attribution
files. Copyright holders (including the root `LICENSE`), upstream lineage,
vendored source and third-party legal notices are preserved. These may identify authors: the result is identity-redacted
source with retained legal attribution, not a guarantee of complete anonymity.
Markers are also checked in member paths. Address checks use general IPv4
recognition, including escaped spellings, rather than a list of real hosts.
Loopback, unspecified addresses, documentation ranges and the documented default
Docker bridge are allowed. These checks are not a complete hostname, IPv6 or
personal-information detector. Review configuration values, names, URLs,
screenshots and accompanying reports manually.
Resolve any conflict with review requirements before submitting; do not remove
legally required notices to satisfy a blind-review claim.

Verification unpacks the actual archive into a fresh directory, builds the Linux
release with Docker, checks `--version`, initializes MCP and requires exactly
`AX` and `GUI`, then compares every packaged license notice with the unpacked
source and rejects unexpected release files. `verification.json` is written
only on success. Install `.[test]` before using `--python-tests`, which runs
offline Python tests from the unpacked source and reports optional-dependency
skips. The report records the verification Python version and resolved
distribution names/versions (not editable install paths), the Go compiler
version read from the built executable, Docker versions when used, and the
Computer Use version. `--builder local` instead runs real Go tests/build with the
installed toolchain and records that different validation path explicitly.
Neither mode starts an agent, model request, VM or desktop action. The Docker
base image must already be cached or obtainable; vendored Go dependencies need
no network during compilation.

The `submission` CI job installs test dependencies, performs Docker verification
and runs Python tests from the unpacked source with no Git metadata. Native
macOS/Windows CI separately builds and tests their packages; neither is evidence of desktop permissions or
interactive operation. To prepare a clean Linux binary distribution directly,
use `bash scripts/build-release.sh NEW_OUTPUT_DIRECTORY`; the explicit output
must not already exist. Do not distribute the shared `artifacts/` directory.

For an already reviewed, committed release, export its revision without Git history:

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
