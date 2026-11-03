// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "Assist",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Assist",
            path: "Sources/Assist",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "AssistTests",
            dependencies: ["Assist"],
            path: "Tests/AssistTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
