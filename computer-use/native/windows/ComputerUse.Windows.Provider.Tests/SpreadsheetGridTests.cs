using System.Drawing;
using ComputerUse.Windows.Runtime;

internal static class SpreadsheetGridTests
{
    internal static void Run()
    {
        CheckA1Names();
        CheckScrolledViewport();
        CheckMergedAndHiddenColumns();
        CheckProbeBudget();
        CheckOccludedOrigin();
        CheckGridDetection();
    }

    private static void CheckA1Names()
    {
        Check(SpreadsheetGrid.TryParseA1("A1", out var row, out var column) &&
            row == 0 && column == 0, "A1 is row 0, column 0.");
        Check(SpreadsheetGrid.TryParseA1("AB12", out row, out column) &&
            row == 11 && column == 27, "AB12 is row 11, column 27.");
        Check(SpreadsheetGrid.TryParseA1("XFD1048576", out row, out column) &&
            row == 1048575 && column == 16383, "The last Calc cell parses.");
        foreach (var name in new[] { "", "A", "1", "A0", "a1", "Sheet1", "A1 ", "B-2" })
        {
            Check(!SpreadsheetGrid.TryParseA1(name, out _, out _), $"'{name}' is not a cell name.");
        }
        Check(SpreadsheetGrid.A1(11, 27) == "AB12" && SpreadsheetGrid.A1(0, 0) == "A1",
            "A1 formats zero-based coordinates.");
    }

    // A sheet scrolled to C10 shows rows 9..13 and columns 2..5; the last
    // column is cut by the area edge and still counts as visible.
    private static void CheckScrolledViewport()
    {
        var grid = new FakeGrid(firstRow: 9, firstColumn: 2, columnWidths: new[] { 80, 60, 70, 90 },
            rowHeight: 20, origin: new Point(100, 200));
        var area = new Rectangle(100, 200, 80 + 60 + 70 + 40, 5 * 20);
        var viewport = SpreadsheetGrid.ReadViewport(area, grid.Hit, probeBudget: 100, mayProbe: () => true)
            ?? throw new InvalidOperationException("viewport");
        Check(viewport.Row0 == 9 && viewport.Row1 == 13 && viewport.Column0 == 2 && viewport.Column1 == 5,
            $"Viewport range {viewport.Row0}-{viewport.Row1} x {viewport.Column0}-{viewport.Column1}.");
        Check(viewport.Cells.Count == 20 && viewport.Missing == 0 && viewport.Unprobed == 0,
            $"Every visible cell is read once ({viewport.Cells.Count}).");
        Check(viewport.Cells.Select(cell => (cell.Row, cell.Column)).SequenceEqual(
                from row in Enumerable.Range(9, 5) from column in Enumerable.Range(2, 4) select (row, column)),
            "Cells come back in row-major order.");
        Check(grid.Probes == 20, $"Each cell costs one probe ({grid.Probes}).");
        Check(grid.Points.All(area.Contains), "Probes stay inside the area.");
    }

    // A zero-width (hidden) column is skipped and a merged cell is listed once.
    private static void CheckMergedAndHiddenColumns()
    {
        var grid = new FakeGrid(firstRow: 0, firstColumn: 0, columnWidths: new[] { 50, 0, 50, 50 },
            rowHeight: 20, origin: new Point(0, 0));
        grid.Merge(row: 1, column: 2, columns: 2);
        var area = new Rectangle(0, 0, 150, 40);
        var viewport = SpreadsheetGrid.ReadViewport(area, grid.Hit, probeBudget: 100, mayProbe: () => true)
            ?? throw new InvalidOperationException("viewport");
        var names = viewport.Cells.Select(cell => SpreadsheetGrid.A1(cell.Row, cell.Column)).ToArray();
        Check(names.SequenceEqual(new[] { "A1", "C1", "D1", "A2", "C2" }),
            "Hidden column B is absent and the merged C2:D2 appears once: " + string.Join(',', names));
        Check(viewport.Column0 == 0 && viewport.Column1 == 3, "The range still spans the visible columns.");
    }

    private static void CheckProbeBudget()
    {
        var grid = new FakeGrid(firstRow: 0, firstColumn: 0, columnWidths: Enumerable.Repeat(40, 10).ToArray(),
            rowHeight: 20, origin: new Point(0, 0));
        var area = new Rectangle(0, 0, 400, 200);
        var viewport = SpreadsheetGrid.ReadViewport(area, grid.Hit, probeBudget: 30, mayProbe: () => true)
            ?? throw new InvalidOperationException("viewport");
        Check(grid.Probes == 30, $"The probe budget bounds hit tests ({grid.Probes}).");
        Check(viewport.Cells.Count + viewport.Unprobed == 100,
            $"Unprobed cells are counted ({viewport.Cells.Count} + {viewport.Unprobed}).");
        var stopped = new FakeGrid(firstRow: 0, firstColumn: 0, columnWidths: Enumerable.Repeat(40, 10).ToArray(),
            rowHeight: 20, origin: new Point(0, 0));
        var allowed = 12;
        var timed = SpreadsheetGrid.ReadViewport(area, stopped.Hit, probeBudget: 1000, mayProbe: () => allowed-- > 0);
        Check(timed is not null && stopped.Probes == 12 && timed.Unprobed > 0 && !timed.ExtentComplete,
            "A stopped clock ends probing and leaves the rest unprobed.");
    }

