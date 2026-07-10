# lint.ps1 -- run golangci-lint across all modules on Windows.
#
# In CI the official action installs golangci-lint. Locally when absent the
# script prints a SKIP and returns 0.

$ErrorActionPreference = 'Stop'

if (-not (Get-Command golangci-lint -ErrorAction SilentlyContinue)) {
    Write-Host "[lint] SKIP: golangci-lint not on PATH." -ForegroundColor Yellow
    Write-Host "        install: https://golangci-lint.run/usage/install/"
    exit 0
}

$repo = Split-Path -Parent $PSScriptRoot
$modules = @('core','apps\cli','apps\desktop','plugins\ssh','plugins\docker','sdk','tests\e2e')
foreach ($m in $modules) {
    $path = Join-Path $repo $m
    Write-Host "[lint] --- $m ---" -ForegroundColor Cyan
    Push-Location $path
    try {
        golangci-lint run --config (Join-Path $repo '.golangci.yml') ./...
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } finally {
        Pop-Location
    }
}
Write-Host "[lint] OK" -ForegroundColor Green
