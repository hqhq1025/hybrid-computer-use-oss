import ApplicationServices
import AppKit
import CoreGraphics
import Foundation

public enum SystemPermissionKind: String, CaseIterable, Sendable {
    case accessibility
    case eventPosting
    case screenRecording
}

public struct PermissionDiagnostics: Sendable {
    public let accessibilityTrusted: Bool
    public let eventPostingGranted: Bool
    public let screenCaptureGranted: Bool

    public static func current() -> PermissionDiagnostics {
        current(
            accessibility: AXIsProcessTrusted,
            eventPosting: CGPreflightPostEventAccess,
            screenCapture: CGPreflightScreenCaptureAccess
        )
    }

    static func current(
        accessibility: () -> Bool,
        eventPosting: () -> Bool,
        screenCapture: () -> Bool
    ) -> PermissionDiagnostics {
        // TCC rows can outlive the executable's signing identity. Only the
        // non-prompting runtime checks establish this process's permissions.
        PermissionDiagnostics(
            accessibilityTrusted: accessibility(),
            eventPostingGranted: eventPosting(),
            screenCaptureGranted: screenCapture()
        )
    }

    public var missingPermissions: [SystemPermissionKind] {
        SystemPermissionKind.allCases.filter { permission in
            switch permission {
            case .accessibility:
                return !accessibilityTrusted
            case .eventPosting:
                return !eventPostingGranted
            case .screenRecording:
                return !screenCaptureGranted
            }
        }
    }
}

public enum PermissionSupport {
    public static let bundleDisplayName = "Computer Use Provider"
    public static let providerBundleIdentifier =
        "org.computer-use.provider.v2"

    public static func currentPermissionClients(
        bundle: Bundle = .main
    ) -> [PermissionClientRecord] {
        var records = [
            PermissionClientRecord(
                identifier: providerBundleIdentifier,
                type: 0
            ),
        ]
        if bundle.bundleURL.pathExtension == "app" {
            records.append(
                PermissionClientRecord(
                    identifier: bundle.bundleURL.standardizedFileURL.path,
                    type: 1
                )
            )
        }
        return records
    }

    public static func requestAccessibilityPrompt() {
        let options: NSDictionary = ["AXTrustedCheckOptionPrompt": true]
        _ = AXIsProcessTrustedWithOptions(options)
    }
}

public struct PermissionClientRecord: Sendable, Equatable, Hashable {
    public let identifier: String
    public let type: Int32
}

