import ApplicationServices
import AppKit
import CoreGraphics
import Foundation
import SQLite3

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
        let persisted = TCCAuthorizationStore.current
        return PermissionDiagnostics(
            accessibilityTrusted: permissionGranted(
                persisted: persisted.accessibility,
                runtime: AXIsProcessTrusted()
            ),
            eventPostingGranted: permissionGranted(
                persisted: persisted.eventPosting,
                runtime: CGPreflightPostEventAccess()
            ),
            screenCaptureGranted: permissionGranted(
                persisted: persisted.screenRecording,
                runtime: CGPreflightScreenCaptureAccess()
            )
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
        "com.microsoft.computer-use.provider.v2"

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

func tccAuthorizationGranted(authValues: [Int32?]) -> Bool {
    authValues.contains(2)
}

func permissionGranted(persisted: Bool?, runtime: Bool) -> Bool {
    persisted == true || runtime
}

private struct TCCAuthorizationStore {
    let accessibility: Bool?
    let eventPosting: Bool?
    let screenRecording: Bool?

    static var current: TCCAuthorizationStore {
        let database = TCCDatabase(
            path: "/Library/Application Support/com.apple.TCC/TCC.db"
        )
        let clients = PermissionSupport.currentPermissionClients()
        return TCCAuthorizationStore(
            accessibility: database.authorization(
                for: .accessibility,
                clients: clients
            ),
            eventPosting: database.authorization(
                for: .eventPosting,
                clients: clients
            ),
            screenRecording: database.authorization(
                for: .screenRecording,
                clients: clients
            )
        )
    }
}

private struct TCCDatabase {
    enum Service: String {
        case accessibility = "kTCCServiceAccessibility"
        case eventPosting = "kTCCServicePostEvent"
        case screenRecording = "kTCCServiceScreenCapture"
    }

    private let path: String
    private let sqliteTransient = unsafeBitCast(
        -1,
        to: sqlite3_destructor_type.self
    )

    init(path: String) {
        self.path = path
    }

    func authorization(
        for service: Service,
        clients: [PermissionClientRecord]
    ) -> Bool? {
        var database: OpaquePointer?
        guard sqlite3_open_v2(
            path,
            &database,
            SQLITE_OPEN_READONLY,
            nil
        ) == SQLITE_OK else {
            if database != nil {
                sqlite3_close(database)
            }
            return nil
        }
        defer { sqlite3_close(database) }

        let query = """
        SELECT auth_value
        FROM access
        WHERE service = ? AND client = ? AND client_type = ?
        ORDER BY last_modified DESC
        LIMIT 1;
        """
        var values: [Int32?] = []
        for client in clients {
            var statement: OpaquePointer?
            guard sqlite3_prepare_v2(
                database,
                query,
                -1,
                &statement,
                nil
            ) == SQLITE_OK else {
                if statement != nil {
                    sqlite3_finalize(statement)
                }
                return nil
            }
            sqlite3_bind_text(
                statement,
                1,
                service.rawValue,
                -1,
                sqliteTransient
            )
            sqlite3_bind_text(
                statement,
                2,
                client.identifier,
                -1,
                sqliteTransient
            )
            sqlite3_bind_int(statement, 3, client.type)
            if sqlite3_step(statement) == SQLITE_ROW {
                values.append(sqlite3_column_int(statement, 0))
            }
            sqlite3_finalize(statement)
        }
        return tccAuthorizationGranted(authValues: values)
    }
}
