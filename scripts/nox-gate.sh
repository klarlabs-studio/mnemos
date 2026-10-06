#!/usr/bin/env bash
# Security gate: scan with the pinned nox and fail on net-new critical/high
# findings (anything .nox/baseline.json does not list). Run by warden's
# pre-push hook; merges go through warden, so this is the security check on
# the path to main.
#
# The nox version is pinned, and the archive is checked against a committed
# sha256 before it runs, every time. It must match the version the shared
# go-ci workflow pins (klarlabs-studio/.github, nox-version input), because
# fingerprints differ between nox releases: a baseline written by one version
# looks entirely net-new to another.
#
#   NOX_GATE_WRITE_BASELINE=1 scripts/nox-gate.sh   # re-baseline to current findings
set -euo pipefail

NOX_VERSION=1.34.0
# From nox's checksums.txt for v1.34.0; its cosign bundle verifies against
# Nox-HQ/nox/.github/workflows/release.yml@refs/tags/v1.34.0.
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)        plat=darwin_arm64; want=d4ce37ccd9b090c5640b40b1398eaaa169400f11019930e6c52784d68d0ff9a5 ;;
  Darwin/x86_64)       plat=darwin_amd64; want=9207fa64d4192597f891e78b2a26f153056b5870690410bba028ceb824f8759a ;;
  Linux/x86_64)        plat=linux_amd64;  want=4a207ef6d30f260de037d5c9a77379547733c41f84f2d78d657249735fae3de0 ;;
  Linux/aarch64|Linux/arm64) plat=linux_arm64; want=ec7c4d0f650f96e5461bf9b433fbaff42048c9d356a9b214ce5ba3f28078a812 ;;
  *) echo "nox-gate: unsupported platform $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

cache="${XDG_CACHE_HOME:-$HOME/.cache}/mnemos/nox/${NOX_VERSION}"
archive="${cache}/nox_${NOX_VERSION}_${plat}.tar.gz"
mkdir -p "$cache"
if [ ! -f "$archive" ] || [ "$(sha256 "$archive")" != "$want" ]; then
  curl -fsSL -o "${archive}.part" \
    "https://github.com/nox-hq/nox/releases/download/v${NOX_VERSION}/nox_${NOX_VERSION}_${plat}.tar.gz"
  mv "${archive}.part" "$archive"
fi
got="$(sha256 "$archive")"
if [ "$got" != "$want" ]; then
  echo "nox-gate: nox ${NOX_VERSION} archive sha256 ${got}, want ${want}; refusing to run it" >&2
  rm -f "$archive"
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
tar -xzf "$archive" -C "$work" nox

# nox exits non-zero on any unsuppressed finding at any severity; noxgate,
# not that exit code, decides what blocks.
"$work/nox" scan . -format json -output "$work/out" >/dev/null 2>&1 || true
[ -s "$work/out/findings.json" ] || { echo "nox-gate: scan produced no findings.json" >&2; exit 1; }

if [ "${NOX_GATE_WRITE_BASELINE:-}" = "1" ]; then
  go run ./tools/noxgate -findings "$work/out/findings.json" -write
  echo "nox-gate: baseline rewritten; cover any new entry in .nox/waivers.yaml"
else
  go run ./tools/noxgate -findings "$work/out/findings.json"
fi