    // A window over the grid's top-left corner hides the viewport origin:
    // the range is unknown, so nothing is guessed.
    private static void CheckOccludedOrigin()
    {
        var grid = new FakeGrid(firstRow: 0, firstColumn: 0, columnWidths: new[] { 50, 50 },
            rowHeight: 20, origin: new Point(0, 0));
        grid.Occlude(new Rectangle(0, 0, 30, 30));
        Check(SpreadsheetGrid.ReadViewport(new Rectangle(0, 0, 100, 40), grid.Hit, 100, () => true) is null,
            "An occluded origin gives no viewport.");
        var partly = new FakeGrid(firstRow: 0, firstColumn: 0, columnWidths: new[] { 50, 50, 50 },
            rowHeight: 20, origin: new Point(0, 0));
        partly.Occlude(new Rectangle(60, 25, 100, 100));
        var viewport = SpreadsheetGrid.ReadViewport(new Rectangle(0, 0, 150, 60), partly.Hit, 100, () => true)
            ?? throw new InvalidOperationException("viewport");
        Check(viewport.Missing == 4 && viewport.Cells.Count == 5,
            $"Occluded cells are counted as missing ({viewport.Missing}, {viewport.Cells.Count}).");
    }

    // LibreOffice 24.8 lists the cells as the sheet's children; 25.2 reports
    // no children but answers a hit test with the cell. Either is a grid.
    // Another table is not, and its corner is probed only when it has no
    // children.
    private static void CheckGridDetection()
    {
        static bool IsCell(string name) => SpreadsheetGrid.TryParseA1(name, out _, out _);
        var probes = 0;
        string? Corner(string? name) { probes++; return name; }
        Check(SpreadsheetGrid.IsGrid("A1", () => Corner("A1"), IsCell) && probes == 0,
            "A first child named A1 makes a grid without a probe.");
        Check(SpreadsheetGrid.IsGrid(null, () => Corner("C7"), IsCell) && probes == 1,
            "A table without children whose corner hit is a cell is a grid.");
        Check(!SpreadsheetGrid.IsGrid(null, () => Corner("Header"), IsCell) && probes == 2,
            "A corner hit that is not a cell is no grid.");
        Check(!SpreadsheetGrid.IsGrid(null, () => Corner(null), IsCell) && probes == 3,
            "No corner hit is no grid.");
        Check(!SpreadsheetGrid.IsGrid("Name", () => Corner("A1"), IsCell) && probes == 3,
            "A table whose first child is not a cell is no grid and is not probed.");
    }

    private static void Check(bool condition, string message)
    {
        if (!condition) { throw new InvalidOperationException("SpreadsheetGridTests: " + message); }
    }

    private sealed class FakeGrid(int firstRow, int firstColumn, int[] columnWidths, int rowHeight, Point origin)
    {
        private readonly List<(int Row, int Column, int Columns)> _merged = new();
        private readonly List<Rectangle> _occluded = new();
        internal int Probes { get; private set; }
        internal List<Point> Points { get; } = new();

        internal void Merge(int row, int column, int columns) => _merged.Add((row, column, columns));

        internal void Occlude(Rectangle area) => _occluded.Add(area);

        internal SpreadsheetCellHit<string>? Hit(Point point)
        {
            Probes++;
            Points.Add(point);
            if (_occluded.Any(area => area.Contains(point))) { return null; }
            var row = firstRow + (point.Y - origin.Y) / rowHeight;
            var x = origin.X;
            for (var index = 0; index < columnWidths.Length; index++)
            {
                if (point.X >= x && point.X < x + columnWidths[index])
                {
                    var column = firstColumn + index;
                    var left = x;
                    var width = columnWidths[index];
                    foreach (var merged in _merged.Where(item => item.Row == row &&
                                 column >= item.Column && column < item.Column + item.Columns))
                    {
                        column = merged.Column;
                        left = origin.X + columnWidths.Take(merged.Column - firstColumn).Sum();
                        width = columnWidths.Skip(merged.Column - firstColumn).Take(merged.Columns).Sum();
                    }
                    var top = origin.Y + (row - firstRow) * rowHeight;
                    return new SpreadsheetCellHit<string>(
                        SpreadsheetGrid.A1(row, column), row, column, new Rectangle(left, top, width, rowHeight));
                }
                x += columnWidths[index];
            }
            return null;
        }
    }
}
