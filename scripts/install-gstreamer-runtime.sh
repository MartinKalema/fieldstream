#!/bin/sh
# Extract the pinned official macOS runtime into a private project directory.
# This does not run the package installer or change the active lab programs.
set -eu
umask 077

GST_INSTALL_VERSION=1.28.7
GST_INSTALL_URL=https://gstreamer.freedesktop.org/data/pkg/osx/1.28.7/gstreamer-1.0-1.28.7-universal.pkg
GST_INSTALL_SHA=529fdf4a4027d942e59b5b3564f6400adaa008f63ce5f3fed4ffe35d73911994
GST_INSTALL_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GST_INSTALL_STAGE=

usage() {
  echo "Usage: $0 [new-output-directory [existing-package-file]]"
  echo "Default output: $GST_INSTALL_ROOT/.tools/gstreamer-$GST_INSTALL_VERSION"
  echo 'Relative output paths start at the project folder; package paths start at the current folder.'
  echo 'Existing output directories are refused. No system installer or lab activation is performed.'
}

case ${1:-} in
  -h|--help) usage; exit 0 ;;
esac
if [ "$#" -gt 2 ]; then
  usage >&2
  exit 2
fi
if [ "$(uname -s)" != Darwin ]; then
  echo 'This private runtime setup requires macOS.' >&2
  exit 1
fi

