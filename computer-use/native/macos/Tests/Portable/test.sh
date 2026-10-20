#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
stage="$(mktemp -d)"
trap "rm -rf \"${stage}\"" EXIT
mkdir -p "${stage}/Sources/ComputerUseProviderKit" "${stage}/Tests/TimingTests"
cp "${root}/Tests/Portable/Package.swift" "${stage}/Package.swift"
for source in Errors GUIStillness AXTextSelection; do
  cp "${root}/Sources/ComputerUseProviderKit/${source}.swift" "${stage}/Sources/ComputerUseProviderKit/"
done
# Selection logic depends only on the production error evidence types.
# Extract those declarations without compiling the platform event emitter.
awk '
  BEGIN { print "import Foundation" }
  /^protocol InputEmissionReportingError/ { include = 1 }
  /^struct PasteboardDataSnapshot/ { include = 0 }
  include { print }
' "${root}/Sources/ComputerUseProviderKit/InputSimulation.swift" \
  > "${stage}/Sources/ComputerUseProviderKit/InputDeliveryError.swift"
# Extract the actual production constant; no macOS SDK or capture stub is run.
awk "/^import Foundation/ || /^let screenshotCaptureTimeout:/" \
  "${root}/Sources/ComputerUseProviderKit/AccessibilitySnapshot.swift" \
  > "${stage}/Sources/ComputerUseProviderKit/CaptureTimeout.swift"
cp "${root}/Tests/ComputerUseProviderKitTests/GUIStillnessTimingTests.swift" "${stage}/Tests/TimingTests/"
cp "${root}/Tests/ComputerUseProviderKitTests/AXTextSelectionTests.swift" "${stage}/Tests/TimingTests/"
swift test --package-path "${stage}" --scratch-path "${stage}/.build" --disable-sandbox
