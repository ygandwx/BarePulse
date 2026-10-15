param(
    [string]$Php = $(if ($env:PHP_BIN) { $env:PHP_BIN } else { 'php' }),
    [string]$ExtensionDirectory = $env:PHP_EXTENSION_DIR
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$phpArgs = @()
if ($ExtensionDirectory) {
    $phpArgs += @('-d', "extension_dir=$ExtensionDirectory", '-d', 'extension=curl')
}
Push-Location $repo
try {
    & $Php @phpArgs -r 'if (!extension_loaded("curl")) { fwrite(STDERR, "PHP cURL extension required\n"); exit(1); }'
    if ($LASTEXITCODE -ne 0) { throw 'PHP cURL prerequisite failed' }
    Get-ChildItem -LiteralPath 'integrations/whmcs' -Recurse -Filter '*.php' | ForEach-Object {
        & $Php @phpArgs -l $_.FullName
        if ($LASTEXITCODE -ne 0) { throw "PHP lint failed: $($_.FullName)" }
    }
    & $Php @phpArgs integrations/whmcs/tests/contract.php
    if ($LASTEXITCODE -ne 0) { throw 'WHMCS stub contract failed' }
    & $Php @phpArgs integrations/whmcs/tests/http_contract.php
    if ($LASTEXITCODE -ne 0) { throw 'WHMCS loopback HTTP contract failed' }
    Write-Output 'WHMCS offline verification PASS (licensed WHMCS execution is separate).'
} finally {
    Pop-Location
}
