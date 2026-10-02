# run.ps1 -- one-shot HAR analyzer
#
#   .\run.ps1                  pick the newest .har in this folder (ignores _*.har samples)
#   .\run.ps1 claude.har       use a specific file
#   .\run.ps1 claude.har -NoRedact
#
# NOTE: this file is intentionally ASCII-only. Windows PowerShell 5.1 decodes
# .ps1 files as ANSI unless they carry a UTF-8 BOM, so non-ASCII here would
# corrupt parsing. The analyzer itself (Node) emits UTF-8 Chinese fine.

param(
  [Parameter(Position = 0)][string]$Har,
  [switch]$NoRedact
)

$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here

if (-not $Har) {
  $Har = Get-ChildItem -Filter *.har -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -notlike '_*' } |
    Sort-Object LastWriteTime -Descending |
    Select-Object -First 1 -ExpandProperty Name
}

if (-not $Har) {
  Write-Host "No .har file in this folder." -ForegroundColor Yellow
  Write-Host "Export one first: DevTools > Network > tick 'Preserve log' > reproduce > right-click the request list > 'Save all as HAR with content'" -ForegroundColor Yellow
  exit 1
}
if (-not (Test-Path $Har)) { Write-Host "File not found: $Har" -ForegroundColor Red; exit 1 }

$base = [IO.Path]::GetFileNameWithoutExtension($Har)
$reportDir = Join-Path $here 'reports'
New-Item -ItemType Directory -Force -Path $reportDir | Out-Null
$md = Join-Path $reportDir "$base.md"
$json = Join-Path $reportDir "$base.json"

Write-Host "Analyzing $Har ..." -ForegroundColor Cyan

$nodeArgs = @('--max-old-space-size=8192', 'analyze-har.mjs', $Har, '--md', $md, '--json', $json)
if ($NoRedact) { $nodeArgs += '--no-redact' }

& node @nodeArgs
if ($LASTEXITCODE -ne 0) { Write-Host "Analyzer failed (exit $LASTEXITCODE)" -ForegroundColor Red; exit $LASTEXITCODE }

Write-Host ""
Write-Host "Done:" -ForegroundColor Green
Write-Host "  Markdown: $md"
Write-Host "  JSON    : $json"