GST_INSTALL_OUTPUT=${1:-"$GST_INSTALL_ROOT/.tools/gstreamer-$GST_INSTALL_VERSION"}
case "$GST_INSTALL_OUTPUT" in
  /*) ;;
  *) GST_INSTALL_OUTPUT="$GST_INSTALL_ROOT/$GST_INSTALL_OUTPUT" ;;
esac
# Strip trailing slashes before checking for a dangling output symlink.
while [ "${GST_INSTALL_OUTPUT%/}" != "$GST_INSTALL_OUTPUT" ]; do
  GST_INSTALL_OUTPUT=${GST_INSTALL_OUTPUT%/}
done
if [ -z "$GST_INSTALL_OUTPUT" ] || [ -e "$GST_INSTALL_OUTPUT" ] || [ -L "$GST_INSTALL_OUTPUT" ]; then
  echo "Output already exists or is invalid; choose a new directory: $GST_INSTALL_OUTPUT" >&2
  exit 1
fi

for GST_INSTALL_TOOL in shasum pkgutil ditto mktemp; do
  if ! command -v "$GST_INSTALL_TOOL" >/dev/null 2>&1; then
    echo "Required macOS tool is unavailable: $GST_INSTALL_TOOL" >&2
    exit 1
  fi
done

GST_INSTALL_PARENT=$(dirname -- "$GST_INSTALL_OUTPUT")
GST_INSTALL_NAME=$(basename -- "$GST_INSTALL_OUTPUT")
mkdir -p "$GST_INSTALL_PARENT"
GST_INSTALL_PARENT=$(CDPATH= cd -- "$GST_INSTALL_PARENT" && pwd -P)
GST_INSTALL_OUTPUT="$GST_INSTALL_PARENT/$GST_INSTALL_NAME"
if [ -e "$GST_INSTALL_OUTPUT" ] || [ -L "$GST_INSTALL_OUTPUT" ]; then
  echo "Output already exists; choose a new directory: $GST_INSTALL_OUTPUT" >&2
  exit 1
fi

cleanup() {
  # Only remove the unique temporary directory created by this invocation.
  if [ -n "$GST_INSTALL_STAGE" ]; then
    rm -rf "$GST_INSTALL_STAGE"
  fi
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
GST_INSTALL_STAGE=$(mktemp -d "$GST_INSTALL_PARENT/.gstreamer-stage.XXXXXX")

if [ "$#" -eq 2 ]; then
  GST_INSTALL_PACKAGE=$2
  case "$GST_INSTALL_PACKAGE" in
    /*) ;;
    *) GST_INSTALL_PACKAGE="$(pwd -P)/$GST_INSTALL_PACKAGE" ;;
  esac
  if [ ! -f "$GST_INSTALL_PACKAGE" ]; then
    echo 'The supplied package path must name an existing file.' >&2
    exit 1
  fi
else
  if ! command -v curl >/dev/null 2>&1; then
    echo 'curl is required when no existing package file is supplied.' >&2
    exit 1
  fi
  GST_INSTALL_PACKAGE="$GST_INSTALL_STAGE/gstreamer.pkg"
  echo "Downloading the official GStreamer $GST_INSTALL_VERSION runtime."
  curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 20 --max-time 600 \
    --retry 2 --retry-delay 2 --retry-max-time 900 \
    --output "$GST_INSTALL_PACKAGE" "$GST_INSTALL_URL"
fi

GST_INSTALL_ACTUAL_SHA=$(shasum -a 256 "$GST_INSTALL_PACKAGE" | awk '{print $1}')
if [ "$GST_INSTALL_ACTUAL_SHA" != "$GST_INSTALL_SHA" ]; then
  echo 'Package SHA-256 mismatch; nothing was installed.' >&2
  exit 1
fi
echo 'Package checksum verified. Extracting without running installer scripts.'
pkgutil --expand-full "$GST_INSTALL_PACKAGE" "$GST_INSTALL_STAGE/expanded"
mkdir "$GST_INSTALL_STAGE/install"
mkdir "$GST_INSTALL_STAGE/install/runtime"
GST_INSTALL_PAYLOADS=0
for GST_INSTALL_COMPONENT in "$GST_INSTALL_STAGE/expanded/"*.pkg; do
  case $(basename -- "$GST_INSTALL_COMPONENT") in
    osx-framework-*|gstreamer-1.0-python-*) continue ;;
  esac
  if [ ! -d "$GST_INSTALL_COMPONENT/Payload" ]; then
    echo "Expected component payload is missing: $(basename -- "$GST_INSTALL_COMPONENT")" >&2
    exit 1
  fi
  ditto "$GST_INSTALL_COMPONENT/Payload" "$GST_INSTALL_STAGE/install/runtime"
  GST_INSTALL_PAYLOADS=$((GST_INSTALL_PAYLOADS + 1))
done
if [ "$GST_INSTALL_PAYLOADS" -eq 0 ]; then
  echo 'No runtime payloads were found.' >&2
  exit 1
fi

mkdir "$GST_INSTALL_STAGE/install/viewer-plugins"
for GST_INSTALL_PLUGIN in \
  applemedia coreelements debugutilsbad libav opengl rtp rtpmanager rtsp \
  tcp udp videoconvertscale videoparsersbad videotestsrc x264; do
  GST_INSTALL_PLUGIN_FILE="libgst$GST_INSTALL_PLUGIN.dylib"
  if [ ! -f "$GST_INSTALL_STAGE/install/runtime/lib/gstreamer-1.0/$GST_INSTALL_PLUGIN_FILE" ]; then
    echo "Required viewer plugin is missing: $GST_INSTALL_PLUGIN_FILE" >&2
    exit 1
  fi
  ln -s "../runtime/lib/gstreamer-1.0/$GST_INSTALL_PLUGIN_FILE" \
    "$GST_INSTALL_STAGE/install/viewer-plugins/$GST_INSTALL_PLUGIN_FILE"
done

for GST_INSTALL_COMMAND in gst-launch-1.0 gst-inspect-1.0; do
  if [ ! -x "$GST_INSTALL_STAGE/install/runtime/bin/$GST_INSTALL_COMMAND" ]; then
    echo "Required runtime command is missing: $GST_INSTALL_COMMAND" >&2
    exit 1
  fi
  cat >"$GST_INSTALL_STAGE/install/$GST_INSTALL_COMMAND" <<'WRAPPER'
#!/bin/sh
set -eu
umask 077
GST_LAB_BASE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
export DYLD_LIBRARY_PATH="$GST_LAB_BASE/runtime/lib"
export GST_PLUGIN_SYSTEM_PATH_1_0="$GST_LAB_BASE/viewer-plugins"
export GST_PLUGIN_PATH_1_0=""
export GST_PLUGIN_SCANNER_1_0="$GST_LAB_BASE/runtime/libexec/gstreamer-1.0/gst-plugin-scanner"
export GST_REGISTRY_1_0="$GST_LAB_BASE/viewer-registry.bin"
export GIO_EXTRA_MODULES="$GST_LAB_BASE/runtime/lib/gio/modules"
WRAPPER
  printf 'exec "$GST_LAB_BASE/runtime/bin/%s" "$@"\n' "$GST_INSTALL_COMMAND" \
    >>"$GST_INSTALL_STAGE/install/$GST_INSTALL_COMMAND"
  chmod 0700 "$GST_INSTALL_STAGE/install/$GST_INSTALL_COMMAND"
done
if [ ! -x "$GST_INSTALL_STAGE/install/runtime/libexec/gstreamer-1.0/gst-plugin-scanner" ]; then
  echo 'The required private plugin scanner is missing.' >&2
  exit 1
fi

cat >"$GST_INSTALL_STAGE/install/RUNTIME-INFO.txt" <<EOF
Version: $GST_INSTALL_VERSION
Package URL: $GST_INSTALL_URL
Package SHA-256: $GST_INSTALL_SHA
Layout: extracted component payloads; osx-framework and Python components excluded
Plugin scan: 14 viewer and generated-test plugins in viewer-plugins
Registry: private viewer-registry.bin, created on first use
Status: installed privately; no active lab program or global search path changed
EOF

# mkdir is the final exclusive claim: it fails if another process created the
# output while extraction ran. A copy failure leaves only this new directory
# for inspection; cleanup never removes any final output directory.
mkdir "$GST_INSTALL_OUTPUT"
if ! ditto "$GST_INSTALL_STAGE/install" "$GST_INSTALL_OUTPUT"; then
  echo "Copy failed. An incomplete new output was left for inspection: $GST_INSTALL_OUTPUT" >&2
  exit 1
fi
echo "Private runtime ready: $GST_INSTALL_OUTPUT"
echo 'No camera connection, active lab program or global program path was changed.'
