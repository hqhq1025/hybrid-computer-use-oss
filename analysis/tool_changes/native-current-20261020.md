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
(`desktop_env.file_source.configure`). An isolated compatible overlay then
passed 360 runner tests with 28 skips. Go transport race tests also passed.

Native CI run `39416860159` passed both platform jobs: macOS ran 348 tests,
built its native package, and passed the MCP smoke; Windows passed native
protocol and policy tests, built its package, and passed the MCP smoke.

Two baseline CI failures also exposed missing test isolation. Combo fixtures
use PID 4242 but consulted the host's real process generation. The local host
happened to have that PID; CI did not. The fixture now supplies a fixed process
generation. Runner unit tests mocked other desktop_env modules but omitted
the new file_source import. They now mock that import too. These changes
preserve product identity validation and scoring assertions.

CI run `39418544534` passed all five jobs after those fixture fixes.
Windows VM smoke then passed ten native MCP checks, including Unicode edits,
exact text selection, input-free rejections, scaled coordinate clicks, and a
64-action GUI batch. UIA and Win32 confirmed the selected list item; the
WinForms application's cached selection did not update. Platform documentation
records this application-callback limitation.

The macOS VM built the complete native package, but real ref-owned actions
exposed another contract gap: the provider treated `a1` as an app name instead
of resolving the supplied process identity. Accessibility actions, window
operations, and service snapshot refreshes now share a request-scoped identity.
They resolve the bound PID and process generation without name fallback.
The binding is restored after the synchronous request. Five regressions cover
short app refs, stale process rejection, discovery, and binding cleanup.
CI run `39421333133` passed all five jobs after the identity fix. The cpu001
Mac desktop then passed ref-owned activation, Unicode selection, complete
selection, duplicate rejection, scaled screenshots, and a 64-key batch.

An expanded desktop check exposed a second integration gap: native app
discovery omitted window records that the Go window-wait path consumes.
App discovery now includes window titles and identities. Failed enumeration
or title reads fail explicitly, so missing evidence cannot prove a window
closed. Unknown or ambiguous native handles do not receive executable refs.
Window identity paths match observation, and active state uses actual focus.
These changes and their regressions are confined to the macOS provider.
Final desktop window-wait validation and native CI are pending.

Combined checks and native execution remain pending. The user excludes the
personal MacBook Pro; testing uses isolated cpu001 VM overlays and native CI.
Native CI builds and tests are distinct from interactive desktop tests. No new
model evaluation is scheduled by this change.

## Disposition

Testing and publication status will be recorded after validation. No formal
product release, live Mac installation, or live Windows installation has
been changed.
