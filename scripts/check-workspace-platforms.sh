#!/usr/bin/env bash
set -euo pipefail

go_bin="${GO:-go}"
output_dir="$(mktemp -d)"
trap 'rm -rf "$output_dir"' EXIT

targets=(
  linux/amd64
  linux/arm64
  windows/amd64
  darwin/amd64
  darwin/arm64
  dragonfly/amd64
  freebsd/amd64
  netbsd/amd64
  openbsd/amd64
  aix/ppc64
  illumos/amd64
  solaris/amd64
  plan9/amd64
  js/wasm
  wasip1/wasm
)

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  echo "compile internal/workspace for ${goos}/${goarch}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    "$go_bin" test -c ./internal/workspace \
    -o "$output_dir/workspace-${goos}-${goarch}.test"
done
