@testable import ComputerUseProviderKit
import Foundation
import Testing

private func sharedProviderFixture(_ name: String) throws -> [String: Any] {
    var root = URL(fileURLWithPath: #filePath)
    for _ in 0..<5 {
        root.deleteLastPathComponent()
    }
    let file = root.appendingPathComponent("testdata/provider-wire/\(name)")
    return try #require(
        JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any]
    )
}

@Test
func sharedProviderRequestsPassNativeValidation() throws {
    for manifestName in ["manifest.json", "native-manifest.json"] {
        let manifest = try sharedProviderFixture(manifestName)
        #expect(manifest["protocol"] as? String == "computer-use-provider")
        #expect(manifest["schema_version"] as? Int == 2)
        if manifestName == "native-manifest.json" {
            let platforms = try #require(manifest["ordered_input_platforms"] as? [String: Bool])
            #expect(platforms["darwin"] == true)
        }
        let fixtures = try #require(manifest["fixtures"] as? [[String: Any]])
        for fixture in fixtures where fixture["direction"] as? String == "request" {
            let file = try #require(fixture["file"] as? String)
            try ProviderRequestValidator.validate(sharedProviderFixture(file))
        }
    }
}

@Test
func sharedOptionalResponsesPreserveNativeWireContract() async throws {
    let manifest = try sharedProviderFixture("native-manifest.json")
    let fixtures = try #require(manifest["fixtures"] as? [[String: Any]])
    var responseCount = 0
    for fixture in fixtures where fixture["direction"] as? String == "response" {
        let file = try #require(fixture["file"] as? String)
        let requestFile = try #require(fixture["request_file"] as? String)
        var expected = try sharedProviderFixture(file)
        let request = try sharedProviderFixture(requestFile)
        let payload = try #require(request["ordered_input"] as? [String: Any])
        var runtimeResult = expected["ordered_input"] as? [String: Any] ?? [:]
        if let reset = expected["provider_reset_required"] {
            runtimeResult["providerResetRequired"] = reset
        }
        var dispatched: [[String: Any]] = []
        let server = ProviderServer(
            transport: ProviderLineTransport(inputDescriptor: -1, outputDescriptor: -1),
            executeRuntime: { operation in
                dispatched.append(operation)
                return runtimeResult
            }
        )
        let line = String(decoding: try JSONSerialization.data(withJSONObject: request), as: UTF8.self)
        let response = await server.handle(line: line)
        if var rejection = expected["request_rejection"] as? [String: Any] {
            #expect(dispatched.isEmpty)
            do {
                try ProviderRequestValidator.validate(request)
                Issue.record("Expected shared invalid request to fail validation")
            } catch {
                let message = (error as? LocalizedError)?.errorDescription ?? String(describing: error)
                #expect(message.contains("final_observation.capture_snapshot"))
                // Error wording is platform-specific; all typed facts must agree.
                rejection["message"] = message
                expected["request_rejection"] = rejection
            }
        } else {
            try #require(dispatched.count == 1)
            let operation = dispatched[0]
            #expect(operation["tool"] as? String == "ordered_keypress")
            #expect(operation["_diagnostic_request_id"] as? String == request["request_id"] as? String)
            let adapted = try #require(operation["orderedInput"] as? [String: Any])
            #expect(NSDictionary(dictionary: adapted).isEqual(to: payload))
        }
        #expect(NSDictionary(dictionary: response).isEqual(to: expected))
        responseCount += 1
    }
    #expect(responseCount == 4)
}
