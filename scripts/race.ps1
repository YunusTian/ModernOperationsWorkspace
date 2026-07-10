# race.ps1 —— 在启用 CGO 的前提下对全仓跑 -race。
#
# 用法：
#   .\scripts\race.ps1
#
# 前置条件：
#   - CGO_ENABLED=1
#   - PATH 中存在 C 编译器（gcc/tdm-gcc/clang）；Windows 推荐 TDM-GCC 或 mingw-w64。
#
# 无 CGO 时脚本会 exit 0 并打印 skip 信息，以便 CI 上矩阵配置可以按平台跳过。

$ErrorActionPreference = 'Stop'

function Test-CgoReady {
    if (-not (Get-Command gcc -ErrorAction SilentlyContinue) `
        -and -not (Get-Command clang -ErrorAction SilentlyContinue)) {
        return $false
    }
    return $true
}

if (-not (Test-CgoReady)) {
    Write-Host "[race] SKIP: no C compiler (gcc/clang) in PATH; install TDM-GCC or mingw-w64 to enable -race." -ForegroundColor Yellow
    exit 0
}

$env:CGO_ENABLED = '1'

$modules = @(
    'core',
    'plugins\ssh',
    'plugins\docker',
    'tests\e2e'
)

$repo = Split-Path -Parent $PSScriptRoot
foreach ($m in $modules) {
    $path = Join-Path $repo $m
    Write-Host "[race] --- $m ---" -ForegroundColor Cyan
    Push-Location $path
    try {
        go test -race -count=1 ./...
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[race] FAIL in $m" -ForegroundColor Red
            exit $LASTEXITCODE
        }
    } finally {
        Pop-Location
    }
}
Write-Host "[race] OK" -ForegroundColor Green
