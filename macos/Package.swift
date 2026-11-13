// swift-tools-version: 6.0
// The gh-hud Mac app: a menu bar client of the hub (see PLAN-mac.md).
// Builds with the Command Line Tools alone; `make mac` (repo root) wraps
// it in an app bundle.
import PackageDescription

let package = Package(
    name: "GHHud",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "GHHud", targets: ["GHHud"]),
    ],
    dependencies: [
        // Keep in step with the Swift plugins in buf.gen.yaml.
        .package(url: "https://github.com/connectrpc/connect-swift", exact: "1.2.3"),
        .package(url: "https://github.com/apple/swift-protobuf", exact: "1.40.0"),
    ],
    targets: [
        // The hub client and the logic the UI shows, without UI.
        .target(
            name: "GHHudKit",
            dependencies: [
                .product(name: "Connect", package: "connect-swift"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ]
        ),
        // The menu bar app.
        .executableTarget(name: "GHHud", dependencies: ["GHHudKit"]),
        // GHHudKit's checks. XCTest and Swift Testing need Xcode, and the
        // app must build without it, so they run as `swift run GHHudChecks`.
        .executableTarget(name: "GHHudChecks", dependencies: ["GHHudKit"]),
    ]
)
