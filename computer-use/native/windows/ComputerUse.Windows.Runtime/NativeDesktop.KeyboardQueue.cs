using System.Diagnostics;
using System.IO.MemoryMappedFiles;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

internal static partial class NativeDesktop
{
    private static IKeyboardInputBarrier CreateKeyboardInputBarrier(ushort[] keys)
        => new KeyboardInputBarrier(keys);

    private sealed class KeyboardInputBarrier : IKeyboardInputBarrier
    {
        private const int HeaderBytes = 64;
        private const int RecordBytes = 32;
        private const uint Magic = 0x31514943;
        private readonly TextFocus _focus;
        private readonly uint _tag;
        private readonly int _count;
        private MemoryMappedFile? _mapping;
        private MemoryMappedViewAccessor? _view;
        private EventWaitHandle? _ready;
        private EventWaitHandle? _changed;
        private EventWaitHandle? _stop;
        private Process? _helper;
        private bool _closed;
        private int _consumed;

        internal KeyboardInputBarrier(ushort[] keys)
        {
            _count = checked(keys.Length * 2);
            _tag = unchecked((uint)RandomNumberGenerator.GetInt32(1, int.MaxValue - _count));
            _focus = ReadKeyboardFocus() ?? throw Failure("The keyboard input focus is unavailable.", false);
            try
            {
                if (_count is < 1 or > 4096)
                    throw Failure("The keyboard input event count is unsupported.", false);
                var architecture = TargetArchitecture(_focus.FocusProcess);
                var executable = Path.Combine(AppContext.BaseDirectory,
                    $"computer-use-input-queue-{architecture}.exe");
                if (!File.Exists(executable) || !File.Exists(Path.ChangeExtension(executable, ".dll")))
                    throw Failure("The native keyboard queue observer is not installed.", false);
                var name = $"Local\\ComputerUse.InputQueue.{_focus.FocusProcess}.{_focus.FocusThread}";
                var bytes = checked(HeaderBytes + _count * RecordBytes);
                _mapping = MemoryMappedFile.CreateNew(name, bytes, MemoryMappedFileAccess.ReadWrite);
                _view = _mapping.CreateViewAccessor(0, bytes, MemoryMappedFileAccess.ReadWrite);
                _ready = NewEvent(name + ".Ready", EventResetMode.ManualReset);
                _changed = NewEvent(name + ".Changed", EventResetMode.AutoReset);
                _stop = NewEvent(name + ".Stop", EventResetMode.ManualReset);
                _view.Write(0, Magic);
                _view.Write(4, 1U);
                _view.Write(8, _focus.FocusProcess);
                _view.Write(12, _focus.FocusThread);
                _view.Write(16, unchecked((uint)Environment.ProcessId));
                _view.Write(20, unchecked((uint)_count));
                _view.Write(24, unchecked((uint)bytes));
                _view.Write(28, 1);
                for (var index = 0; index < _count; index++)
                {
                    var offset = HeaderBytes + index * RecordBytes;
                    _view.Write(offset, unchecked(_tag + (uint)index));
                    _view.Write(offset + 4, (uint)(index < keys.Length
                        ? keys[index] : keys[_count - index - 1]));
                    _view.Write(offset + 8, index < keys.Length ? 0U : 1U);
                }
                var start = new ProcessStartInfo(executable)
                {
                    UseShellExecute = false,
                    CreateNoWindow = true,
                };
                start.ArgumentList.Add("--pid");
                start.ArgumentList.Add(_focus.FocusProcess.ToString());
                start.ArgumentList.Add("--tid");
                start.ArgumentList.Add(_focus.FocusThread.ToString());
                _helper = Process.Start(start) ?? throw Failure("The keyboard observer could not start.", false);
                if (!_ready.WaitOne(3000) || _view.ReadUInt32(36) != 1 || _view.ReadUInt32(40) != 0)
                    throw Failure($"The keyboard observer could not attach (Win32 error {_view.ReadUInt32(40)}).", false);
                if (!SameReceiver())
                    throw Failure("The keyboard input receiver changed before input.", false);
            }
            catch (Exception error)
            {
                var cleanup = Close();
                if (error is ProviderFailureException && cleanup is null) throw;
                throw Failure(error.Message + (cleanup is null ? "" : " " + cleanup), false);
            }
        }

        public nuint Tag(int index) => unchecked((nuint)(_tag + (uint)index));

