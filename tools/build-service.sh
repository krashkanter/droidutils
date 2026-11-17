#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../service"
mkdir -p ../dist
"${GO:-go}" test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 "${GO:-go}" build -trimpath -ldflags='-s -w' -o ../dist/sysutilsd .