import AppKit
import CoreServices
import Darwin
import Foundation

public struct RunningAppDescriptor: Sendable {
    public let name: String
    public let bundleIdentifier: String?
    public let pid: pid_t
    public let runningApplication: NSRunningApplication
}

struct ListedAppDescriptor {
    let name: String
    let bundleIdentifier: String
    let isRunning: Bool
    let isFrontmost: Bool
    let lastUsed: Date?
    let uses: Int?

    var renderedLine: String {
        var markers: [String] = []
        if isFrontmost {
            markers.append("frontmost")
        }
        if isRunning {
            markers.append("running")
        }
        if let lastUsed {
            markers.append("last-used=\(AppDiscovery.usageDateFormatter.string(from: lastUsed))")
        }
        if let uses {
            markers.append("uses=\(uses)")
        }

        return "\(name) — \(bundleIdentifier) [\(markers.joined(separator: ", "))]"
    }
}

private struct SpotlightAppRecord {
    let name: String
    let bundleIdentifier: String
    let lastUsed: Date?
    let uses: Int?
}

enum AppDiscovery {
    static let internalProviderBundleIdentifiers: Set<String> = [
        "org.computer-use.provider.v2",
    ]
    private static let listAppsQuery = #"kMDItemContentType == "com.apple.application-bundle" && kMDItemFSName == "*.app""#
    private static let lastUsedDateRankingAttribute = "kMDItemLastUsedDate_Ranking"
    private static let useCountAttribute = "kMDItemUseCount"
    private static let maxRecentNonRunningApps = 10

