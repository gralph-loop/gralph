#!/usr/bin/env sh
# Build gralph for all release platforms into dist/.
# Artifacts: dist/gralph-<os>-<arch>[.exe]
# Convenience copies for local use: dist/gralph (native), dist/gralph.exe (windows, host arch).
set -e
cd "$(dirname "$0")"

PLATFORMS="linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64"

# Version to stamp into the binary: $VERSION env var (set by CI from the git
# tag) or the exact tag of the checked-out commit, if any.
VERSION="${VERSION:-$(git describe --tags --exact-match 2>/dev/null || true)}"
LDFLAGS=""
[ -n "$VERSION" ] && LDFLAGS="-X main.version=$VERSION"
echo "[build] version: ${VERSION:-(none)}"

mkdir -p dist
for p in $PLATFORMS; do
  os=${p%/*}
  arch=${p#*/}
  out="dist/gralph-$os-$arch"
  [ "$os" = "windows" ] && out="$out.exe"
  echo "[build] $os/$arch"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$out" .
done

host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
suffix=""
[ "$host_os" = "windows" ] && suffix=".exe"
cp "dist/gralph-$host_os-$host_arch$suffix" "dist/gralph$suffix"
cp "dist/gralph-windows-$host_arch.exe" dist/gralph.exe
echo "[build] done:"
ls -1 dist/gralph*
