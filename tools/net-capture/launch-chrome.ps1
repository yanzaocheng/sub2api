# launch-chrome.ps1 -- start a Chrome that the agent can drive over CDP
#
# Run this in YOUR OWN terminal (not through the agent's sandbox).
# It opens a SEPARATE Chrome instance with its own profile, so your normal
# browsing session is not touched.
#
#   .\launch-chrome.ps1
#   .\launch-chrome.ps1 -Url "https://claude.ai/"
#   .\launch-chrome.ps1 -NoProxy                  # if you use TUN mode instead
#   .\launch-chrome.ps1 -Port 9222
#
# NOTE: ASCII-only on purpose. Windows PowerShell 5.1 decodes .ps1 as ANSI
# unless the file has a UTF-8 BOM, so non-ASCII here would break parsing.

param(
  [int]$Port = 9222,
  [string]$Url = "https://claude.ai/",
  [string]$Proxy = "http://127.0.0.1:7892",
  [switch]$NoProxy,
  [string]$ProfileDir
)

$ErrorActionPreference = 'Stop'

if (-not $ProfileDir) { $ProfileDir = Join-Path $PSScriptRoot '.chrome-profile' }

$chrome = @(
  "C:\Program Files\Google\Chrome\Application\chrome.exe",
  "C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
  "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1

if (-not $chrome) { Write-Host "chrome.exe not found" -ForegroundColor Red; exit 1 }

# Chrome 136+ ignores --remote-debugging-port when the default user data dir is
# used, so a dedicated --user-data-dir is mandatory. Chrome 153 here.
$args = @(
  "--remote-debugging-port=$Port",
  "--remote-allow-origins=*",
  "--user-data-dir=$ProfileDir",
  "--no-first-run",
  "--no-default-browser-check",
  "--disable-features=Translate",
  $Url
)
if (-not $NoProxy) { $args = @("--proxy-server=$Proxy") + $args }

Write-Host "Chrome : $chrome"
Write-Host "Profile: $ProfileDir"
if (-not $NoProxy) { Write-Host "Proxy  : $Proxy" } else { Write-Host "Proxy  : (none - relies on TUN/system routing)" }
Write-Host "CDP    : http://127.0.0.1:$Port"
Write-Host ""

Start-Process -FilePath $chrome -ArgumentList $args

# Wait for the DevTools endpoint to come up.
$ok = $false
foreach ($i in 1..30) {
  Start-Sleep -Milliseconds 700
  try {
    $r = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/json/version" -UseBasicParsing -TimeoutSec 2
    if ($r.StatusCode -eq 200) { $ok = $true; break }
  } catch { }
}

if ($ok) {
  Write-Host "CDP is UP on port $Port." -ForegroundColor Green
  Write-Host ""
  Write-Host "First run: log into claude.ai in that window (the profile is fresh)." -ForegroundColor Yellow
  Write-Host "After that the login persists in $ProfileDir, so later runs skip it." -ForegroundColor Yellow
  Write-Host ""
  Write-Host "Then tell the agent to run:" -ForegroundColor Cyan
  Write-Host "  node tools\net-capture\cdp.mjs tabs"
  Write-Host "  node tools\net-capture\cdp.mjs capture --seconds 90 --out reports\claude.har"
} else {
  Write-Host "CDP did not come up on port $Port." -ForegroundColor Red
  Write-Host "Check: is another Chrome already using that port? Try -Port 9333" -ForegroundColor Yellow
  exit 1
}
