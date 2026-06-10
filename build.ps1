# Build gralph for all release platforms into dist\.
# Artifacts: dist\gralph-<os>-<arch>[.exe]
# Convenience copies for local use: dist\gralph.exe (windows, host arch), dist\gralph (linux, for WSL).
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$platforms = @(
    "linux/amd64", "linux/arm64",
    "windows/amd64", "windows/arm64",
    "darwin/amd64", "darwin/arm64"
)

# Version to stamp into the binary: $env:VERSION (set by CI from the git
# tag) or the exact tag of the checked-out commit, if any.
$version = $env:VERSION
if (-not $version) { $version = git describe --tags --exact-match 2>$null }
$ldflags = if ($version) { "-X main.version=$version" } else { "" }
Write-Host "[build] version: $(if ($version) { $version } else { '(none)' })"

New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = "0"
foreach ($p in $platforms) {
    $os, $arch = $p -split '/'
    $out = "dist\gralph-$os-$arch"
    if ($os -eq "windows") { $out += ".exe" }
    Write-Host "[build] $os/$arch"
    $env:GOOS = $os; $env:GOARCH = $arch
    go build -trimpath -ldflags $ldflags -o $out .
    if ($LASTEXITCODE -ne 0) { throw "build failed: $p" }
}
$env:GOOS = ""; $env:GOARCH = ""; $env:CGO_ENABLED = ""

$hostArch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
Copy-Item "dist\gralph-windows-$hostArch.exe" dist\gralph.exe -Force
Copy-Item "dist\gralph-linux-$hostArch" dist\gralph -Force
Write-Host "[build] done:"
Get-ChildItem dist\gralph* | ForEach-Object { Write-Host "  $($_.Name)" }
