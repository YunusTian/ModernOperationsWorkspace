# release-smoke-lib.ps1 —— 供 release-smoke.ps1 与其回归测试共同使用的
# 平台过滤 / URL 改写辅助函数。
#
# 单独抽出为模块是因为主 smoke 脚本会真正调 mow.exe / tar，无法在 Windows
# CI runner 上离线复用；把纯 JSON 变换逻辑独立出来后，回归测试可以在不下载
# 任何 release artifact 的情况下验证平台过滤规则。

Set-StrictMode -Off
$ErrorActionPreference = "Stop"

<#
.SYNOPSIS
    根据 target / arch 过滤 catalog 中的 platforms[]，并把保留下来的 URL
    改写为 file:// 指向本地 artifact 目录。

.DESCRIPTION
    Windows install-smoke 只会下载"当前 target/arch"的 tar.gz（release.yml
    的 `binaries-<target>-<arch>` artifact），因此 catalog 里其它平台的产
    物在本机上并不存在。历史实现对所有平台都执行 Resolve-Path，遇到不存在
    的文件直接抛错。该函数只保留 target/arch 匹配且本机存在的条目，其余
    条目从 `versions[].platforms` 中剔除。

.PARAMETER Catalog
    通过 ConvertFrom-Json 得到的 catalog 对象；本函数会就地修改其
    entries[].versions[].platforms 数组。

.PARAMETER ArtifactDir
    存放 tar.gz 的本地目录。

.PARAMETER Target
    当前平台 target（linux / windows / darwin）。

.PARAMETER Arch
    当前平台 arch（amd64 / arm64 等）。

.OUTPUTS
    返回改写后的 catalog 对象。
#>
function ConvertTo-LocalCatalog {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Catalog,
        [Parameter(Mandatory = $true)][string]$ArtifactDir,
        [Parameter(Mandatory = $true)][string]$Target,
        [Parameter(Mandatory = $true)][string]$Arch
    )
    foreach ($e in $Catalog.entries) {
        foreach ($r in $e.versions) {
            $kept = @()
            foreach ($p in $r.platforms) {
                if ($p.os -ne $Target -or $p.arch -ne $Arch) { continue }
                $fname = ([Uri]$p.url).Segments[-1]
                $localPath = Join-Path $ArtifactDir $fname
                if (!(Test-Path -LiteralPath $localPath)) { continue }
                $local = (Resolve-Path -LiteralPath $localPath).Path
                # file:///C:/... 形式，Windows 需要 3 个斜杠且盘符前有斜杠
                $p.url = "file:///" + ($local -replace "\\", "/")
                $kept += $p
            }
            $r.platforms = $kept
        }
    }
    return $Catalog
}
