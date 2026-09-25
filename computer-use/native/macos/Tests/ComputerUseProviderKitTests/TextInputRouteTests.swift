@testable import ComputerUseProviderKit
import Testing

@Test
func literalTypeUsesGlobalPasteRoute() {
    let selected = ComputerUseService.textInputRoute()
    #expect(selected.route == "input.pasteboard.global_command_v")
    #expect(selected.fallbackUsed == false)
}
