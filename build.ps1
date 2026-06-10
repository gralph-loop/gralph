# Build gralph for Windows and cross-build for Linux (e.g. for WSL).
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
Write-Host "[build] windows/amd64"
$env:GOOS = ""; $env:GOARCH = ""
go build -o dist\gralph.exe .
Write-Host "[build] linux/amd64"
$env:GOOS = "linux"; $env:GOARCH = "amd64"
go build -o dist\gralph .
$env:GOOS = ""; $env:GOARCH = ""
Write-Host "[build] done: .\gralph.exe, .\gralph"
