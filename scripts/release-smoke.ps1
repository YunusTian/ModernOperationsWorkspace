param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [string]$Target = "windows",
    [string]$Arch = "amd64"
)
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "release-smoke-lib.ps1")
$root = Join-Path ([System.IO.Path]::GetTempPath()) ("mow-smoke-" + [guid]::NewGuid())
try {
    $install = Join-Path $root "install"
    $plugins = Join-Path $install "plugins"
    $data = Join-Path $root "data"
    New-Item -ItemType Directory -Force -Path $plugins, $data | Out-Null

    $cliArchive = Join-Path $ArtifactDir "mow-$Target-$Arch.tar.gz"
    if (!(Test-Path -LiteralPath $cliArchive)) { throw "CLI release archive is missing from $ArtifactDir" }
    tar -xzf $cliArchive -C $install

    $mow = Join-Path $install "mow.exe"

    # ------------------------------------------------------------------
    # Phase 1: plugin validate 冒烟（保持原有语义，覆盖 legacy path）
    # ------------------------------------------------------------------
    foreach ($id in @("ssh", "docker", "ai", "pve")) {
        $archive = Join-Path $ArtifactDir "mow-$id-plugin-$Target-$Arch.tar.gz"
        if (!(Test-Path -LiteralPath $archive)) { throw "$id release archive is missing from $ArtifactDir" }
        $package = Join-Path $plugins $id
        New-Item -ItemType Directory -Force -Path $package | Out-Null
        tar -xzf $archive -C $package
        & $mow plugin validate $package
        if ($LASTEXITCODE -ne 0) { throw "mow plugin validate $id failed with exit code $LASTEXITCODE" }
    }

    $config = @{
        version = 1
        app = @{ data_dir = $data; plugins_dir = $plugins }
        plugins = @{ ai = @{ enabled = $true; settings = @{ providers = @(@{ name = "mock"; kind = "mock" }) } } }
    }
    $configPath = Join-Path $root "config.json"
    $configJson = $config | ConvertTo-Json -Depth 8
    [System.IO.File]::WriteAllText($configPath, $configJson, [System.Text.UTF8Encoding]::new($false))

    $version = & $mow version
    if ($LASTEXITCODE -ne 0) { throw "mow version failed with exit code $LASTEXITCODE" }
    if (!$version) { throw "mow version returned empty output" }
    $providers = & $mow --config $configPath ai providers
    if ($LASTEXITCODE -ne 0) { throw "mow ai providers failed with exit code $LASTEXITCODE" }
    if (($providers -join "`n") -notmatch "mock") { throw "mock provider missing: $providers" }
    Write-Output "release package + runtime smoke passed: target=$Target arch=$Arch version=$version"

    # ------------------------------------------------------------------
    # Phase 2: 通过 catalog 走 refresh → search → install → uninstall
    # ------------------------------------------------------------------
    $catJson = Join-Path $ArtifactDir "catalog.json"
    if (!(Test-Path -LiteralPath $catJson)) {
        Write-Output "catalog.json not present; skipping catalog smoke"
        return
    }
    # 派生 catalog：把 URL 改写为 file:// 指向本地 artifact 目录。
    #
    # 注意（Windows 平台过滤）：install-smoke 矩阵只会下载"当前 target/arch"的 tar.gz
    # （见 release.yml 的 `binaries-<target>-<arch>` artifact），因此 catalog 里其它
    # 平台的产物在本机上并不存在。历史实现调用 `Resolve-Path`，遇到不存在的文件
    # 直接抛错，导致 Windows smoke 在解析 linux/darwin 平台条目时失败。
    #
    # 修复：按 target/arch 过滤 platforms[]，只保留本机可解析的条目；对齐 Bash
    # 版本"未访问的 URL 无需存在"的语义。实现在 release-smoke-lib.ps1
    # 的 ConvertTo-LocalCatalog（便于 Pester 覆盖）。
    $catObj = Get-Content -LiteralPath $catJson -Raw | ConvertFrom-Json
    $catObj = ConvertTo-LocalCatalog -Catalog $catObj -ArtifactDir $ArtifactDir -Target $Target -Arch $Arch
    $derivedCat = Join-Path $root "catalog.json"
    $catObj | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $derivedCat -Encoding UTF8

    $catalogPlugins = Join-Path $root "plugins-catalog"
    New-Item -ItemType Directory -Force -Path $catalogPlugins | Out-Null
    $catalogCache = Join-Path $root "catalog-cache"
    $catConfig = @{
        version = 1
        app = @{
            data_dir     = $data
            plugins_dir  = $catalogPlugins
            catalog      = @{
                cache_dir = $catalogCache
                sources   = @(@{ name = "local"; url = "file:///" + ($derivedCat -replace "\\", "/") })
            }
        }
    }
    $catConfigPath = Join-Path $root "config-catalog.json"
    ($catConfig | ConvertTo-Json -Depth 10) | Set-Content -LiteralPath $catConfigPath -Encoding UTF8

    & $mow --config $catConfigPath plugin catalog refresh
    if ($LASTEXITCODE -ne 0) { throw "plugin catalog refresh failed" }
    $search = & $mow --config $catConfigPath plugin search
    if ($LASTEXITCODE -ne 0) { throw "plugin search failed" }
    if (($search -join "`n") -notmatch "ssh|docker|ai|pve") { throw "no known plugin in search output" }
    & $mow --config $catConfigPath plugin install ssh
    if ($LASTEXITCODE -ne 0) { throw "plugin install ssh (catalog) failed" }
    $list = & $mow --config $catConfigPath plugin list
    if ($LASTEXITCODE -ne 0) { throw "plugin list failed" }
    if (($list -join "`n") -notmatch "ssh") { throw "installed plugin ssh missing from list" }
    & $mow --config $catConfigPath plugin uninstall ssh --purge
    if ($LASTEXITCODE -ne 0) { throw "plugin uninstall failed" }
    $sshDir = Join-Path $catalogPlugins "ssh"
    if (Test-Path -LiteralPath $sshDir) { throw "ssh dir should have been removed" }
    Write-Output "catalog install smoke passed"
}
finally {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
