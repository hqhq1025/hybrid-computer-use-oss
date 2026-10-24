using System.Runtime.InteropServices;
using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

internal static partial class NativeDesktop
{
    private const uint TextCharacterMessage = 0x0102;
    private const uint TextSelectionMessage = 0x00C2;
    private const int TextMessageTimeoutMilliseconds = 1000;
    private const int TextInputTimeoutMilliseconds = 15000;

    internal readonly record struct TextFocus(
        nint Foreground, nint Focus, uint OwnerProcess, uint OwnerThread,
        uint FocusProcess, uint FocusThread, nint Root);

    internal static void ValidateTypedText(string text)
    {
        for (var index = 0; index < text.Length; index++)
        {
            if (text[index] == '\0')
                throw new ProviderFailureException("invalid_argument", "validation",
                    "Text cannot contain NUL characters.");
            if (!char.IsSurrogate(text[index])) continue;
            if (!char.IsHighSurrogate(text[index]) || index + 1 >= text.Length ||
                !char.IsLowSurrogate(text[index + 1]))
            {
                throw new ProviderFailureException("invalid_argument", "validation",
                    "Text contains an unpaired UTF-16 surrogate.");
            }
            index++;
        }
    }

    // Use only known native control contracts, including subclasses whose
    // original class is reported by RealGetWindowClass. Application names
    // and document contents do not select an input route.
    internal static bool IsStandardTextControlClass(string name) =>
        name.Equals("Edit", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("RichEdit20W", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("RICHEDIT50W", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("RichEditD2D", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("RichEditD2DPT", StringComparison.OrdinalIgnoreCase);

    // Sent character messages avoid keyboard queues whose modal loops can
    // discard VK_PACKET without translating it. A successful API return is
    // emission evidence only: the receiver can reply before processing ends.
    internal static void TypeText(string text, nint expectedRoot = default)
    {
        ValidateTypedText(text);
        if (text.Length == 0) return;
        var initial = ReadTextFocus();
        var attempted = 0;
        var returned = 0;
        var messageIndex = 0;
        var sourceIndex = 0;
        var sourceCount = 0;
        uint currentMessage = 0;
        var sequencePhase = "not_started";
        var started = TextInputTicks();
        if (initial is null || (expectedRoot != 0 && initial.Value.Root != expectedRoot))
        {
            throw Failure("The expected Unicode input focus is unavailable.", false);
        }
        var controlClass = ReadTextControlClass(initial.Value.Focus);
        if (controlClass.Length == 0)
            throw Failure("The focused window's native control class is unavailable.", false);
        var selectionInsertion = IsStandardTextControlClass(controlClass);
        RequireWritable();
        var scan = text.IndexOfAny(['\r', '\n']) >= 0 ? TextVirtualKeyScan(0x0D) : 0;
        if (text.IndexOfAny(['\r', '\n']) >= 0 && (scan == 0 || scan > 255))
            throw Failure("The Enter key scan code is unavailable; no text was sent.", false);

        ProviderFailureException? inputFailure = null;
        try
        {
            for (; sourceIndex < text.Length; sourceIndex++)
            {
                var unit = text[sourceIndex];
                if (unit is '\r' or '\n')
                {
                    sourceCount = unit == '\r' && sourceIndex + 1 < text.Length && text[sourceIndex + 1] == '\n' ? 2 : 1;
                    // A sent keydown cannot be paired with a later sent
                    // character through the receiver's queued-message lookup.
                    // Receivers handling both would apply the newline twice.
                    sequencePhase = "enter_character";
                    Send(TextCharacterMessage, 0x0D, 1U | (scan << 16));
                    if (unit == '\r' && sourceIndex + 1 < text.Length && text[sourceIndex + 1] == '\n')
                        sourceIndex++;
                }
                else if (selectionInsertion)
                {
                    var end = sourceIndex + 1;
                    while (end < text.Length && text[end] is not '\r' and not '\n') end++;
                    sourceCount = end - sourceIndex;
                    sequencePhase = "selection_text";
                    Send(TextSelectionMessage, 1, 0, text[sourceIndex..end]);
                    sourceIndex = end - 1;
                }
                else
                {
                    sourceCount = 1;
                    sequencePhase = char.IsHighSurrogate(unit) ? "surrogate_high" :
                        char.IsLowSurrogate(unit) ? "surrogate_low" : "character";
                    Send(TextCharacterMessage, unit, 1);
                }
            }
        }
        catch (ProviderFailureException error)
        {
            inputFailure = error;
        }
        finally
        {
            if (attempted > 0)
            {
                // GetMessage can dispatch sent characters without returning
                // to the application's outer loop. Wake that loop once,
                // including after partial input, without replaying any text.
                // Posting is not an acknowledgement of processing or paint.
                (bool Success, int Error) wake;
                try { wake = PostTextWake(initial.Value); }
                catch (Exception error) { wake = (false, error.HResult); }
                if (!wake.Success)
                {
                    var description = $" Message-loop wake failed (error code {wake.Error}); " +
                        "inspect the application before further input.";
                    if (inputFailure is null)
                    {
                        sourceCount = 0;
                        sequencePhase = "wake";
                        currentMessage = 0;
                        inputFailure = Failure("Text messages were sent." + description, false);
                    }
                    else
                    {
                        inputFailure = new ProviderFailureException(
                            inputFailure.Code, inputFailure.Phase, inputFailure.Message + description,
                            inputFailure.InputEventsEmitted, inputFailure.MutationUnknown,
                            actionEvidence: inputFailure.ActionEvidence);
                    }
                }
            }
        }
        if (inputFailure is not null) throw inputFailure;

        void Send(uint message, uint value, uint parameter, string? insertion = null)
        {
            currentMessage = message;
            if (ReadTextFocus() != initial)
                throw Failure("Input focus changed; remaining text was not sent.", false);
            RequireWritable();
            var remaining = TextInputTimeoutMilliseconds - (TextInputTicks() - started);
            if (remaining <= 0)
                throw Failure("Text input reached its time limit; remaining text was not sent.", false);
            // EM_REPLACESEL is a system message below WM_USER. Windows
            // marshals its input string into the receiver process; a timed-out
            // receiver can still finish using that copy after this call ends.
            var buffer = insertion is null ? 0 : Marshal.StringToHGlobalUni(insertion);
            try
            {
                Interlocked.Increment(ref _inputGeneration);
                attempted++;
                var timeout = (uint)Math.Min(TextMessageTimeoutMilliseconds, remaining);
                var result = insertion is null
                    ? SendTextMessage(initial.Value.Focus, message, value, parameter, timeout)
                    : SendTextSelection(initial.Value.Focus, buffer, timeout);
                if (!result.Success)
                    throw Failure($"Text message completion is unknown (Win32 error {result.Error}); " +
                        "do not replay this text without observing the application.", true);
            }
            finally
            {
                if (buffer != 0) Marshal.FreeHGlobal(buffer);
            }
            returned++;
            messageIndex++;
        }

        void RequireWritable()
        {
            if (!selectionInsertion) return;
            bool readOnly;
            try { readOnly = IsTextControlReadOnly(initial.Value.Focus); }
            catch (ProviderFailureException error) { throw Failure(error.Message, false); }
            if (readOnly)
                throw Failure("The focused text control is read-only; remaining text was not sent.", false);
        }

        ProviderFailureException Failure(string description, bool currentUnknown) => new(
            "desktop_input_failed", attempted == 0 ? "preparation" : "emission", description,
            returned > 0, attempted > 0,
            actionEvidence: new JsonObject
            {
                ["route"] = "input.window_messages",
                ["source_utf16_index"] = sourceIndex,
                ["source_utf16_count"] = sourceCount,
                ["source_utf16_length"] = text.Length,
                ["attempted_messages"] = attempted,
                ["returned_messages"] = returned,
                ["unknown_message_index"] = currentUnknown ? JsonValue.Create(messageIndex) : null,
                ["message"] = currentMessage,
                ["sequence_phase"] = sequencePhase,
                ["effect_verified"] = false,
            });
    }
}
