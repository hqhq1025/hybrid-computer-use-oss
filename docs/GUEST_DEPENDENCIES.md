# Guest Workstation Dependencies

The official OSWorld V1 and V2 qcow2 images are preserved unchanged. Hybrid
Computer Use builds separate derived guest images containing ordinary upstream
workstation dependencies.

The dependency builder is under
`guest-images/workstation-deps-v014-beta1/`. The two explicit manifests are:

- `profiles/v1-core/`: the smaller V1 profile based on the high-frequency V1
  audit;
- `profiles/v2-full/`: the broader dependency demand observed in V2.

Together they cover:

- document and Office formats: `openpyxl`, `python-docx`, `python-pptx`,
  `odfpy`, `xlrd`, XlsxWriter, LibreOffice UNO and Python macro support;
- PDF, image, OCR and archive tooling: Poppler, MuPDF, qpdf, Ghostscript,
  ImageMagick, Tesseract, ExifTool, libarchive, zip and 7-Zip;
- audio and video tooling: ffmpeg, SoX, librosa, SoundFile, Vosk and
  faster-whisper;
- browser and CDP clients: Python and Node Playwright, Selenium, pychrome,
  `ws` and `chrome-remote-interface`;
- data and plotting libraries: NumPy, pandas, SciPy, scikit-image,
  scikit-learn, matplotlib, seaborn, Plotly, Altair and Bokeh;
- general workstation utilities including `rg`, SQLite, `file`, `strings`,
  `lsof`, `jq`, `wmctrl`, `xdotool`, clipboard tools and disk expansion tools.

V1 intentionally omits V2's low-frequency heavy stacks: speech recognition,
music transcription, background-removal/ONNX, medical imaging, Java, LaTeX and
OpenBoard. V2 retains them because historical V2 tasks explicitly attempted
to install or use those dependencies.

The candidate intentionally excludes software whose installation or
configuration is itself an OSWorld task, project-specific dependency
environments, model weights, benchmark helpers and repository-authored
near-solution commands.

Build the shared V2-superset cache once:

```bash
deps=guest-images/workstation-deps-v014-beta1
"$deps/cache-dependencies.sh" \
  /path/to/cache \
  "$deps/profiles/v2-full"
```

Build V1 core:

```bash
"$deps/build-candidate.sh" \
  /path/to/v1-official-source.qcow2 \
  /path/to/v1-core.qcow2 \
  64 \
  /path/to/cache \
  "$deps/profiles/v1-core"
```

Build V2 full:

```bash
"$deps/build-candidate.sh" \
  /path/to/v2-official-source.qcow2 \
  /path/to/v2-full.qcow2 \
  100 \
  /path/to/cache \
  "$deps/profiles/v2-full"
```

Validated candidates:

| Benchmark | Profile | Image | SHA-256 | Actual host size |
| --- | --- | --- | --- | --- |
| V1 | `v1-core` | `/datadrive2/osworld-v1/images/workstation-deps-v014-beta1/osworld-v1-ubuntu-workstation-deps-v014-beta1-core-64g.qcow2` | `acb8297f6d5e94a2c57eac6db05cc9822939d2c6389ce52cec5eb93306139479` | about 24 GiB |
| V2 | `v2-full` | `/datadrive2/osworld-v2/images/workstation-deps-v014-beta1/osworld-v2-ubuntu-workstation-deps-v014-beta1-100g.qcow2` | `76d3eebed863d6834254729c2bd7805d080cfbf3f6de1c4036a78d8b010f404a` | about 30 GiB |

Both images passed `qemu-img check`, a real
`happysixd/osworld-docker` QEMU boot, dependency imports and command checks,
an `openpyxl` write/read round trip, LibreOffice UNO/Python checks, and the
v0.14 `AX`/`GUI` smoke. Reports are stored under
`/datadrive2/osworld-v1/smokes/workstation-deps-v014-beta1-core-20260830-r2`
and
`/datadrive2/osworld-v2/smokes/workstation-deps-v014-beta1-full-20260830`.

The default `python3` remains `/usr/bin/python3`. Third-party packages are
provided through `PYTHONPATH` from `/opt/osw-workstation`, preserving Ubuntu
desktop integrations such as `gi`, AT-SPI and PyAutoGUI. The venv `bin`
directory is appended, rather than prepended, to `PATH` so package CLIs such
as `yt-dlp` remain available without replacing the system Python interpreter.

## Task Canaries

The August 30, 2026 canaries used Codex `0.151.0`, Computer Use `v0.14`, and
`gpt-5.4` with `xhigh` reasoning.

| Benchmark | Task | Dependency evidence | Outcome |
| --- | --- | --- | --- |
| V1 core | `6f81754e-285d-4ce0-b59e-af7edb02d108` | `file` was available; no package installation or missing-module error | Judge `PASS`; same eight train IDs as history; RAW `0.0` due evaluator interpretation |
| V2 full | `060` | `openpyxl` and `python-pptx` imported immediately; no `pip install` or missing-module error | Truncated at the 100-call budget; Judge `FAIL` on slide order, animations, duplicate content and broken links |

Evidence paths:

- `/datadrive2/osworld-v1/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v1-6f81754e-r2`
- `/datadrive2/osworld-v2/canaries/workstation-deps-v014-beta1-g54xhigh-20260830/v2-060`
- `/datadrive2/repos/osw-hybrid/logs/workstation-deps-v014-beta1-g54-canary-20260830.tokens.jsonl`

V1 and V2 do not share a sudo password. V1 requires
`--client-password password`; V2 uses
`--client-password osworld-public-evaluation`. Omitting the V1 override causes
the unified runner to fail before task execution while aligning the guest
clock.

These canaries show that the dependency layer removes missing-package
blockers. They do not show that every task becomes shorter or more accurate:
the agent can still choose a lower-level implementation, make application or
file-format mistakes, or exhaust its model-call budget.
