// swift-tools-version:5.9
// SPDX-License-Identifier: GPL-2.0-only
//
// avprobe plays the QA matrix stream URLs through macOS AVFoundation and
// reports what an AVPlayer client would see. It is the automated pre-TV
// probe for docs/qa/atv-matrix.md. It needs no signing and no keychain:
//
//   swift build -c release
//   printf '%s' "$KEY" | .build/release/avprobe --plan plan.json --out results.json
//
// The key is read from stdin and never printed; URLs in the output have
// their query strings removed.

import PackageDescription

let package = Package(
    name: "avprobe",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "avprobe",
            path: "Sources/avprobe"
        ),
    ]
)
