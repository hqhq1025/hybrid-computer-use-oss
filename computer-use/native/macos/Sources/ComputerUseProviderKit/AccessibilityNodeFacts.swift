import ApplicationServices
import Foundation

/// Attributes one snapshot render reads from each node. A single
/// AXUIElementCopyMultipleAttributeValues request per node replaces the
/// separate reads the renderer helpers make; any other attribute falls back
/// to one single-attribute read.
let snapshotNodeAttributes: [String] = [
    kAXRoleAttribute, kAXSubroleAttribute, kAXRoleDescriptionAttribute,
    kAXTitleAttribute, kAXDescriptionAttribute, kAXHelpAttribute,
    kAXIdentifierAttribute, kAXValueAttribute, kAXEnabledAttribute,
    kAXSelectedAttribute, kAXExpandedAttribute, kAXPositionAttribute,
    kAXSizeAttribute, kAXChildrenAttribute, kAXRowsAttribute,
    axContentsAttribute, axVisibleChildrenAttribute, kAXURLAttribute,
    "AXPlaceholderValue", "AXPlaceholder",
]

struct AXRead {
    let status: AXError
    let value: CFTypeRef?
}

func singleAttributeRead(_ element: AXUIElement, _ attribute: String) -> AXRead {
    var value: CFTypeRef?
    let status = AXUIElementCopyAttributeValue(element, attribute as CFString, &value)
    return AXRead(status: status, value: value)
}

/// Returns one read per attribute, or nil when the request as a whole fails.
func multipleAttributeRead(_ element: AXUIElement, _ attributes: [String]) -> [AXRead]? {
    var values: CFArray?
    let status = AXUIElementCopyMultipleAttributeValues(
        element, attributes as CFArray, [], &values
    )
    guard status == .success,
          let raw = values as? [AnyObject],
          raw.count == attributes.count else {
        return nil
    }
    return raw.map(decodeBatchedAttribute)
}

/// The batched request reports a per-attribute failure as an AXValue of type
/// axError where a single read would return that error status.
func decodeBatchedAttribute(_ value: AnyObject) -> AXRead {
    if value is NSNull {
        return AXRead(status: .noValue, value: nil)
    }
    let reference = value as CFTypeRef
    if let axValue = checkedAXValue(reference), AXValueGetType(axValue) == .axError {
        var error = AXError.success
        AXValueGetValue(axValue, .axError, &error)
        return AXRead(status: error, value: nil)
    }
    return AXRead(status: .success, value: reference)
}

/// Per-snapshot AX read cache. The renderer reads a node, its children and
/// its ancestors several times; inside one snapshot every repeated read
/// returns the first sample instead of another IPC round trip. The cache
/// exists only while `withSnapshotScope` runs, so no sample outlives the
/// snapshot that took it.
final class AXNodeFactsCache {
    private struct Key: Hashable {
        let element: AXUIElement

        static func == (lhs: Key, rhs: Key) -> Bool {
            CFEqual(lhs.element, rhs.element)
        }

        func hash(into hasher: inout Hasher) {
            hasher.combine(CFHash(element))
        }
    }

    private final class Node {
        var attributes: [String: AXRead] = [:]
        var names: AXRead?
        var actions: AXRead?
        var settable: [String: Bool] = [:]
    }

    private static let threadKey = "computer-use.ax-node-facts"
    private var nodes: [Key: Node] = [:]
    private let attributes: [String]
    private let multiRead: (AXUIElement, [String]) -> [AXRead]?
    private let singleRead: (AXUIElement, String) -> AXRead
    private let settableRead: (AXUIElement, String) -> Bool

    init(
        attributes: [String] = snapshotNodeAttributes,
        multiRead: @escaping (AXUIElement, [String]) -> [AXRead]? = multipleAttributeRead,
        singleRead: @escaping (AXUIElement, String) -> AXRead = singleAttributeRead,
        settableRead: @escaping (AXUIElement, String) -> Bool = { element, attribute in
            var settable = DarwinBoolean(false)
            let status = AXUIElementIsAttributeSettable(element, attribute as CFString, &settable)
            return status == .success && settable.boolValue
        }
    ) {
        self.attributes = attributes
        self.multiRead = multiRead
        self.singleRead = singleRead
        self.settableRead = settableRead
    }

    static var current: AXNodeFactsCache? {
        Thread.current.threadDictionary[threadKey] as? AXNodeFactsCache
    }

    /// Runs `body` with a cache on this thread; a nested scope shares the
    /// outer cache.
    static func withSnapshotScope<T>(_ body: () throws -> T) rethrows -> T {
        let dictionary = Thread.current.threadDictionary
        let previous = dictionary[threadKey]
        dictionary[threadKey] = previous ?? AXNodeFactsCache()
        defer { dictionary[threadKey] = previous }
        return try body()
    }

    private func node(_ element: AXUIElement) -> Node {
        let key = Key(element: element)
        if let node = nodes[key] {
            return node
        }
        let node = Node()
        if let reads = multiRead(element, attributes), reads.count == attributes.count {
            for (attribute, read) in zip(attributes, reads) {
                node.attributes[attribute] = read
            }
        }
        nodes[key] = node
        return node
    }

    func attribute(_ element: AXUIElement, _ attribute: String) -> AXRead {
        let node = node(element)
        if let read = node.attributes[attribute] {
            return read
        }
        let read = singleRead(element, attribute)
        node.attributes[attribute] = read
        return read
    }

    func attributeNames(_ element: AXUIElement) -> AXRead {
        let node = node(element)
        if let names = node.names {
            return names
        }
        var names: CFArray?
        let status = AXUIElementCopyAttributeNames(element, &names)
        let read = AXRead(status: status, value: names)
        node.names = read
        return read
    }

    func actionNames(_ element: AXUIElement) -> AXRead {
        let node = node(element)
        if let actions = node.actions {
            return actions
        }
        var actions: CFArray?
        let status = AXUIElementCopyActionNames(element, &actions)
        let read = AXRead(status: status, value: actions)
        node.actions = read
        return read
    }

    func isSettable(_ element: AXUIElement, _ attribute: String) -> Bool {
        let node = node(element)
        if let settable = node.settable[attribute] {
            return settable
        }
        // An attribute the element does not support cannot be set.
        let settable = node.attributes[attribute]?.status == .attributeUnsupported
            ? false
            : settableRead(element, attribute)
        node.settable[attribute] = settable
        return settable
    }
}

func readAttribute(_ element: AXUIElement, _ attribute: String) -> AXRead {
    AXNodeFactsCache.current?.attribute(element, attribute)
        ?? singleAttributeRead(element, attribute)
}
