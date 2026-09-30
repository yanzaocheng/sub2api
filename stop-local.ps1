$ErrorActionPreference = 'Stop'
& python (Join-Path $PSScriptRoot 'tools/local_runtime.py') stop
if ($LASTEXITCODE -ne 0) { throw 'Could not stop the recorded local processes.' }
