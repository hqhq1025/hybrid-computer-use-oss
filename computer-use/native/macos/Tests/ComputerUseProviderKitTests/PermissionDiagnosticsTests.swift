@testable import ComputerUseProviderKit
import Testing

@Test(arguments: 0..<8)
func permissionDiagnosticsPreserveEachRuntimeResult(
    grantedMask: Int
) {
    let accessibility = grantedMask & 1 != 0
    let eventPosting = grantedMask & 2 != 0
    let screenCapture = grantedMask & 4 != 0
    var probes: [SystemPermissionKind] = []
    let diagnostics = PermissionDiagnostics.current(
        accessibility: {
            probes.append(.accessibility)
            return accessibility
        },
        eventPosting: {
            probes.append(.eventPosting)
            return eventPosting
        },
        screenCapture: {
            probes.append(.screenRecording)
            return screenCapture
        }
    )

    #expect(diagnostics.accessibilityTrusted == accessibility)
    #expect(diagnostics.eventPostingGranted == eventPosting)
    #expect(diagnostics.screenCaptureGranted == screenCapture)
    #expect(probes == SystemPermissionKind.allCases)
    #expect(diagnostics.missingPermissions.contains(.accessibility) == !accessibility)
    #expect(diagnostics.missingPermissions.contains(.eventPosting) == !eventPosting)
    #expect(diagnostics.missingPermissions.contains(.screenRecording) == !screenCapture)
}

@Test
func permissionDiagnosticsDoNotRetainAnEarlierGrant() {
    var runtimeGranted = true
    let probe = { runtimeGranted }
    let granted = PermissionDiagnostics.current(
        accessibility: probe,
        eventPosting: probe,
        screenCapture: probe
    )
    #expect(granted.missingPermissions.isEmpty)

    runtimeGranted = false
    let revoked = PermissionDiagnostics.current(
        accessibility: probe,
        eventPosting: probe,
        screenCapture: probe
    )
    #expect(revoked.missingPermissions == SystemPermissionKind.allCases)

    runtimeGranted = true
    let restored = PermissionDiagnostics.current(
        accessibility: probe,
        eventPosting: probe,
        screenCapture: probe
    )
    #expect(restored.missingPermissions.isEmpty)
}
