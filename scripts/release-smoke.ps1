param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [string]$Target = "windows",
    [string]$Arch = "amd64"
)
$ErrorActionPreference = "Stop"
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
    foreach ($id in @("ssh", "docker", "ai")) {
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
}
finally {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
