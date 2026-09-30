$ErrorActionPreference = 'Stop'
& python (Join-Path $PSScriptRoot 'tools/local_runtime.py') start
if ($LASTEXITCODE -ne 0) { throw 'Local startup failed. See .dev/*.log.' }
