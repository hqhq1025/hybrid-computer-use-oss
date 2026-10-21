# Platform-specific application launch guidance

Status: `testing`
Created: 2026-10-21
Change ID: `macos-launch-guidance-20261021`
Baseline version: `1.0.0`; native macOS candidate, no new release.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Environment: MacAgentBench official image cache, isolated macOS 26.3 VM.

The hybrid instructions previously prescribed `setsid -f APP` for every OS.
The macOS VM has `/usr/bin/open` and no `setsid` on its command path.
The instruction could therefore prevent an agent from launching an application.

`instructions/hybrid.md` now limits the existing `setsid` guidance to Linux.
It describes `open -a APP` for macOS and asks the agent to reuse an existing
instance and verify its window. This is a general OS launch mechanism;
it names no benchmark application, task or solution.

The MCP schema, server instructions, parameter descriptions and execution
code do not change. The hybrid guide is the affected model-facing surface.
The candidate must use a new attempt after changing this guide. No task-score,
token, latency or cost improvement is claimed. Runtime safety checks still
govern subsequent AX and GUI input.

Validation: checked command availability in the actual macOS 26.3 guest.
The deployment canary uses LaunchServices to start application bundles.
Benchmark validation is pending. No formal release is promoted.
