#!/bin/sh
# Builds den for each kind of host install.sh supports, with their
# checksums, into dist/. Attach the files to a GitHub release.
#
#   scripts/release.sh v0.1.0
set -eu
cd "$(dirname "$0")/.."
version=${1:-dev}

rm -rf dist
mkdir dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os=${target%/*}
	arch=${target#*/}
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w -X main.version=$version" -o "dist/den-$os-$arch" ./cmd/den
done

cd dist
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum den-* >SHA256SUMS
else
	shasum -a 256 den-* >SHA256SUMS
fi
cat SHA256SUMS
