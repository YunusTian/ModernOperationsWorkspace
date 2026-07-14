# release-smoke-lib.tests.ps1 —— release-smoke-lib.ps1 的最小回归测试。
#
# 覆盖场景（对应 Windows install-smoke 的历史缺陷）：
#   S1 "Catalog 含多个平台、当前目录只有本平台产物" —— 期望 ConvertTo-LocalCatalog
#      剔除其它平台，只保留 target=windows/arch=amd64 的条目，并把该条目的
#      URL 改写为 file:/// 路径。
#   S2 当前 target 对应的产物在磁盘上缺失时，platforms[] 应被清空，而不是抛出
#      Resolve-Path 的路径不存在错误（原缺陷）。
#
# 运行方式：pwsh -File scripts/release-smoke-lib.tests.ps1
# 退出码：0 全部通过；非 0 有失败项。
#
# 不依赖 Pester；在 Windows 的 powershell 5.1 与跨平台 pwsh 7.x 上都可执行。

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "release-smoke-lib.ps1")

$script:failures = New-Object System.Collections.ArrayList
function Assert-True($cond, $msg) {
    if (-not $cond) { [void]$script:failures.Add($msg) }
}
function Assert-Equal($actual, $expected, $msg) {
    if ($actual -ne $expected) {
        [void]$script:failures.Add(("{0}: expected={1} actual={2}" -f $msg, $expected, $actual))
    }
}

function New-TempDir {
    $p = Join-Path ([System.IO.Path]::GetTempPath()) ("mow-smoke-lib-" + [guid]::NewGuid())
    New-Item -ItemType Directory -Force -Path $p | Out-Null
    return $p
}

# ---------------------------------------------------------------------------
# 场景 1（回归）：catalog 含 linux/windows/darwin 三个平台，本地仅有 windows amd64
# ---------------------------------------------------------------------------
function Test-Scenario1 {
    $tmp = New-TempDir
    try {
        $winArtifact = Join-Path $tmp "mow-ssh-plugin-windows-amd64.tar.gz"
        Set-Content -LiteralPath $winArtifact -Value "fake" -Encoding ASCII

        $catalog = @{
            catalogVersion = 1
            entries = @(
                @{
                    id = "ssh"
                    versions = @(
                        @{
                            version = "0.5.1"
                            platforms = @(
                                @{ os = "linux";   arch = "amd64"; url = "https://example.test/mow-ssh-plugin-linux-amd64.tar.gz";   checksum = "sha256:aa" },
                                @{ os = "windows"; arch = "amd64"; url = "https://example.test/mow-ssh-plugin-windows-amd64.tar.gz"; checksum = "sha256:bb" },
                                @{ os = "darwin";  arch = "arm64"; url = "https://example.test/mow-ssh-plugin-darwin-arm64.tar.gz";  checksum = "sha256:cc" }
                            )
                        }
                    )
                }
            )
        } | ConvertTo-Json -Depth 8 | ConvertFrom-Json

        $result = ConvertTo-LocalCatalog -Catalog $catalog -ArtifactDir $tmp -Target "windows" -Arch "amd64"

        $platforms = @($result.entries[0].versions[0].platforms)
        Assert-Equal $platforms.Count 1 "S1 保留的平台数量"
        if ($platforms.Count -ge 1) {
            Assert-Equal $platforms[0].os   "windows" "S1 保留的 os"
            Assert-Equal $platforms[0].arch "amd64"   "S1 保留的 arch"
            $actualUrl = $platforms[0].url
            Assert-True ($actualUrl -like "file:///*mow-ssh-plugin-windows-amd64.tar.gz") ("S1 URL 已改写为 file:/// (actual={0})" -f $actualUrl)
            Assert-Equal $platforms[0].checksum "sha256:bb" "S1 保留原 checksum"
        }
    } finally {
        if ($tmp -and (Test-Path -LiteralPath $tmp)) {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# ---------------------------------------------------------------------------
# 场景 2（防御）：当前 target 对应的产物在磁盘上缺失时，platforms[] 应被清空
# ---------------------------------------------------------------------------
function Test-Scenario2 {
    $tmp = New-TempDir
    try {
        $catalog = @{
            catalogVersion = 1
            entries = @(
                @{
                    id = "docker"
                    versions = @(
                        @{
                            version = "0.5.1"
                            platforms = @(
                                @{ os = "windows"; arch = "amd64"; url = "https://example.test/mow-docker-plugin-windows-amd64.tar.gz"; checksum = "sha256:dd" }
                            )
                        }
                    )
                }
            )
        } | ConvertTo-Json -Depth 8 | ConvertFrom-Json

        $result = ConvertTo-LocalCatalog -Catalog $catalog -ArtifactDir $tmp -Target "windows" -Arch "amd64"
        $platforms = @($result.entries[0].versions[0].platforms)
        Assert-Equal $platforms.Count 0 "S2 产物缺失时 platforms[] 被清空"
    } finally {
        if ($tmp -and (Test-Path -LiteralPath $tmp)) {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

Test-Scenario1
Test-Scenario2

if ($script:failures.Count -gt 0) {
    Write-Error ("release-smoke-lib tests failed:`n - " + ($script:failures -join "`n - "))
    exit 1
}
Write-Output ("release-smoke-lib tests passed ({0} failures)" -f $script:failures.Count)
