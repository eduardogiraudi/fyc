#!/bin/bash
set -e

# Read version from file (fallback to "dev" if missing)
VERSION="dev"
if [ -f version ]; then
  VERSION=$(cat version | tr -d '[:space:]')
fi

# Build directory
BUILD_DIR="${BUILD_DIR:-.}"

# Create build output directory
BUILD_OUTPUT="$BUILD_DIR/build"
mkdir -p "$BUILD_OUTPUT"

echo "Building fuckyourcli..."

# Resolve over HTTP without modifying the tracked release fallback.
# Run the helper on the host even when GOOS/GOARCH select a cross build.
CODEX_VERSION=$(GOOS= GOARCH= CGO_ENABLED=0 go run -buildvcs=false ./cmd/update-codex-version)

# Compile with Go
go build -buildvcs=false -ldflags="-X main.version=${VERSION} -X fuckyourcli/codexversion.BuildVersion=${CODEX_VERSION}" -o "$BUILD_OUTPUT/fuckyourcli" .

echo "Build complete!"
echo "Output location: $BUILD_OUTPUT/fuckyourcli"
echo ""
echo "Usage from any directory:"
echo "  $BUILD_OUTPUT/fuckyourcli"
