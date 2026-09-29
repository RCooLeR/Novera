#!/usr/bin/env bash
# Copyright (c) 2018-Present Lea Anthony
# SPDX-License-Identifier: MIT

# Fail script on any error
set -euxo pipefail

# Define variables
APP_DIR="${APP_NAME}.AppDir"
LINUXDEPLOY_VERSION="1-alpha-20251107-1"

# Create AppDir structure
mkdir -p "${APP_DIR}/usr/bin"
cp -r "${APP_BINARY}" "${APP_DIR}/usr/bin/"
cp "${ICON_PATH}" "${APP_DIR}/"
cp "${DESKTOP_FILE}" "${APP_DIR}/"

case "$(uname -m)" in
    x86_64)
        LINUXDEPLOY_ASSET="linuxdeploy-x86_64.AppImage"
        LINUXDEPLOY_SHA256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
        ;;
    aarch64|arm64)
        LINUXDEPLOY_ASSET="linuxdeploy-aarch64.AppImage"
        LINUXDEPLOY_SHA256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
        ;;
    *)
        echo "Unsupported AppImage build architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

wget -q -4 --output-document "${LINUXDEPLOY_ASSET}" \
    "https://github.com/linuxdeploy/linuxdeploy/releases/download/${LINUXDEPLOY_VERSION}/${LINUXDEPLOY_ASSET}"
printf '%s  %s\n' "${LINUXDEPLOY_SHA256}" "${LINUXDEPLOY_ASSET}" | sha256sum --check --strict -
chmod +x "${LINUXDEPLOY_ASSET}"
"./${LINUXDEPLOY_ASSET}" --appdir "${APP_DIR}" --output appimage

# Rename the one generated AppImage. Keep the glob outside quotes so it expands,
# then reject zero/multiple matches instead of moving an arbitrary stale file.
shopt -s nullglob
generated_appimages=("${APP_NAME}"*.AppImage)
if [[ ${#generated_appimages[@]} -ne 1 ]]; then
    echo "Expected exactly one generated ${APP_NAME} AppImage, found ${#generated_appimages[@]}" >&2
    exit 1
fi
if [[ "${generated_appimages[0]}" != "${APP_NAME}.AppImage" ]]; then
    mv -- "${generated_appimages[0]}" "${APP_NAME}.AppImage"
fi
