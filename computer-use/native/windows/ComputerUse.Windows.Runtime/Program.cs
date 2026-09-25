namespace ComputerUse.Windows.Runtime;

internal static class Program
{
    [STAThread]
    private static int Main()
    {
        try
        {
            Console.InputEncoding = new System.Text.UTF8Encoding(
                encoderShouldEmitUTF8Identifier: false);
            Console.OutputEncoding = new System.Text.UTF8Encoding(
                encoderShouldEmitUTF8Identifier: false);
            using var server = new ProviderServer();
            server.Run();
            return Environment.ExitCode;
        }
        catch (Exception error)
        {
            Console.Error.WriteLine(error);
            return 1;
        }
    }
}
