# Third-Party Notices

## Computer Use

The Linux Computer Use implementation includes MIT-licensed code from
`iFurySt/open-codex-computer-use`. Its copyright and license are retained
in `LICENSE`.

## OSWorld V2

These files are modified copies of files from `xlang-ai/OSWorld-V2` at
revision `d578d2d4e0dc82b43e270fdaa7fa89d9708cd154`, which is licensed under
the Apache License 2.0 (copy in `licenses/Apache-2.0.txt`):

- `osworld-overlay/desktop_env/controllers/setup.py`
- `osworld-overlay/desktop_env/file_source.py`
- `osworld-overlay/desktop_env/desktop_env.py`
- `osworld-overlay/desktop_env/evaluators/getters/__init__.py`
- `osworld-overlay/desktop_env/evaluators/getters/chrome.py`
- `osworld-overlay/desktop_env/evaluators/getters/state.py`
- `osworld-overlay/desktop_env/providers/docker/provider.py`
- `osworld-overlay/desktop_env/providers/volume.py`
- `hcu/osworld/task_loader.py`

Each file names its upstream path in its header and remains under the Apache
License 2.0. The modifications provide bounded retries and diagnostics around guest
startup, task setup and Chrome DevTools connections, per-task runtime
settings, guest disk capacity checks, and task lookup without a domain.
`osworld-overlay/desktop_env/cache.py` and `osworld-overlay/desktop_env/cdp.py`
are original to this repository.

`configs/osworld-v1-tasks.json` is a normalized task-ID/domain index from
`xlang-ai/OSWorld`'s public `evaluation_examples/test_all.json`.
It contains no task definitions, assets, answers or
evaluation results. The original benchmark is distributed under Apache-2.0.
The separate exclusion list records this project's evaluation scope.

## Playwright

`StableIndexer` in `computer-use/native/linux/provider/ax_records.py` adapts
the role/name-based reference retention rule from Microsoft Playwright's
`packages/injected/src/ariaSnapshot.ts` (`computeAriaRef`). The Python adaptation
uses caller-supplied AT-SPI path mappings, full-name fingerprints and an
index-allocation set instead of state stored on DOM elements.
The adapted portions retain Playwright's Apache License 2.0 attribution:
Copyright (c) Microsoft Corporation.
The license is in `licenses/Apache-2.0.txt` and the upstream notice is in
`licenses/Playwright-NOTICE.txt`.

Other behavior follows designs from Playwright and Playwright MCP, including
strict-mode error messages, error context files and file output mode.
Code comments identify those design references. Playwright is installed
separately for OSWorld browser integration; it is not bundled as a runtime
dependency of the native desktop provider.

## Native desktop dependencies

`computer-use/vendor/golang.org/x/text` contains the packages needed for
Unicode key-name validation, from Go's x/text v0.22.0. Its BSD-style LICENSE
and PATENTS are retained in that directory. Vendoring permits offline builds.

The Windows provider directly references FlaUI.Core 5.0.0, FlaUI.UIA3 5.0.0
and System.Drawing.Common 8.0.10, which are MIT licensed, through NuGet.
Native builds restore those packages;
their source is not vendored here. Self-contained Windows packages also
include the Microsoft .NET runtime and must retain its third-party notices.
The macOS provider links system Apple frameworks and uses the Swift toolchain.

The Python package requires `docker`, `requests` and `urllib3`, as declared in
`pyproject.toml`. OSWorld integration additionally uses the dependencies of the
external OSWorld checkout, including Playwright and its browser installation.
These packages are installed separately, not vendored in this source package.

### Cua Driver and yabai

The macOS SkyLight bridge and event recipes derive from these MIT-licensed
implementations. Their copyright and permission notices follow.

- Cua Driver: https://github.com/trycua/cua, source revision
  `b8a0f32a06c75225ba24ebb5ab14f6507fa90d15`.
- yabai: https://github.com/asmvik/yabai, source revision
  `dd845723416f5fe92af49fad5ebab00369e07edd`.

MIT License

Copyright (c) 2025 Cua AI, Inc.

Copyright (c) 2019 Åsmund Vikane

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Agent CLIs

`make agents` downloads the agent releases listed in
`configs/agent-artifacts.lock.json` from their publishers, together with the
Node runtime Gemini CLI needs. They are not part of this repository and keep
their own licenses: Codex (Apache-2.0), Claude Code (proprietary),
`@google/gemini-cli` (Apache-2.0), `@xai-official/grok-linux-x64`
(Apache-2.0, bundled THIRD_PARTY_NOTICES) and Node.js (MIT).
The downloader prepares local executables; it is not a redistribution packager.
Before distributing downloaded executables, obtain and retain their publishers'
license and notice files and verify the applicable redistribution permissions.

## Not redistributed

This repository does not redistribute benchmark task data, evaluator gold
artifacts, VM images, model credentials, or private execution traces.
