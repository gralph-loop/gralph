#!/usr/bin/env sh
# Build gralph for the current platform and cross-build for Windows.
set -e
cd "$(dirname "$0")"
echo "[build] native ($(go env GOOS)/$(go env GOARCH))"
go build -o gralph .
echo "[build] windows/amd64"
GOOS=windows GOARCH=amd64 go build -o gralph.exe .
cp gralph gralph.exe
echo "[build] done: ./gralph, ./gralph.exe"
