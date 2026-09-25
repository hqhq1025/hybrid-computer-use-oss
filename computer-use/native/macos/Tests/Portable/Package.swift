// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "GUIStillnessChecks",
    targets: [
        .target(name: "ComputerUseProviderKit"),
        .testTarget(name: "TimingTests", dependencies: ["ComputerUseProviderKit"]),
    ]
)
