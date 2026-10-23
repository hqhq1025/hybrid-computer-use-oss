using ComputerUse.Windows.Runtime;

internal static class AccessibilityBridgeNoteTests
{
    internal static void Run()
    {
        var vcl = new[] { "soffice.bin", "vclplug_winlo.dll", "user32.dll" };
        var bridged = new[] { "soffice.bin", "vclplug_winlo.dll", "winaccessibility.dll" };
        var titleBarOnly = new[] { true };
        Check(WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, vcl, titleBarOnly)
                is { } note && note.Contains("SAL_FORCE_IACCESSIBLE2=1") && note.Contains("pid 42"),
            "A VCL frame without the bridge module that exposes only its title bar gets the note.");
        Check(WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, bridged, titleBarOnly) is null,
            "A loaded bridge module means the tree shape has another cause.");
        Check(WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, vcl, new[] { true, false }) is null,
            "Any content beside the title bar means the bridge is working.");
        Check(WindowsAccessibilityBridgeNote.Note("Notepad", "Untitled", 42, vcl, titleBarOnly) is null,
            "Only VCL frame windows qualify, whatever modules the process loaded.");
        Check(WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, new[] { "soffice.bin" }, titleBarOnly) is null,
            "A SALFRAME class without the VCL module is not trusted as VCL.");
        Check(WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, null, titleBarOnly) is null &&
            WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, vcl, null) is null &&
            WindowsAccessibilityBridgeNote.Note("SALFRAME", "Untitled 1", 42, vcl, Array.Empty<bool>()) is null,
            "Unreadable module or tree facts produce no note.");
        Check(WindowsAccessibilityBridgeNote.RootChildrenAreTitleBars(() => Enumerable.Repeat(true, 40)) is { } many &&
            many.Any(value => !value),
            "An unexpectedly wide root is not reported as decoration-only.");
        Check(WindowsAccessibilityBridgeNote.RootChildrenAreTitleBars(
                () => throw new InvalidOperationException("gone")) is null,
            "A failed root read is an unknown fact.");
        Console.WriteLine("Accessibility bridge note tests passed.");
    }

    private static void Check(bool condition, string message)
    {
        if (!condition) { throw new InvalidDataException(message); }
    }
}