        public void WaitForConsumption()
        {
            string? failure = null;
            try
            {
                var clock = Stopwatch.StartNew();
                while (true)
                {
                    var receipts = ReadReceipts();
                    _consumed = receipts.Count(receipt => receipt.Sequence > 0);
                    if (WindowsKeyboardQueueReceipts.AreComplete(receipts))
                    {
                        if (!WindowsKeyboardQueueReceipts.ModifiersReleased(receipts))
                            failure = "Keyboard messages were removed, but modifier release could not be confirmed.";
                        break;
                    }
                    if (!SameReceiver())
                    {
                        failure = "The keyboard receiver changed threads; remaining input consumption is unknown.";
                        break;
                    }
                    if (_helper!.HasExited || clock.ElapsedMilliseconds >= 5000)
                    {
                        failure = "Keyboard message consumption timed out or the observer exited; do not repeat the input without observing the application.";
                        break;
                    }
                    _changed!.WaitOne((int)Math.Clamp(5000 - clock.ElapsedMilliseconds, 1, 50));
                }
            }
            catch (Exception error)
            {
                failure = "Keyboard message consumption is unknown: " + error.Message;
            }
            var cleanup = Close();
            if (cleanup is not null) failure = failure is null ? cleanup : failure + " " + cleanup;
            if (failure is not null) throw Failure(failure, true);
        }

        private bool SameReceiver()
        {
            var current = ReadKeyboardFocus();
            return current is not null && current.Value.FocusProcess == _focus.FocusProcess &&
                current.Value.FocusThread == _focus.FocusThread;
        }

        private WindowsKeyboardQueueReceipt[] ReadReceipts()
        {
            var receipts = new WindowsKeyboardQueueReceipt[_count];
            for (var index = 0; index < receipts.Length; index++)
            {
                var offset = HeaderBytes + index * RecordBytes;
                var sequence = _view!.ReadInt32(offset + 12);
                Thread.MemoryBarrier();
                receipts[index] = new(sequence, _view.ReadUInt32(offset + 16) != 0,
                    _view.ReadUInt32(offset + 20));
            }
            return receipts;
        }

        private string? Close()
        {
            if (_closed) return null;
            _closed = true;
            string? failure = null;
            try
            {
                _view?.Write(28, 0);
                _stop?.Set();
                if (_helper is not null)
                {
                    if (!_helper.WaitForExit(2000))
                    {
                        failure = "The keyboard observer did not stop within its cleanup deadline.";
                        _helper.Kill();
                        if (!_helper.WaitForExit(1000))
                            failure += " Its owned process remains active.";
                    }
                    if (_view is not null && (_view.ReadUInt32(44) != 0 || _view.ReadUInt32(32) != 0))
                        failure = $"Keyboard observer cleanup is incomplete (Win32 error {_view.ReadUInt32(44)}).";
                    if (_helper.HasExited && _helper.ExitCode != 0 && failure is null)
                        failure = $"Keyboard observer exited with error {_helper.ExitCode}.";
                }
            }
            catch (Exception error)
            {
                failure = "Keyboard observer cleanup failed: " + error.Message;
            }
            finally
            {
                _helper?.Dispose();
                _view?.Dispose();
                _mapping?.Dispose();
                _ready?.Dispose();
                _changed?.Dispose();
                _stop?.Dispose();
            }
            return failure;
        }

        public void Dispose()
        {
            var failure = Close();
            if (failure is not null) Console.Error.WriteLine(failure);
        }

        private ProviderFailureException Failure(string message, bool emitted) => new(
            "desktop_input_failed", emitted ? "emission" : "preparation", message,
            emitted, emitted, actionEvidence: new JsonObject
            {
                ["route"] = "input.keyboard_queue",
                ["requested_events"] = _count,
                ["consumed_events"] = _consumed,
            });

        private static EventWaitHandle NewEvent(string name, EventResetMode mode)
        {
            var result = new EventWaitHandle(false, mode, name, out var created);
            if (created) return result;
            result.Dispose();
            throw new InvalidOperationException("A keyboard observer event is already owned.");
        }

        private static string TargetArchitecture(uint processId)
        {
            using var process = Process.GetProcessById(checked((int)processId));
            if (!IsWow64Process2(process.Handle, out var machine, out var nativeMachine))
                throw new InvalidOperationException("The keyboard receiver architecture is unavailable.");
            return (machine == 0 ? nativeMachine : machine) switch
            {
                0x14c => "x86",
                0x8664 => "x64",
                0xaa64 => "arm64",
                _ => throw new InvalidOperationException("The keyboard receiver architecture is unsupported."),
            };
        }

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool IsWow64Process2(nint process, out ushort processMachine, out ushort nativeMachine);
    }
}
