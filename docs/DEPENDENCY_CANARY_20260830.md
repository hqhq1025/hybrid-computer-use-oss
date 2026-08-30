# Dependency Image Canary, August 30, 2026

## Configuration

- Task Agent and Judge: `gpt-5.4`, `xhigh`
- Codex: `0.151.0`
- Computer Use: released `v0.14`
- Skill SHA-256:
  `61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16`
- V1 image SHA-256:
  `acb8297f6d5e94a2c57eac6db05cc9822939d2c6389ce52cec5eb93306139479`
- V2 image SHA-256:
  `76d3eebed863d6834254729c2bd7805d080cfbf3f6de1c4036a78d8b010f404a`

The runs are diagnostic canaries, not a formal score comparison.

## Results

| Item | V1 | V2 |
| --- | --- | --- |
| Task | `6f81754e-285d-4ce0-b59e-af7edb02d108` | `060` |
| Dependency target | `file`, `python-docx` | `openpyxl`, `python-pptx` |
| Missing dependency | None | None |
| Runtime install | None | None |
| Model requests | 8 | 101 |
| Shell calls | 7 | 67 |
| MCP calls | 0 | 13 |
| Truncated | No | Yes |
| Official RAW | `0.0` | `0.0` |
| Interactive Judge | `PASS` | `FAIL` |

## Interpretation

The V1 answer matched the historical result: eight unique train IDs. The
agent still chose direct OOXML parsing rather than `python-docx`, so package
availability did not reduce work in this sample. The official evaluator
expects document mutation even though the task is naturally answer-only; the
interactive Judge verified the answer from all 100 records.

### V1 repeated-run comparison

To check whether the added dependency layer caused the first V1 canary's
higher step count, the same task was rerun three more times on the derived
dependency image and once on the unmodified official V1 qcow2. The Task Agent
model, effort, Codex binary, skill, base instructions, MCP release, runner and
model-call budget were held fixed.

| Guest | Run | Model requests | Shell calls | Events | Input tokens | Model latency | Total duration | Judge |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Unmodified official V1 qcow2 | control | 5 | 4 | 17 | 47,585 | 58.3 s | 302.5 s | PASS |
| Derived dependency image | r2 | 8 | 7 | 25 | 78,638 | 69.0 s | 327.9 s | PASS |
| Derived dependency image | r3 | 7 | 6 | 21 | 72,354 | 42.0 s | 328.1 s | PASS |
| Derived dependency image | r4 | 6 | 5 | 19 | 62,280 | 108.5 s | 354.5 s | PASS |
| Derived dependency image | r5 | 6 | 5 | 18 | 59,394 | 47.7 s | 312.0 s | PASS |

The four derived-image runs averaged 6.75 model requests, 5.75 shell calls,
68,167 input tokens and 330.6 seconds total duration. Their request count
ranged from 6 to 8, so the original 8-request observation is not a stable
fixed cost.

No run imported `python-docx` or another newly installed Python package, and
none attempted `pip` or `apt`. All five runs parsed the DOCX with Python's
standard-library ZIP/XML support. The extra calls were model-selected
inspection and verification passes, such as printing sample XML, checking
per-ID counts, checking the time range or repeating the parse. This experiment
therefore finds no direct dependency-triggered cause for the extra steps. The
small sample still shows a directional increase over the single current
control and the historical four-request run, so it does not prove zero
environment effect; a larger interleaved A/B would be needed for that claim.

The V2 agent imported `openpyxl` and `python-pptx` immediately. Historical
runs on the old guest first hit `ModuleNotFoundError` and installed those
packages at runtime. The new image therefore fixes the intended environment
gap. The task still failed because the generated deck had incorrect slide
ordering, no active animation timing, duplicate/mixed location content and
hyperlinks to orphan slide parts. The run exhausted its model-call budget
before producing a completion response.

Dependency availability and task quality are separate variables. A matched
same-model A/B against the original guest is required before claiming an
efficiency improvement.

## Operational Finding

The unified runner defaults to the V2 sudo password. V1 runs must pass:

```bash
--client-password password
```

V2 uses:

```bash
--client-password osworld-public-evaluation
```

The first V1 attempt omitted the override and failed before task execution
during guest clock alignment. The corrected run completed normally.

## Evidence

- V1:
  `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-r2`
- V1 repeated derived-image runs:
  `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-r3`,
  `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-r4`,
  and
  `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-r5`
- V1 unmodified official-image control:
  `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-official-control`
- V2:
  `/datadrive2/osworld-v2/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v2-060`
- Token log:
  `/datadrive2/repos/osw-hybrid/logs/workstation-deps-v014-beta1-g54-canary-20260830.tokens.jsonl`
