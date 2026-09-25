# Third-Party Notices

## Computer Use lineage

The Linux Computer Use implementation descends from
`hqhq1025/unify-computer-use`, itself derived from
`iFurySt/open-codex-computer-use`. The upstream repository carries the MIT
license copied at the repository root.

Upstream revision inspected while preparing this repository:
`276cc718886dfe5fc086d69f1d0d1378bdbec401`.

## OSWorld V2

These files are modified copies of files from `xlang-ai/OSWorld-V2` at
revision `d578d2d4e0dc82b43e270fdaa7fa89d9708cd154`, which is licensed under
the Apache License 2.0 (copy in `licenses/Apache-2.0.txt`):

- `osworld-overlay/desktop_env/controllers/setup.py`
- `osworld-overlay/desktop_env/desktop_env.py`
- `osworld-overlay/desktop_env/evaluators/getters/__init__.py`
- `osworld-overlay/desktop_env/evaluators/getters/chrome.py`
- `osworld-overlay/desktop_env/evaluators/getters/state.py`
- `osworld-overlay/desktop_env/providers/docker/provider.py`
- `osworld-overlay/desktop_env/providers/volume.py`
- `hcu/osworld/task_loader.py`

Each file names its upstream path in its header and remains under the Apache
License 2.0. The changes add bounded retries and diagnostics around guest
startup, task setup and Chrome DevTools connections, per-task runtime
settings, guest disk capacity checks, and task lookup without a domain;
compare a file with the upstream revision for the exact changes.
`osworld-overlay/desktop_env/cache.py` and `osworld-overlay/desktop_env/cdp.py`
are original to this repository.

## Playwright

Some behavior follows designs from Microsoft Playwright and Playwright MCP
(Apache License 2.0), such as stable element references in aria snapshots,
strict-mode error messages, error context files and file output mode. Code
comments name the design they follow. This repository contains no Playwright
source files.

## Native desktop dependencies

`computer-use/vendor/golang.org/x/text` contains the packages needed for
Unicode key-name validation, from Go's x/text v0.22.0. Its BSD-style LICENSE
and PATENTS are retained in that directory. Vendoring permits offline builds.

The Windows provider references FlaUI.UIA3 5.0.0 and its FlaUI.Core dependency,
which are MIT licensed, through NuGet. Native builds restore those packages;
their source is not vendored here. Self-contained Windows packages also
include the Microsoft .NET runtime and must retain its third-party notices.
The macOS provider links system Apple frameworks and uses the Swift toolchain.

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

## Not redistributed

This repository does not redistribute benchmark task data, evaluator gold
artifacts, VM images, model credentials, or private execution traces.