    static let usageDateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter
    }()

    static func listCatalog() -> [ListedAppDescriptor] {
        let running = userFacingRunningApps()
        let frontmostBundleIdentifier = NSWorkspace.shared.frontmostApplication?.bundleIdentifier?.lowercased()
        let runningByBundle = running.reduce(into: [String: RunningAppDescriptor]()) { result, descriptor in
            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                return
            }

            let key = bundleIdentifier.lowercased()
            if result[key] == nil {
                result[key] = descriptor
            }
        }

        var entriesByBundle: [String: ListedAppDescriptor] = [:]

        for record in SpotlightAppIndex.recentApps(cutoffDate: recentUsageCutoff()) {
            let key = record.bundleIdentifier.lowercased()
            guard isDiscoverableBundleIdentifier(key) else {
                continue
            }
            let runningDescriptor = runningByBundle[key]
            entriesByBundle[key] = ListedAppDescriptor(
                name: runningDescriptor?.name ?? record.name,
                bundleIdentifier: record.bundleIdentifier,
                isRunning: runningDescriptor != nil,
                isFrontmost: key == frontmostBundleIdentifier,
                lastUsed: record.lastUsed,
                uses: record.uses
            )
        }

        for descriptor in running {
            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                continue
            }

            let key = bundleIdentifier.lowercased()
            let existing = entriesByBundle[key]
            entriesByBundle[key] = ListedAppDescriptor(
                name: descriptor.name,
                bundleIdentifier: bundleIdentifier,
                isRunning: true,
                isFrontmost: key == frontmostBundleIdentifier,
                lastUsed: existing?.lastUsed,
                uses: existing?.uses
            )
        }

        let sorted = entriesByBundle.values.sorted(by: compareListedApps)
        let runningEntries = sorted.filter(\.isRunning)
        let recentEntries = sorted.filter { !$0.isRunning }.prefix(maxRecentNonRunningApps)
        return runningEntries + recentEntries
    }

    static func runningApps(
        readWorkspace: @MainActor () -> [NSRunningApplication] = {
            NSWorkspace.shared.runningApplications
        }
    ) -> [RunningAppDescriptor] {
        // A worker can retain an older workspace snapshot across app launches
        // and exits. Read and materialize each inventory on the main run loop;
        // only Sendable descriptors leave the main actor.
        onMainActor {
            discoverableRunningApps(readWorkspace())
        }
    }

    /// Runs `body` on the main actor and blocks the caller until it returns.
    /// The main thread must not wait on a caller of this function.
    static func onMainActor<T: Sendable>(_ body: @MainActor () -> T) -> T {
        if Thread.isMainThread {
            return MainActor.assumeIsolated(body)
        }
        return DispatchQueue.main.sync {
            MainActor.assumeIsolated(body)
        }
    }

    private static func discoverableRunningApps(
        _ workspaceApps: [NSRunningApplication]
    ) -> [RunningAppDescriptor] {
        workspaceApps
            .filter {
                isDiscoverableRunningApplication(
                    isTerminated: $0.isTerminated,
                    bundleIdentifier: $0.bundleIdentifier
                ) && !processIsKnownMissing($0.processIdentifier)
            }
            .sorted { lhs, rhs in
                if lhs.isActive != rhs.isActive {
                    return lhs.isActive && !rhs.isActive
                }

                return appName(lhs).localizedCaseInsensitiveCompare(appName(rhs)) == .orderedAscending
            }
            .map { app in
                RunningAppDescriptor(
                    name: appName(app),
                    bundleIdentifier: app.bundleIdentifier,
                    pid: app.processIdentifier,
                    runningApplication: app
                )
            }
    }

    static func resolve(_ query: String) throws -> RunningAppDescriptor {
        let normalizedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        let running = runningApps()

        if let bundleIdentifier = blockedBundleIdentifier(forQuery: normalizedQuery) {
            throw AppSafetyPolicy.permissionDenied(bundleIdentifier: bundleIdentifier)
        }

        if let match = resolvedRunningApp(in: running, matching: normalizedQuery) {
            return match
        }

        // Observation and actions are read-only towards the application
        // list: the provider never launches an application.
        throw ComputerUseError.appNotFound(normalizedQuery)
    }

    private static func resolvedRunningApp(in descriptors: [RunningAppDescriptor], matching query: String) -> RunningAppDescriptor? {
        if isBundleIdentifierQuery(query) {
            return descriptors.first(where: { descriptor in
                descriptor.bundleIdentifier?.caseInsensitiveCompare(query) == .orderedSame
            })
        }

        return descriptors.first(where: { descriptor in
            guard !AppSafetyPolicy.isBlocked(bundleIdentifier: descriptor.bundleIdentifier) else {
                return false
            }

            return descriptor.name.caseInsensitiveCompare(query) == .orderedSame
                || descriptor.runningApplication.executableURL?.deletingPathExtension().lastPathComponent.caseInsensitiveCompare(query) == .orderedSame
        })
    }

    private static func userFacingRunningApps() -> [RunningAppDescriptor] {
        var seen: Set<String> = []
        var descriptors: [RunningAppDescriptor] = []

        for descriptor in runningApps() {
            guard isUserFacingListApp(descriptor.runningApplication) else {
                continue
            }

            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                continue
            }

            let key = bundleIdentifier.lowercased()
            guard seen.insert(key).inserted else {
                continue
            }

            descriptors.append(descriptor)
        }

        return descriptors
    }

    private static func listedBundleIdentifier(for descriptor: RunningAppDescriptor) -> String? {
        guard let bundleIdentifier = descriptor.bundleIdentifier,
              !bundleIdentifier.isEmpty,
              isDiscoverableBundleIdentifier(bundleIdentifier) else {
            return nil
        }
        return bundleIdentifier
    }

    static func isDiscoverableBundleIdentifier(_ bundleIdentifier: String) -> Bool {
        !internalProviderBundleIdentifiers.contains(bundleIdentifier.lowercased())
    }

    static func isDiscoverableRunningApplication(
        isTerminated: Bool,
        bundleIdentifier: String?
    ) -> Bool {
        !isTerminated && bundleIdentifier.map(isDiscoverableBundleIdentifier) != false
    }

    static func processIsKnownMissing(
        _ pid: pid_t,
        probe: (pid_t) -> (result: Int32, error: Int32) = { pid in
            let result = Darwin.kill(pid, 0)
            return (result, errno)
        }
    ) -> Bool {
        // NSWorkspace can retain an exited process until its run loop updates.
        // Only ESRCH proves absence; denied or unavailable probes remain unknown.
        guard pid > 0 else { return false }
        let result = probe(pid)
        return result.result != 0 && result.error == ESRCH
    }

    static func compareListedApps(_ lhs: ListedAppDescriptor, _ rhs: ListedAppDescriptor) -> Bool {
        if lhs.isFrontmost != rhs.isFrontmost {
            return lhs.isFrontmost && !rhs.isFrontmost
        }

        if lhs.isRunning != rhs.isRunning {
            return lhs.isRunning && !rhs.isRunning
        }

        let lhsHasUsage = lhs.lastUsed != nil
        let rhsHasUsage = rhs.lastUsed != nil
        if lhsHasUsage != rhsHasUsage {
            return lhsHasUsage && !rhsHasUsage
        }

        let calendar = Calendar(identifier: .gregorian)
        if let lhsLast = lhs.lastUsed, let rhsLast = rhs.lastUsed {
            let lhsDay = calendar.startOfDay(for: lhsLast)
            let rhsDay = calendar.startOfDay(for: rhsLast)
            if lhsDay != rhsDay {
                return lhsDay > rhsDay
            }
        }

        if let lhsUses = lhs.uses, let rhsUses = rhs.uses, lhsUses != rhsUses {
            return lhsUses > rhsUses
        }

        return lhs.name.localizedCaseInsensitiveCompare(rhs.name) == .orderedAscending
    }

    private static func recentUsageCutoff(referenceDate: Date = Date()) -> Date {
        let calendar = Calendar(identifier: .gregorian)
        let startOfToday = calendar.startOfDay(for: referenceDate)
        return calendar.date(byAdding: .day, value: -13, to: startOfToday) ?? startOfToday
    }

    private static func blockedBundleIdentifier(forQuery query: String) -> String? {
        guard isBundleIdentifierQuery(query), AppSafetyPolicy.isBlocked(bundleIdentifier: query) else {
            return nil
        }

        return query
    }

    private static func isBundleIdentifierQuery(_ query: String) -> Bool {
        query.contains(".")
    }

    private static func isUserFacingListApp(_ app: NSRunningApplication) -> Bool {
        app.activationPolicy == .regular
    }

    private static func bundleDisplayName(_ bundle: Bundle?) -> String? {
        guard let bundle else {
            return nil
        }

        let displayName = bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String
        let bundleName = bundle.object(forInfoDictionaryKey: kCFBundleNameKey as String) as? String
        return displayName ?? bundleName
    }

    private static func stripAppSuffix(from value: String) -> String {
        value.hasSuffix(".app") ? String(value.dropLast(4)) : value
    }

    static func appName(_ app: NSRunningApplication) -> String {
        app.localizedName
            ?? bundleDisplayName(Bundle(url: app.bundleURL ?? URL(fileURLWithPath: "/")))
            ?? app.bundleURL?.deletingPathExtension().lastPathComponent
            ?? app.executableURL?.lastPathComponent
            ?? "pid-\(app.processIdentifier)"
    }

    private enum SpotlightAppIndex {
        static func recentApps(cutoffDate: Date) -> [SpotlightAppRecord] {
            let sortingAttributes = [
                lastUsedDateRankingAttribute as CFString,
                useCountAttribute as CFString,
            ] as CFArray

            guard let query = MDQueryCreate(
                kCFAllocatorDefault,
                listAppsQuery as CFString,
                nil,
                sortingAttributes
            ) else {
                return []
            }

            MDQuerySetSearchScope(query, standardSearchScopes() as CFArray, 0)
            MDQuerySetSortOptionFlagsForAttribute(query, lastUsedDateRankingAttribute as CFString, kMDQueryReverseSortOrderFlag.rawValue)
            MDQuerySetSortOptionFlagsForAttribute(query, useCountAttribute as CFString, kMDQueryReverseSortOrderFlag.rawValue)

            guard MDQueryExecute(query, CFOptionFlags(kMDQuerySynchronous.rawValue)) else {
                return []
            }

            var seen: Set<String> = []
            var records: [SpotlightAppRecord] = []

            for index in 0..<MDQueryGetResultCount(query) {
                guard let rawResult = MDQueryGetResultAtIndex(query, index) else {
                    continue
                }

                let item = unsafeBitCast(rawResult, to: MDItem.self)
                guard
                    let bundleIdentifier = stringAttribute(kMDItemCFBundleIdentifier, item: item),
                    !bundleIdentifier.isEmpty
                else {
                    continue
                }

                let key = bundleIdentifier.lowercased()
                guard seen.insert(key).inserted else {
                    continue
                }

                guard let path = stringAttribute(kMDItemPath, item: item) else {
                    continue
                }

                let appURL = URL(fileURLWithPath: path)
                let bundle = Bundle(url: appURL)
                if bundle?.object(forInfoDictionaryKey: "LSBackgroundOnly") as? Bool == true {
                    continue
                }
                if bundle?.object(forInfoDictionaryKey: "LSUIElement") as? Bool == true {
                    continue
                }

                let lastUsed = dateAttribute(lastUsedDateRankingAttribute as CFString, item: item)
                    ?? dateAttribute(kMDItemLastUsedDate, item: item)
                guard let lastUsed, lastUsed >= cutoffDate else {
                    continue
                }

                let uses = numberAttribute(useCountAttribute as CFString, item: item)?.intValue
                let displayName = bundleDisplayName(bundle)
                    ?? stringAttribute(kMDItemDisplayName, item: item).map(stripAppSuffix(from:))
                    ?? stripAppSuffix(from: appURL.lastPathComponent)

                records.append(
                    SpotlightAppRecord(
                        name: displayName,
                        bundleIdentifier: bundleIdentifier,
                        lastUsed: lastUsed,
                        uses: uses
                    )
                )
            }

            return records
        }

        private static func standardSearchScopes() -> [CFString] {
            var scopes: [String] = [
                "/Applications",
                "/System/Applications",
                "/System/Library/CoreServices",
            ]

            let homeApplications = NSString(string: "~/Applications").expandingTildeInPath
            if FileManager.default.fileExists(atPath: homeApplications) {
                scopes.append(homeApplications)
            }

            return scopes as [CFString]
        }

        private static func stringAttribute(_ name: CFString, item: MDItem) -> String? {
            MDItemCopyAttribute(item, name) as? String
        }

        private static func numberAttribute(_ name: CFString, item: MDItem) -> NSNumber? {
            MDItemCopyAttribute(item, name) as? NSNumber
        }

        private static func dateAttribute(_ name: CFString, item: MDItem) -> Date? {
            MDItemCopyAttribute(item, name) as? Date
        }
    }
}
