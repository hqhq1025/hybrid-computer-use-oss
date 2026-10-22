# GUI screenshot scaling and generic instructions

Status: `merged`

Created: 2026-10-20

Baseline: remote main at the v4 development baseline; package VERSION remains
1.0.0. The evaluation harness records baseline tool version v0.14. These are
different namespaces; this integration does not create a product release.

Baseline evaluation image: `osw-hybrid:hcu-full-f3reuse-21963d1`.
Baseline record: `/datadrive2/results/hcu-paper-full-f3reuse-panel20-r1-20261019T1311Z/config/run-manifest.json`.
Integration source: `/datadrive2/repos/hybrid-computer-use-oss-gui-skill-main`.
Local executable: `artifacts/computer-use`. No new Docker image is published.

## Problem and changes

GPT-5.4 high-detail input resizes a 1920 x 1080 image to 1365 x 768 in the
recorded probes. Returning the larger dimensions makes model coordinates
disagree with the delivered visual space. GUI now fits the desktop within
1366 x 768 and uses the exact delivered/native ratio for each axis.

The native Linux foundation and GUI capture modules, macOS screenshot code,
Windows screenshot geometry, GUI runtime tests, macOS geometry tests, public
contract fixture, tool description, and architecture documentation change.
Coordinates remain full-desktop image pixels. Existing stale-layout checks
remain in place. Zoom, query clicks, screenshot opt-out, and AX additions are
outside this integration.

`instructions/base.md` and `instructions/hybrid.md` bound discovery, stop
after sufficient outcome evidence, and describe saving/closing/reopening an
open document when direct file editing is needed. These are generic channel
and evidence rules, with no task IDs, evaluator knowledge, or task helpers.

## Model-contract review

- Server instructions: no server lifecycle or capability change is needed.
- GUI description and contract fixture: coordinates refer to the returned
  screenshot dimensions; updated with the scaling change.
- AX schema and descriptions: unchanged; no AX action is added here.
- Developer documentation: screenshot dimensions updated in ARCHITECTURE.md.
- Evaluated-agent guidance: base and hybrid plain-text instructions updated.
  This repository uses these files rather than a separately injected skill.

## Evidence and limits

Separate 20-task panels use GPT-5.4 xhigh, official/original evaluation,
100 successful responses, and a 5400-second task timeout. Exact task lists
and configuration remain in each run's config/run-manifest.json.

| Candidate | Run IDs under /datadrive2/results | Raw score |
| --- | --- | --- |
| GUI scaling | hcu-paper-full-guiscale-panel20-r1-20261019T1708Z; hcu-paper-full-guiscale-panel20-r2-20261019T2008Z | 19/20; 18/20 |
| Generic instructions | hcu-paper-full-skill5-panel20-r1-20261019T2008Z | 19/20 |

Against two baseline rounds, paired median cost on common clean successful
tasks fell 15.7% for scaling (17 tasks) and 14.4% for instructions (18 tasks).
These estimates use input at $2.5/M and output at $15/M without cache discount.
They exclude failures and do not establish combined performance or full-set
capability. Scaling has a one-point average raw-score deficit against main.
The instruction candidate has only one evaluated round.

Smaller screenshots can reduce detail for other models. Windows native builds
and new combined desktop/model runs remain unverified in this integration.
Instruction changes can alter channel choice, stopping behavior, and cost.

## Verification and disposition

The user explicitly requested committing both candidates to main after
reviewing these limits on 2026-10-20. The two implementation commits are kept
separate. Main integration is not a claim that the prior two-round candidate
and two-round integration gates passed, and does not promote a new release.

Combined local validation on 2026-10-20:

- gofmt check, Linux go vet and go test: passed.
- GOOS=darwin GOARCH=arm64 go vet and GOOS=windows GOARCH=amd64 go vet: passed.
- make test-transports (darwin_sidecar_test and windows_sidecar_test): passed.
- /usr/bin/python3 computer-use/runtime_test.py: 550 passed.
- Python tests with declared dependencies in an isolated environment and
  PYTHONPATH pointing to this checkout's osworld-overlay: 344 passed,
  29 skipped. Log: /tmp/hcu-gui-skill-overlay-tests.log on cpu001.
- make build-local and make smoke: passed; public tools are AX and GUI.
- git diff --check: passed.

The initial make test used system Python without docker. The older runner
environment also lacked the matching desktop_env overlay; its failing tests
reproduced on the unmodified baseline. No runner code was changed to hide
these dependency failures. The final Python invocation used the repository's
actual overlay and an isolated dependency environment.

Native macOS/Windows builds and combined real-desktop/model evaluation were
not executed. Main adoption is user-authorized with these limits recorded.
