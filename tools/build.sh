#!/bin/sh
# Builds BlackoutMode-<version>.exe into dist/. Needs Go 1.24 and LLVM (llvm-rc, llvm-cvtres).
# The libraries are in third_party, nothing is downloaded.
set -eu
cd "$(dirname "$0")/.."
VERSION=$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\(.*\)"$/\1/p' main_windows.go)
test -n "$VERSION"
go test ./...
(cd res && llvm-rc /C 65001 /FO app.res app.rc && llvm-cvtres /MACHINE:X64 /OUT:../rsrc_windows_amd64.syso app.res && rm app.res)
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H=windowsgui -s -w" -o "dist/BlackoutMode-$VERSION.exe" .
rm -f rsrc_windows_amd64.syso
(cd dist && sha256sum "BlackoutMode-$VERSION.exe" > "BlackoutMode-$VERSION.exe.sha256")
echo "dist/BlackoutMode-$VERSION.exe"
