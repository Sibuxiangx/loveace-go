#!/bin/sh
# Fetch the onnxruntime shared library into third_party/ (gitignored).
#
# core/captcha (onnxruntime_go) dlopens the ORT 1.22.0 shared library; the
# binary blobs never enter the repository — a fresh clone runs this once:
#
#   scripts/fetch_onnxruntime.sh
#
# Production images get the linux-x64 lib baked in at build time (see
# deploy/Containerfile); local dev/tests resolve it via
# core/captcha.bundledLibPath (third_party/onnxruntime/<platform>/lib/).
set -eu

VERSION=1.22.0
ROOT="$(cd "$(dirname "$0")/.." && pwd)/third_party/onnxruntime"

case "$(uname -s)-$(uname -m)" in
	Darwin-arm64)
		pkg="onnxruntime-osx-arm64-$VERSION"; dest="osx-arm64"; lib="libonnxruntime.dylib" ;;
	Linux-x86_64)
		pkg="onnxruntime-linux-x64-$VERSION"; dest="linux-x64"; lib="libonnxruntime.so" ;;
	*)
		echo "fetch_onnxruntime.sh: unsupported platform $(uname -s)-$(uname -m)" >&2
		echo "(needed: darwin/arm64 for local dev, linux/amd64 for the image)" >&2
		exit 1 ;;
esac

if [ -f "$ROOT/$dest/lib/$lib" ]; then
	echo "onnxruntime $dest already present ($ROOT/$dest/lib/$lib)"
	exit 0
fi

echo "fetching onnxruntime $VERSION ($dest)…"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# GitHub releases over a possibly-flaky link: try twice.
curl -fL --retry 2 --retry-delay 3 \
	"https://github.com/microsoft/onnxruntime/releases/download/v$VERSION/$pkg.tgz" \
	-o "$tmp/$pkg.tgz"
tar -xzf "$tmp/$pkg.tgz" -C "$tmp"

mkdir -p "$ROOT/$dest/lib"
cp "$tmp/$pkg/lib/$lib" "$ROOT/$dest/lib/$lib"
echo "installed: $ROOT/$dest/lib/$lib"
