@testable import ComputerUseProviderKit
import CoreGraphics
import Testing

private func layout(
    title: String = "Document",
    x: Int = 10,
    activeWindow: String = "42"
) -> [String: Any] {
    [
        "desktop": ["x": 0, "y": 0, "width": 1920, "height": 1080],
        "activeWindow": activeWindow,
        "windows": [[
            "id": "42",
            "pid": 100,
            "title": title,
            "x": x,
            "y": 20,
            "width": 800,
            "height": 600,
        ]],
    ]
}

@Test
func guiFrameIgnoresWindowTitleChanges() {
    #expect(
        desktopLayoutFrameID(layout(title: "Document A"))
            == desktopLayoutFrameID(layout(title: "Document B"))
    )
}

@Test
func guiFrameChangesWithWindowGeometry() {
    #expect(
        desktopLayoutFrameID(layout(x: 10))
            != desktopLayoutFrameID(layout(x: 11))
    )
}

@Test
func guiFrameChangesWithActiveWindow() {
    #expect(
        desktopLayoutFrameID(layout(activeWindow: "42"))
            != desktopLayoutFrameID(layout(activeWindow: "84"))
    )
}

@Test
func gui1080pDesktopIsDeliveredAtNativeResolution() {
    let delivered = desktopDeliveredBounds(
        CGRect(x: 0, y: 0, width: 1920, height: 1080)
    )

    #expect(delivered == CGRect(x: 0, y: 0, width: 1920, height: 1080))
}

@Test
func guiLargerDesktopIsAspectBoundedTo1080p() {
    let delivered = desktopDeliveredBounds(
        CGRect(x: 0, y: 0, width: 2560, height: 1440)
    )

    #expect(delivered == CGRect(x: 0, y: 0, width: 1920, height: 1080))
}

@Test
func guiHeightLimitIsIndependentFromWidthLimit() {
    let delivered = desktopDeliveredBounds(
        CGRect(x: 0, y: 0, width: 1920, height: 1200)
    )

    #expect(delivered == CGRect(x: 0, y: 0, width: 1728, height: 1080))
}

@Test
func guiNativeResolutionCoordinatesMapOneToOneWithDesktopOrigin() {
    let native = CGRect(x: -1920, y: -100, width: 1920, height: 1080)
    let delivered = desktopDeliveredBounds(native)
    let point = guiPoint(
        ["x": 959.5, "y": 539.5],
        deliveredBounds: delivered,
        nativeBounds: native
    )

    #expect(point.x == -960.5)
    #expect(point.y == 439.5)
}

@Test
func guiBoundedCoordinatesMapByPixelCenter() {
    let native = CGRect(x: 0, y: 0, width: 2560, height: 1440)
    let delivered = desktopDeliveredBounds(native)
    let point = guiPoint(
        ["x": 959.5, "y": 539.5],
        deliveredBounds: delivered,
        nativeBounds: native
    )

    #expect(point.x == 1279.5)
    #expect(point.y == 719.5)
}
