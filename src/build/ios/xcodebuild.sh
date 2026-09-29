#!/usr/bin/env bash
set -euo pipefail

# Preserve both compiler and formatter failures. Never retry a failed build or
# let the device task continue to install an artifact left by an earlier build.
if command -v xcpretty >/dev/null 2>&1; then
  xcodebuild "$@" | xcpretty
else
  exec xcodebuild "$@"
fi
