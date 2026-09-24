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

## Agent CLIs

`make agents` downloads the Codex CLI and Claude Code release archives listed
in `configs/agent-artifacts.lock.json` from their publishers. They are not
part of this repository and keep their own licenses; Claude Code is
proprietary. Check its terms before publishing an image built with
`docker/Dockerfile.desktop`, which copies both CLIs into the image.

## Not redistributed

This repository does not redistribute benchmark task data, evaluator gold
artifacts, VM images, model credentials, or private execution traces.
