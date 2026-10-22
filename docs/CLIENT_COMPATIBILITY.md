# Client compatibility

HCU exposes `AX` and `GUI` over stdio MCP. A client that discovers both tools
has passed discovery, not a complete desktop test. Check the model request,
tool result, and next model request to verify the whole path.

Use a minimal MCP configuration to add HCU to an existing client; see
[Computer Use MCP](MCP.md#codex) for Codex and Claude Code examples. The
[agent profiles](AGENT_PROFILES.md) also replace prompts and configure model
gateways for controlled runs; importing a full profile changes more than MCP.

## Schemas

The GUI schema declares its property names at the root and keeps its complete
`oneOf` branches. Root declarations let clients close the root object without
rejecting properties declared in a branch. Branches still define the types,
required fields, action limits, and the screenshot-only rule.

The server validates the original schema before input. A client may need a
different presentation for its model API; that presentation does not replace
server validation. Do not remove `$ref`, union branches, or unknown-field
checks merely to make a client accept the tool definition.

Claude Code uses the existing schema adapter. It changes the GUI schema view,
not results, images, errors, or action execution. Retain it until the selected
client and model API have been tested without it.

Gemini uses `computer-use` as its client-side MCP server key. Current Gemini
policy parsing splits server and tool names at an underscore, so
`computer_use` is ambiguous there. The generated allowlist uses the same key;
other clients retain their existing keys. This does not rename `AX` or `GUI`.

## Errors and images

An MCP tool error is a result with `isError: true`. It can still contain text,
images, and `structuredContent`. A protocol or connection failure is a
different kind of error.

Preserve the failure marker and all available evidence. For example, a
`gui_frame_stale` result can contain a fresh screenshot and state that no input
was sent. Converting that result to `Error(text)` discards the image needed
for recovery. Setting `isError` to false would incorrectly report success.

If a client API represents failure as an exception, carry the original result
on the exception and preserve it when saving the call and building model
messages. Code runtimes should also retain evidence when user code catches
the exception. Do not depend on the success-only image renderer.

MCP images contain bytes. CLI and Python SDK results use image paths; the
caller must explicitly send those images to its model. A path in terminal
output is not an image attachment. See [Code entry points](CODE.md) for
`CodeDesktop`, image sinks, and the PTC adapters.

HCU leaves `structuredContent` off by default and puts its evidence in text.
Some clients prefer a structured object over sibling text, although that
object may contain only part of the result. Keep the default unless the
consumer has been tested with `COMPUTER_USE_STRUCTURED_CONTENT=1`.

OpenCode v1.19.3 needs separate checks for ordinary MCP and experimental Code
Mode. Its ordinary MCP schema conversion closes the root object. Both paths
also turn tool failures into text-only errors in the upstream release, so
updating HCU alone does not repair error-image delivery in that client.

An offline native-client test of Claude Code 2.1.303 confirmed that successful
results retained text and PNG bytes. Error results retained `is_error`, text,
and the copyable GUI screenshot hint, but dropped the image. A separate GUI
screenshot call can obtain a new observation; do not repeat the failed
mutation to recover its image. This is a client limitation, not evidence that
the earlier operation succeeded. The test records image delivery separately
from its assertions about error text and flags.

Gemini CLI 0.65.0 also needs client-side fixes: its error path serializes image
content as text, and its scheduler can replace the error response again.
Its internal `wait_for_previous` scheduling hint belongs to the client; it
must not reach HCU as an unknown action argument. Do not make HCU ignore the
field or silently discard the requested scheduling behavior.

## Sessions, cancellation, and retry

Keep one MCP process for a desktop session. Conversation resume alone does
not prove that a restarted MCP process still recognizes earlier refs.

When mixing MCP, CLI, and SDK, use the same daemon with
`computer-use mcp --attach SOCKET`. The core then coordinates input and refs;
clients still have their own observation state.

A timeout or cancellation does not prove that input was never sent. Preserve
partial-input and unknown-effect facts. Never automatically repeat a desktop
mutation just because its response was lost. The Python SDK uses daemon-bound
replay IDs; the CLI exposes those IDs for explicit retries. Restarting the
daemon does not make an old mutation safe to repeat.

## Verification

For each client version and provider, check these paths separately:

1. Discover both tools and inspect the schema actually sent to the model.
2. Return a successful screenshot and inspect the next model request.
3. Return a tool error with a screenshot and structured facts; check all
   evidence and the failure marker in the next request.
4. Exercise a multi-call session, cancellation, and lost-response recovery.
   Record whether input was sent and whether it was repeated.
5. Run desktop tasks only after these checks pass. Report task success,
   parameter errors, timeouts, retries, image delivery, latency, and cost
   separately.

A local fake model endpoint can test client serialization without inference
charges. Such a test proves the client sent the image bytes, not that a model
interpreted them or that a real desktop task succeeded.
