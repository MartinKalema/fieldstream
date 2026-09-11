#!/bin/sh
# Isolated research candidate. Does not activate it or change existing sources.
set -eu
umask 077

LAB_BUILD_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
LAB_BUILD_OUTPUT=${1:-"$LAB_BUILD_ROOT/.tools/mediamtx-v1.21.0-clockfix1-lan60"}
case "$LAB_BUILD_OUTPUT" in /*) ;; *) LAB_BUILD_OUTPUT="$LAB_BUILD_ROOT/$LAB_BUILD_OUTPUT" ;; esac
[ "$#" -le 1 ] || { echo 'Usage: scripts/build-mediamtx-lan60.sh [new-output-directory]' >&2; exit 1; }
[ ! -e "$LAB_BUILD_OUTPUT" ] && [ ! -L "$LAB_BUILD_OUTPUT" ] || {
  echo 'Output already exists; choose a new output directory.' >&2; exit 1;
}
export GOTOOLCHAIN=go1.26.8
LAB_BUILD_VERSION=v1.21.0-clockfix1-lan60
LAB_GOSRT_MODULE=github.com/datarhei/gosrt
LAB_GOSRT_VERSION=v0.11.1-0.20260812091715-a77b40bb4b76
LAB_GOSRT_SUM='h1:fwEWQ7L/p952NbtyIKPHzLkMGzIzOa29ivQyTQgye5Q='
LAB_GOSRT_COMMIT=a77b40bb4b76b9d1018fa41c6a7fa6ed34af95bf
LAB_MTX_MODULE=github.com/bluenviron/mediamtx
LAB_MTX_VERSION=v1.21.0
LAB_MTX_SUM='h1:0Js3+vV0yyaFszram8LvHeRRr5PLGoF5SoCxpXzo31E='
LAB_MTX_COMMIT=2c6727904fbf233615de74a6c54a9b94dbf6025d
LAB_CLOCK_PATCH="$LAB_BUILD_ROOT/patches/gosrt-a77b40bb4b76-receive-clock.patch"
LAB_CLOCK_TEST="$LAB_BUILD_ROOT/patches/gosrt_receive_clock_test.go.txt"
LAB_WAIT_PATCH="$LAB_BUILD_ROOT/patches/mediamtx-v1.21.0-receiver-lan60.patch"
LAB_WAIT_TEST="$LAB_BUILD_ROOT/patches/mediamtx_receiver_lan60_test.go.txt"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
check_hash() {
  [ "$(sha256_file "$1")" = "$2" ] || { echo "Checksum mismatch: $1" >&2; exit 1; }
}
check_hash "$LAB_CLOCK_PATCH" e9b38dfe08f21834c36a74260092d9f75274103a26f8d23cf6b5cc8c725bbe3b
check_hash "$LAB_CLOCK_TEST" 149c4568aafd2c62cc1bf16070f88364c5d1a20b6fc1bf633b3fe47d82d5bf4b
check_hash "$LAB_WAIT_PATCH" f022a96f5b9781f704c33adbbd514c7c784aedeb06fa7bad4ee9b10ce98b9c9f
check_hash "$LAB_WAIT_TEST" b2c136d3f32e6b266fdf5b04d719ecf8e0a2e3474770cdcd96891517cca5b157

download_pinned() {
  LAB_MODULE_METADATA=$(go mod download -json "$1@$2")
  case "$LAB_MODULE_METADATA" in *'"Sum": "'"$3"'"'*) ;; *) echo "Module checksum mismatch: $1" >&2; exit 1 ;; esac
  case "$LAB_MODULE_METADATA" in *'"Hash": "'"$4"'"'*) ;; *) echo "Module commit mismatch: $1" >&2; exit 1 ;; esac
}
download_pinned "$LAB_GOSRT_MODULE" "$LAB_GOSRT_VERSION" "$LAB_GOSRT_SUM" "$LAB_GOSRT_COMMIT"
download_pinned "$LAB_MTX_MODULE" "$LAB_MTX_VERSION" "$LAB_MTX_SUM" "$LAB_MTX_COMMIT"
LAB_BUILD_CACHE=$(go env GOMODCACHE)
mkdir -p "$LAB_BUILD_ROOT/.tools/source"
LAB_BUILD_STAGE=$(mktemp -d "$LAB_BUILD_ROOT/.tools/source/clockfix1-lan60.XXXXXX")
echo "Keeping candidate sources and test evidence in $LAB_BUILD_STAGE"
mkdir "$LAB_BUILD_STAGE/gosrt" "$LAB_BUILD_STAGE/mediamtx"
cp -R "$LAB_BUILD_CACHE/$LAB_GOSRT_MODULE@$LAB_GOSRT_VERSION/." "$LAB_BUILD_STAGE/gosrt/"
cp -R "$LAB_BUILD_CACHE/$LAB_MTX_MODULE@$LAB_MTX_VERSION/." "$LAB_BUILD_STAGE/mediamtx/"
chmod -R u+rwX "$LAB_BUILD_STAGE"
LAB_SERVER_SOURCE="$LAB_BUILD_STAGE/mediamtx/internal/servers/srt/server.go"
LAB_UPSTREAM_SERVER_HASH=$(sha256_file "$LAB_SERVER_SOURCE")
check_hash "$LAB_SERVER_SOURCE" d419014942b341f2c67abeb49cf42df1c55b4101986f8813cbfe2596a282b9e1
patch -d "$LAB_BUILD_STAGE/gosrt" -p1 -N -t -F 0 -i "$LAB_CLOCK_PATCH"
patch -d "$LAB_BUILD_STAGE/mediamtx" -p1 -N -t -F 0 -i "$LAB_WAIT_PATCH"
cp "$LAB_CLOCK_TEST" "$LAB_BUILD_STAGE/gosrt/receive_clock_test.go"
cp "$LAB_WAIT_TEST" "$LAB_BUILD_STAGE/mediamtx/internal/servers/srt/receiver_lan60_test.go"
(
  cd "$LAB_BUILD_STAGE/gosrt"
  CGO_ENABLED=1 go test -race -count=1 -run '^TestReceiveClockIndependentOfPeerConnectionAge$' -timeout 30s .
) >"$LAB_BUILD_STAGE/receive-clock-tests.log" 2>&1 || { cat "$LAB_BUILD_STAGE/receive-clock-tests.log"; exit 1; }
cat "$LAB_BUILD_STAGE/receive-clock-tests.log"
(
  cd "$LAB_BUILD_STAGE/mediamtx"
  go mod edit -replace github.com/datarhei/gosrt=../gosrt
  printf '%s' "$LAB_BUILD_VERSION" >internal/core/VERSION
  CGO_ENABLED=1 go test -race -v -count=1 -timeout 90s ./internal/servers/srt
) >"$LAB_BUILD_STAGE/srt-server-tests.log" 2>&1 || { cat "$LAB_BUILD_STAGE/srt-server-tests.log"; exit 1; }
tail -n 12 "$LAB_BUILD_STAGE/srt-server-tests.log"
(
  cd "$LAB_BUILD_STAGE/mediamtx"
  # The upstream HLS generator checks its downloaded asset archive hash.
  go generate ./internal/servers/hls
  case "$(go env GOOS)/$(go env GOARCH)" in
    linux/arm|linux/arm64) go generate ./internal/staticsources/rpicamera ;;
  esac
  CGO_ENABLED=0 go build -trimpath -o "$LAB_BUILD_STAGE/mediamtx-bin" .
  CGO_ENABLED=0 go build -trimpath -o "$LAB_BUILD_STAGE/mediamtx-bin-repeat" .
)
[ "$("$LAB_BUILD_STAGE/mediamtx-bin" --version)" = "$LAB_BUILD_VERSION" ] || { echo 'Built version mismatch' >&2; exit 1; }
LAB_BINARY_HASH=$(sha256_file "$LAB_BUILD_STAGE/mediamtx-bin")
check_hash "$LAB_BUILD_STAGE/mediamtx-bin-repeat" "$LAB_BINARY_HASH"
mkdir -p "$(dirname "$LAB_BUILD_OUTPUT")"
mkdir "$LAB_BUILD_OUTPUT"
cp "$LAB_BUILD_STAGE/mediamtx-bin" "$LAB_BUILD_OUTPUT/mediamtx"
cp "$LAB_BUILD_STAGE/mediamtx/LICENSE" "$LAB_BUILD_OUTPUT/LICENSE"
cp "$LAB_BUILD_STAGE/gosrt/LICENSE" "$LAB_BUILD_OUTPUT/LICENSE-gosrt"
cp "$LAB_BUILD_STAGE/receive-clock-tests.log" "$LAB_BUILD_STAGE/srt-server-tests.log" "$LAB_BUILD_OUTPUT/"
cat >"$LAB_BUILD_OUTPUT/BUILD-INFO.txt" <<EOF
Version: $LAB_BUILD_VERSION
Purpose: isolated research candidate; not a general default; not activated
Only added runtime change: MediaMTX listener ReceiverLatency=60ms
Listener PeerLatency: unchanged at120ms
MediaMTX module: $LAB_MTX_MODULE@$LAB_MTX_VERSION
MediaMTX commit: $LAB_MTX_COMMIT
MediaMTX module checksum: $LAB_MTX_SUM
MediaMTX upstream zip SHA256: $(sha256_file "$LAB_BUILD_CACHE/cache/download/$LAB_MTX_MODULE/@v/$LAB_MTX_VERSION.zip")
MediaMTX upstream server.go SHA256: $LAB_UPSTREAM_SERVER_HASH
MediaMTX patched server.go SHA256: $(sha256_file "$LAB_SERVER_SOURCE")
GoSRT module: $LAB_GOSRT_MODULE@$LAB_GOSRT_VERSION
GoSRT commit: $LAB_GOSRT_COMMIT
GoSRT module checksum: $LAB_GOSRT_SUM
GoSRT upstream zip SHA256: $(sha256_file "$LAB_BUILD_CACHE/cache/download/$LAB_GOSRT_MODULE/@v/$LAB_GOSRT_VERSION.zip")
Clock patch SHA256: $(sha256_file "$LAB_CLOCK_PATCH")
Clock test SHA256: $(sha256_file "$LAB_CLOCK_TEST")
Listener patch SHA256: $(sha256_file "$LAB_WAIT_PATCH")
Listener test SHA256: $(sha256_file "$LAB_WAIT_TEST")
Build helper SHA256: $(sha256_file "$LAB_BUILD_ROOT/scripts/build-mediamtx-lan60.sh")
Patched GoSRT conn_request.go SHA256: $(sha256_file "$LAB_BUILD_STAGE/gosrt/conn_request.go")
Patched GoSRT dial.go SHA256: $(sha256_file "$LAB_BUILD_STAGE/gosrt/dial.go")
Patched GoSRT connection.go SHA256: $(sha256_file "$LAB_BUILD_STAGE/gosrt/connection.go")
Binary SHA256: $LAB_BINARY_HASH
Repeat build SHA256: $(sha256_file "$LAB_BUILD_STAGE/mediamtx-bin-repeat")
Toolchain: $(go version)
Platform: $(go env GOOS)/$(go env GOARCH)
Build: CGO_ENABLED=0 go build -trimpath
Clock race-test log SHA256: $(sha256_file "$LAB_BUILD_STAGE/receive-clock-tests.log")
SRT server race-test log SHA256: $(sha256_file "$LAB_BUILD_STAGE/srt-server-tests.log")
Source and full test logs: $LAB_BUILD_STAGE
Negotiation coverage: encrypted requests20/60/80/120/300ms; expected receive60/60/80/120/300ms; reverse120ms
Limits: no live camera, network-loss, frame-fidelity or physical-delay claim
EOF
echo "Built $LAB_BUILD_OUTPUT/mediamtx ($LAB_BUILD_VERSION); active receiver unchanged."
