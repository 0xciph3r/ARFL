#!/usr/bin/env bash
# Copyright (c) 2018-Present Lea Anthony
# SPDX-License-Identifier: MIT

# Fail script on any error
set -euxo pipefail

# Define variables
APP_DIR="${APP_NAME}.AppDir"

# Create AppDir structure
mkdir -p "${APP_DIR}/usr/bin"
cp -r "${APP_BINARY}" "${APP_DIR}/usr/bin/"
cp "${ICON_PATH}" "${APP_DIR}/"
cp "${DESKTOP_FILE}" "${APP_DIR}/"

# linuxdeploy runs inside the build that produces the shipped AppImage, so it
# is pinned to one release and checked against a known hash before it runs.
# The mutable "continuous" tag could be replaced at any time.
LINUXDEPLOY_VERSION="1-alpha-20251107-1"
if [[ $(uname -m) == *x86_64* ]]; then
    LINUXDEPLOY="linuxdeploy-x86_64.AppImage"
    LINUXDEPLOY_SHA256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
else
    LINUXDEPLOY="linuxdeploy-aarch64.AppImage"
    LINUXDEPLOY_SHA256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
fi

wget -q -4 -O "${LINUXDEPLOY}" "https://github.com/linuxdeploy/linuxdeploy/releases/download/${LINUXDEPLOY_VERSION}/${LINUXDEPLOY}"
echo "${LINUXDEPLOY_SHA256}  ${LINUXDEPLOY}" | sha256sum -c -
chmod +x "${LINUXDEPLOY}"

# Run linuxdeploy to bundle the application
"./${LINUXDEPLOY}" --appdir "${APP_DIR}" --output appimage

# Rename the generated AppImage
# The glob must sit outside the quotes or it never matches.
mv "${APP_NAME}"*.AppImage "${APP_NAME}.AppImage"

