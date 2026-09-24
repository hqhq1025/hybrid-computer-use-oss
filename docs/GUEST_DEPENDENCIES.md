# Guest Workstation Dependencies

The official OSWorld and OSWorld-V2 guest images stay unchanged. The scripts
in `guest-images/workstation-deps/` derive separate images that add common
workstation dependencies, so agents do not spend their budget installing
packages that many tasks need.

Two package profiles are provided:

- `profiles/v1-core/`: a smaller profile for V1, based on the dependencies
  that V1 tasks use most often;
- `profiles/v2-full/`: the broader set that V2 tasks were observed to need.

Together they cover:

- document and Office formats: `openpyxl`, `python-docx`, `python-pptx`,
  `odfpy`, `xlrd`, XlsxWriter, LibreOffice UNO and Python macro support;
- PDF, image, OCR and archive tools: Poppler, MuPDF, qpdf, Ghostscript,
  ImageMagick, Tesseract, ExifTool, libarchive, zip and 7-Zip;
- audio and video tools: ffmpeg, SoX, librosa, SoundFile, Vosk and
  faster-whisper;
- browser and CDP clients: Python and Node Playwright, Selenium, pychrome,
  `ws` and `chrome-remote-interface`;
- data and plotting libraries: NumPy, pandas, SciPy, scikit-image,
  scikit-learn, matplotlib, seaborn, Plotly, Altair and Bokeh;
- general utilities including `rg`, SQLite, `file`, `strings`, `lsof`, `jq`,
  `wmctrl`, `xdotool`, clipboard tools and disk expansion tools.

`v1-core` leaves out the heavy stacks that only a few V2 tasks use: speech
recognition, music transcription, background removal with ONNX, medical
imaging, Java, LaTeX and OpenBoard.

Both profiles exclude software whose installation or configuration is itself
part of an OSWorld task, project-specific environments, model weights,
benchmark helpers and anything that would solve a task directly.

## Building

The build needs Docker, `qemu-img`, libguestfs (`virt-resize` and
`virt-customize`) and sudo. First cache the packages of the larger profile,
which covers both:

```bash
deps=guest-images/workstation-deps
"$deps/cache-dependencies.sh" /path/to/cache "$deps/profiles/v2-full"
```

Then derive an image from an official source image:

```bash
# V1 core, 64 GiB
"$deps/build-candidate.sh" /path/to/official-v1.qcow2 /path/to/v1-core.qcow2 \
  64 /path/to/cache "$deps/profiles/v1-core"

# V2 full, 100 GiB
"$deps/build-candidate.sh" /path/to/official-v2.qcow2 /path/to/v2-full.qcow2 \
  100 /path/to/cache "$deps/profiles/v2-full"
```

`build-candidate.sh` never overwrites an existing output image. Pass the same
size as `--volume-size` when you run tasks on the result.

## Verifying

`scripts/smoke-qemu-guest.py` boots an image in the official QEMU runtime,
runs the profile's `verify-guest.sh` (imports, command checks, an `openpyxl`
round trip and LibreOffice UNO checks) and calls `AX` and `GUI` once through
the uploaded MCP binary:

```bash
external/OSWorld-V2/.venv/bin/python scripts/smoke-qemu-guest.py \
  --label v2-full --image /path/to/v2-full.qcow2 --volume-size 100 \
  --output-dir runs/guest-smoke-v2-full
```

The script checks the `v2-full` profile by default; for a `v1-core` image pass
`--dependency-smoke guest-images/workstation-deps/profiles/v1-core/verify-guest.sh`.

Follow with a setup canary from [Running OSWorld](OSWORLD.md) before running
tasks on a new image.

## Python layout in the guest

The default `python3` stays `/usr/bin/python3`. Third-party packages come from
`/opt/osw-workstation` through `PYTHONPATH`, which keeps Ubuntu desktop
integrations such as `gi`, AT-SPI and PyAutoGUI working. The virtual
environment's `bin` directory is appended to `PATH`, not prepended, so package
CLIs such as `yt-dlp` are available without replacing the system interpreter.

## What the images do and do not change

In canary runs the dependency layer removed missing-package blockers: tasks
imported `openpyxl` or `python-pptx` immediately instead of installing them.
It does not make tasks shorter or more accurate by itself; an agent can still
choose a poor approach, make application or file-format mistakes, or exhaust
its model-call budget.
