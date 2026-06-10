# Fake non-interactive agent for e2e testing on Windows.
# One invocation = one session. Mirrors test/agent.sh:
#   1. ask `gralph next` for guidance
#   2. "do the task" (simulated)
#   3. run the instructed command (line starting with RUN:)
#   4. obey "End the session" responses; otherwise fix & retry in-session
param([string]$Prompt = "")
$ErrorActionPreference = "Continue"

Write-Host "----- agent session start -----"

$env:PATH = "$env:PATH;$(Join-Path $PSScriptRoot '..\..\dist')"
$guidance = & "gralph.exe" next 2>&1 | Out-String
if ($LASTEXITCODE -ne 0) { Write-Host "agent: next failed"; exit 1 }
($guidance -split "`r?`n") | Where-Object { $_ -ne "" } | ForEach-Object { Write-Host "  [next] $_" }

$runLine = ($guidance -split "`r?`n") | Where-Object { $_ -match '^RUN: ' } | Select-Object -First 1
if (-not $runLine) { Write-Host "agent: no RUN line"; exit 1 }
$cmdline = $runLine -replace '^RUN: ', ''

# Simulated non-deterministic work: pick a goal / write a report etc.
$cmdline = $cmdline -replace '<your-goal>', 'demo'
$cmdline = $cmdline -replace '<one line>', '"all done nicely"'

for ($attempt = 1; $attempt -le 3; $attempt++) {
    Write-Host "  [agent] running: $cmdline"
    $out = Invoke-Expression "$cmdline 2>&1" | Out-String
    $code = $LASTEXITCODE
    ($out -split "`r?`n") | Where-Object { $_ -ne "" } | ForEach-Object { Write-Host "  [cmd] $_" }

    if ($code -eq 0) {
        Write-Host "----- agent session end (success response) -----"
        exit 0
    }
    if ($out -match "End the session") {
        Write-Host "----- agent session end (forced by gralph) -----"
        exit 0
    }
    # in-session remediation: e.g. create the missing report file, then retry
    Write-Host "  [agent] remediating and retrying in the same session"
    New-Item -ItemType File -Path "report.txt" -Force | Out-Null
}
Write-Host "----- agent session end (gave up) -----"
exit 1
