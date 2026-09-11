#!/bin/sh
# Build an explicitly named local correction without activating it or replacing
# the official release. Requires Go, a C compiler for race tests, and patch.
set -eu
umask 077

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=${1:-"$ROOT/.tools/mediamtx-v1.21.0-clockfix1"}
case "$OUTPUT" in /*) ;; *) OUTPUT="$ROOT/$OUTPUT" ;; esac
if [ -e "$OUTPUT" ]; then
  echo "Output already exists; choose a new directory as the first argument: $OUTPUT" >&2
  exit 1
fi
export GOTOOLCHAIN=go1.26.8
VERSION=v1.21.0-clockfix1
GOSRT_MODULE=github.com/datarhei/gosrt
GOSRT_VERSION=v0.11.1-0.20260812091715-a77b40bb4b76
GOSRT_SUM='h1:fwEWQ7L/p952NbtyIKPHzLkMGzIzOa29ivQyTQgye5Q='
GOSRT_COMMIT=a77b40bb4b76b9d1018fa41c6a7fa6ed34af95bf
MTX_MODULE=github.com/bluenviron/mediamtx
MTX_VERSION=v1.21.0
MTX_SUM='h1:0Js3+vV0yyaFszram8LvHeRRr5PLGoF5SoCxpXzo31E='
MTX_COMMIT=2c6727904fbf233615de74a6c54a9b94dbf6025d
PATCH="$ROOT/patches/gosrt-a77b40bb4b76-receive-clock.patch"
TEST="$ROOT/patches/gosrt_receive_clock_test.go.txt"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
[ "$(sha256_file "$PATCH")" = e9b38dfe08f21834c36a74260092d9f75274103a26f8d23cf6b5cc8c725bbe3b ] || { echo 'Patch checksum mismatch' >&2; exit 1; }
[ "$(sha256_file "$TEST")" = 149c4568aafd2c62cc1bf16070f88364c5d1a20b6fc1bf633b3fe47d82d5bf4b ] || { echo 'Test checksum mismatch' >&2; exit 1; }

download_pinned() {
  metadata=$(go mod download -json "$1@$2")
  case "$metadata" in *'"Sum": "'"$3"'"'*) ;; *) echo "Module checksum mismatch: $1" >&2; exit 1 ;; esac
  case "$metadata" in *'"Hash": "'"$4"'"'*) ;; *) echo "Module commit mismatch: $1" >&2; exit 1 ;; esac
}
download_pinned "$GOSRT_MODULE" "$GOSRT_VERSION" "$GOSRT_SUM" "$GOSRT_COMMIT"
download_pinned "$MTX_MODULE" "$MTX_VERSION" "$MTX_SUM" "$MTX_COMMIT"
CACHE=$(go env GOMODCACHE)
mkdir -p "$ROOT/.tools/source"
STAGE=$(mktemp -d "$ROOT/.tools/source/clockfix1.XXXXXX")
echo "Keeping reproducible source and test evidence in $STAGE"
mkdir "$STAGE/gosrt" "$STAGE/mediamtx"
cp -R "$CACHE/$GOSRT_MODULE@$GOSRT_VERSION/." "$STAGE/gosrt/"
cp -R "$CACHE/$MTX_MODULE@$MTX_VERSION/." "$STAGE/mediamtx/"
chmod -R u+rwX "$STAGE"
patch -d "$STAGE/gosrt" -p1 -N -t -F 0 -i "$PATCH"
cp "$TEST" "$STAGE/gosrt/receive_clock_test.go"
(
  cd "$STAGE/gosrt"
  CGO_ENABLED=1 go test -race -count=1 -run '^TestReceiveClockIndependentOfPeerConnectionAge$' -timeout 30s .
) >"$STAGE/receive-clock-tests.log" 2>&1 || { cat "$STAGE/receive-clock-tests.log"; exit 1; }
cat "$STAGE/receive-clock-tests.log"
(
  cd "$STAGE/mediamtx"
  go mod edit -replace github.com/datarhei/gosrt=../gosrt
  printf '%s' "$VERSION" >internal/core/VERSION
  # This upstream generator verifies the downloaded hls.js archive hash.
  go generate ./internal/servers/hls
  case "$(go env GOOS)/$(go env GOARCH)" in
    linux/arm|linux/arm64) go generate ./internal/staticsources/rpicamera ;;
  esac
  CGO_ENABLED=0 go build -trimpath -o "$STAGE/mediamtx-bin" .
)
[ "$("$STAGE/mediamtx-bin" --version)" = "$VERSION" ] || { echo 'Built version mismatch' >&2; exit 1; }
mkdir -p "$(dirname "$OUTPUT")"
mkdir "$OUTPUT"
cp "$STAGE/mediamtx-bin" "$OUTPUT/mediamtx"
cp "$STAGE/mediamtx/LICENSE" "$OUTPUT/LICENSE"
cp "$STAGE/gosrt/LICENSE" "$OUTPUT/LICENSE-gosrt"
cat >"$OUTPUT/BUILD-INFO.txt" <<EOF
Version: $VERSION
MediaMTX module: $MTX_MODULE@$MTX_VERSION
MediaMTX commit: $MTX_COMMIT
MediaMTX module checksum: $MTX_SUM
GoSRT module: $GOSRT_MODULE@$GOSRT_VERSION
GoSRT commit: $GOSRT_COMMIT
GoSRT module checksum: $GOSRT_SUM
Patch SHA256: $(sha256_file "$PATCH")
Tests SHA256: $(sha256_file "$TEST")
Binary SHA256: $(sha256_file "$OUTPUT/mediamtx")
Toolchain: $(go version)
Build: CGO_ENABLED=0 go build -trimpath
Source and focused race-test log: $STAGE
Status: built only; not activated
EOF
echo "Built $OUTPUT/mediamtx ($VERSION). The active receiver is unchanged."
