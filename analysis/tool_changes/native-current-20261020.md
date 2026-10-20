# Native providers and the current AX contract

Status: `testing`
Created: 2026-10-20
Change ID: `native-current-20261020`
Baseline: remote main `80c5c0e`, including the tested implementation `f847d1a`.
Package version: 1.0.0, unchanged.
Baseline Linux image: `osw-hybrid:ax-bugfix-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-native-current-20261020`.
No new image or formal release is assigned.

## Problem and scope

The user requested parallel Windows and macOS integration with the current
implementation, plus parallel SDK and CLI tests. The native providers do not
recognize the current `select` intent. Windows also rejects the new private
`editTarget` field in the shared provider corpus.

Main CI run `39407874670` confirms the Windows protocol rejection. Its macOS
job stops before compilation because `setup-swift` cannot install version
6.2.3. The macOS 15 runner inventory includes Xcode 26.2. The workflow now
selects that installed toolchain and prints its Swift version.

Native changes must preserve target ownership, preflight before input,
readback, and partial-input evidence. Unsupported selection ranges must fail
explicitly. A provider must not silently ignore a requested edit target.
The implementation must use native capabilities without application names,
benchmark task IDs, or task recipes.

SDK and CLI work is testing only. Their audit findings do not authorize
unrelated implementation changes in this candidate.

## Model-contract review

The public tools remain AX and GUI. The shared schema, server instructions,
and evaluated-agent guide already describe selection and remain unchanged.
This candidate connects native providers to that existing contract.
Platform documentation will describe implemented and unsupported ranges.
Private protocol tests must cover accepted requests and rejected targets.

## Safety, compatibility, and cost

Selection may change application state; success requires native readback.
No new action retry or cross-window permission is introduced. Existing
Linux evaluation evidence applies only to the baseline Linux image.
No native latency, token, cost, or success-rate gain is claimed.

## Verification

Windows adds UIA TextPattern selection with native range resolution and full
selection readback. SelectionItem selects one item and verifies the complete
container selection. macOS writes AXSelectedTextRange and verifies the UTF-16
range, selected text, and source text. Both providers reject unsupported
cross-element selection and Linux composite edit targets before input.
macOS ordered input now accepts 64 steps instead of 30, matching the Go core.

Changed files are the native selection implementations, request validators,
capability projection and focused tests under `computer-use/native/windows/`
and `computer-use/native/macos/`. macOS `build.sh` includes the new Swift files.
The workflow and `docs/PLATFORMS.md` describe the toolchain and native limits.
GUI coordinate scaling and explicit final observations already use the current
implementation and require no additional changes.

Initial checks on Linux passed:

- Windows runtime and protocol test project compile for Windows x64.
- Swift 6.2 portable tests: 14 passed, including five selection tests.
- Swift source syntax checks and two macOS packaging tests passed.
- `git diff --check` passed.

The first combined `make test` passed Go checks and all 607 Linux provider
tests. Runner tests stopped with 2 failures and 18 errors because the selected
external OSWorld checkout lacks this repository's installed overlay API
(`desktop_env.file_source.configure`). Those failures do not validate the
runner; a compatible isolated overlay is required before repeating that step.

Combined checks and native execution remain pending. The user excludes the
personal MacBook Pro; testing uses isolated cpu001 VM overlays and native CI.
Native CI builds and tests are distinct from interactive desktop tests. No new
model evaluation is scheduled by this change.

## Disposition

Testing and publication status will be recorded after validation. No formal
product release, live Mac installation, or live Windows installation has
been changed.
