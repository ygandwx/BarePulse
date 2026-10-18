#Requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$PostgresBin,
    [Parameter(Mandatory)][string]$SourceDsn,
    [Parameter(Mandatory)][ValidatePattern('^[a-z][a-z0-9_]{2,62}$')][string]$RestoreDatabase,
    [Parameter(Mandatory)][string]$MediaPath,
    [Parameter(Mandatory)][switch]$WritersStopped,
    [string]$OutputDirectory,
    [ValidateRange(30, 3600)][int]$CommandTimeoutSeconds = 300
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not $WritersStopped) { throw 'Stop all application, worker, gateway and integration writers before passing -WritersStopped. This script does not stop them.' }
if ($env:ALLOW_REAL_DEVICES -eq 'true' -or $env:ALLOW_DEVICE_WRITES -eq 'true') {
    throw 'Run this offline rehearsal with ALLOW_REAL_DEVICES=false and ALLOW_DEVICE_WRITES=false.'
}
$sourceUri = $null
if (-not [Uri]::TryCreate($SourceDsn, [UriKind]::Absolute, [ref]$sourceUri) -or $sourceUri.Scheme -notin @('postgres', 'postgresql')) {
    throw 'SourceDsn must be an absolute PostgreSQL URI.'
}
if ($sourceUri.Host -notin @('localhost', '127.0.0.1', '[::1]', '::1')) {
    throw 'This offline rehearsal accepts only a loopback PostgreSQL host.'
}
if ([Uri]::UnescapeDataString($sourceUri.UserInfo).Contains(':') -or $sourceUri.Query -match '(?i)(?:^|[?&])password=') {
    throw 'Do not put a password in SourceDsn; use a protected PGPASSFILE or the local PostgreSQL authentication configuration.'
}
foreach ($part in $sourceUri.Query.TrimStart('?').Split('&', [StringSplitOptions]::RemoveEmptyEntries)) {
    $decoded = [Uri]::UnescapeDataString($part)
    if ($decoded -notmatch '^sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$' -and $decoded -notmatch '^connect_timeout=[0-9]+$') {
        throw 'SourceDsn query parameters are restricted to sslmode and connect_timeout; connection-target overrides are forbidden.'
    }
}
$sourceDatabase = [Uri]::UnescapeDataString($sourceUri.AbsolutePath.TrimStart('/'))
if (-not $sourceDatabase -or $sourceDatabase.Contains('/') -or $sourceUri.Fragment) { throw 'SourceDsn must identify exactly one database.' }
if ($sourceDatabase -eq $RestoreDatabase) { throw 'RestoreDatabase must differ from the source database.' }

$pgBin = (Resolve-Path -LiteralPath $PostgresBin).Path
$executables = @{}
foreach ($name in @('psql', 'pg_dump', 'pg_restore', 'createdb')) {
    $path = Join-Path $pgBin "$name.exe"
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing PostgreSQL binary: $name.exe" }
    $executables[$name] = $path
}
$sourceMedia = (Resolve-Path -LiteralPath $MediaPath).Path
$mediaRoot = Get-Item -LiteralPath $sourceMedia
if (-not $mediaRoot.PSIsContainer -or ($mediaRoot.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'MediaPath must be an existing physical directory, not a link.' }
if (-not $OutputDirectory) {
    $repository = Split-Path -Parent $PSScriptRoot
    $OutputDirectory = Join-Path $repository ('work/backup-restore-' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
}
$output = [IO.Path]::GetFullPath($OutputDirectory)
$mediaPrefix = $sourceMedia.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
if ($output.Equals($sourceMedia, [StringComparison]::OrdinalIgnoreCase) -or $output.StartsWith($mediaPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputDirectory must not be inside the source media directory.'
}
if (Test-Path -LiteralPath $output) { throw 'OutputDirectory already exists; choose a new path. Nothing will be overwritten.' }
$null = New-Item -ItemType Directory -Path $output
$mediaBackup = Join-Path $output 'media'
$null = New-Item -ItemType Directory -Path $mediaBackup
$dumpPath = Join-Path $output 'barepulse.dump'
$reportPath = Join-Path $output 'verification.json'
$restoreBuilder = [UriBuilder]::new($sourceUri)
$restoreBuilder.Path = '/' + $RestoreDatabase
$restoreDsn = $restoreBuilder.Uri.AbsoluteUri
$report = [ordered]@{
    status = 'IN_PROGRESS'; started_at_utc = [DateTime]::UtcNow.ToString('o')
    source_database = $sourceDatabase; restore_database = $RestoreDatabase
    source_writers_stopped_acknowledged = $true; restore_database_created = $false
    allow_real_devices = $false; allow_device_writes = $false
    source_unchanged = $false; database_verified = $false; media_verified = $false
    snapshot_id = $null; dump_sha256 = $null; media_files = 0; orphan_media_files = 0
    protected_sessions = 0; unresolved_power_tasks = 0; gateway_connections = 0
    failure = $null
}
$exporter = $null
$exporterErrors = $null
$script:commandIndex = 0

function New-PGStartInfo([string]$Executable, [string[]]$Arguments) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $Executable
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $info.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    $info.Environment['PGAPPNAME'] = 'barepulse_offline_restore_check'
    $info.Environment['PGCLIENTENCODING'] = 'UTF8'
    foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
    return $info
}

function Invoke-PG([string]$Tool, [string[]]$Arguments, [string]$InputText = '') {
    $script:commandIndex++
    $logPrefix = Join-Path $output ('{0:D2}-{1}' -f $script:commandIndex, $Tool)
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = New-PGStartInfo $executables[$Tool] $Arguments
    try {
        if (-not $process.Start()) { throw "Could not start $Tool." }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if ($InputText) { $process.StandardInput.Write($InputText) }
        $process.StandardInput.Close()
        if (-not $process.WaitForExit($CommandTimeoutSeconds * 1000)) {
            $process.Kill($true)
            throw "$Tool exceeded the command timeout; only this script's child process was stopped."
        }
        $textOutput = $stdout.GetAwaiter().GetResult()
        $errorOutput = $stderr.GetAwaiter().GetResult()
        [IO.File]::WriteAllText($logPrefix + '.stderr.txt', $errorOutput, [Text.UTF8Encoding]::new($false))
        if ($process.ExitCode -ne 0) { throw "$Tool failed (exit $($process.ExitCode)); see the private stderr file in OutputDirectory." }
        return $textOutput.Trim()
    }
    finally { $process.Dispose() }
}

function Invoke-SQL([string]$Dsn, [string]$Sql) {
    return Invoke-PG 'psql' @('--no-psqlrc', '--no-password', '--quiet', '--tuples-only', '--no-align', '--set=ON_ERROR_STOP=1', '--dbname', $Dsn) ($Sql + "`n")
}

function Assert-NoSourceClients([int]$ExporterPid = 0) {
    $clients = Invoke-SQL $SourceDsn "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND pid<>pg_backend_pid() AND pid<>$ExporterPid;"
    if ($clients -ne '0') { throw 'Other source-database clients are connected. Stop all writers and close their pools before retrying with a new output directory.' }
}

function Get-MediaManifest([string]$Directory) {
    $entries = @()
    foreach ($item in @(Get-ChildItem -LiteralPath $Directory -Force | Sort-Object Name)) {
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Media directories must contain regular files only; nested directories and links require separate reviewed backup handling.' }
        $entries += [ordered]@{ name = $item.Name; size_bytes = $item.Length; sha256 = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    return ,$entries
}

function Assert-MediaMatchesDatabase($Manifest, $Files) {
    $lookup = @{}
    foreach ($file in $Files) { $lookup[$file.name] = $file }
    foreach ($asset in $Manifest.media) {
        if ($asset.id -notmatch '^[0-9a-f]{48}$') { throw 'Media row has a non-platform identifier; refusing to construct a filesystem path.' }
        $name = $asset.id + '.iso'
        if (-not $lookup.ContainsKey($name)) { throw 'An active database media asset is missing from the media directory.' }
        $file = $lookup[$name]
        if ([long]$file.size_bytes -ne [long]$asset.size_bytes -or $file.sha256 -ne $asset.sha256) { throw 'Media bytes do not match the size/SHA256 recorded in the database.' }
    }
}

# Hashes include the entire canonical JSON row, sorted with C collation, so a
# state/ownership/lease change cannot hide behind an unchanged row count.
$tables = @(
    'schema_migrations', 'users', 'auth_sessions', 'servers', 'devices', 'bindings',
    'operation_tasks', 'audit_events', 'traffic_accounts', 'traffic_members', 'traffic_sources',
    'traffic_source_members', 'traffic_samples', 'traffic_ledger', 'traffic_adjustments', 'traffic_alerts',
    'console_sessions', 'console_tickets', 'console_access', 'media_assets',
    'mock_console_state', 'gateway_connections', 'integrations', 'service_mappings',
    'integration_nonces', 'portal_grants'
)
$tableQueries = foreach ($table in $tables) {
    "SELECT '$table' AS table_name,count(*) AS row_count,encode(sha256(convert_to(COALESCE(string_agg(to_jsonb(t)::text,E'\n' ORDER BY to_jsonb(t)::text COLLATE `"C`"),''),'UTF8')),'hex') AS sha256 FROM public.$table t"
}
$manifestSQL = @"
WITH checks AS (
$($tableQueries -join "`nUNION ALL`n")
)
SELECT jsonb_build_object(
 'tables',(SELECT jsonb_object_agg(table_name,jsonb_build_object('rows',row_count,'sha256',sha256)) FROM checks),
 'protected_sessions',(SELECT count(*) FROM public.console_sessions WHERE state='protected'),
 'unresolved_power_tasks',(SELECT count(*) FROM public.operation_tasks WHERE status IN ('running','verifying','unknown')),
 'gateway_connections',(SELECT count(*) FROM public.gateway_connections),
 'media',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'size_bytes',size_bytes::text,'sha256',sha256,'state',state) ORDER BY id) FROM public.media_assets WHERE state<>'deleted'),'[]'::jsonb)
)::text;
"@

function Get-DatabaseManifest([string]$Dsn, [string]$Snapshot = '') {
    $snapshotStatement = ''
    if ($Snapshot) {
        if ($Snapshot -notmatch '^[0-9A-Fa-f-]+$') { throw 'Unexpected exported snapshot identifier.' }
        $snapshotStatement = "SET TRANSACTION SNAPSHOT '$Snapshot';"
    }
    $sql = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;`n$snapshotStatement`nSET LOCAL timezone='UTC';`n$manifestSQL`nCOMMIT;"
    $raw = Invoke-SQL $Dsn $sql
    return $raw | ConvertFrom-Json -AsHashtable
}

function Canonical-JSON($Value) { return ConvertTo-Json -InputObject $Value -Depth 30 -Compress }
function Save-JSON([string]$Path, $Value) { [IO.File]::WriteAllText($Path, (ConvertTo-Json -InputObject $Value -Depth 30), [Text.UTF8Encoding]::new($false)) }

try {
    Write-Host 'Checking the stopped source and confirming that the restore database is new.'
    Assert-NoSourceClients
    $exists = Invoke-SQL $SourceDsn "SELECT count(*) FROM pg_database WHERE datname='$RestoreDatabase';"
    if ($exists -ne '0') { throw 'RestoreDatabase already exists. This script never overwrites, cleans or drops a database.' }

    $exporter = [Diagnostics.Process]::new()
    $exporter.StartInfo = New-PGStartInfo $executables['psql'] @('--no-psqlrc', '--no-password', '--quiet', '--tuples-only', '--no-align', '--set=ON_ERROR_STOP=1', '--dbname', $SourceDsn)
    if (-not $exporter.Start()) { throw 'Could not start the read-only snapshot exporter.' }
    $exporterErrors = $exporter.StandardError.ReadToEndAsync()
    $exporter.StandardInput.WriteLine("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;`nSELECT 'BAREPULSE_SNAPSHOT|' || pg_export_snapshot() || '|' || pg_backend_pid();")
    $exporter.StandardInput.Flush()
    $snapshotLine = $exporter.StandardOutput.ReadLineAsync().WaitAsync([TimeSpan]::FromSeconds(30)).GetAwaiter().GetResult()
    if ($snapshotLine -notmatch '^BAREPULSE_SNAPSHOT\|([0-9A-Fa-f-]+)\|([0-9]+)$') { throw 'Could not obtain a PostgreSQL transaction snapshot.' }
    $snapshot = $Matches[1]
    $exporterPid = [int]$Matches[2]
    $report.snapshot_id = $snapshot

    $sourceManifest = Get-DatabaseManifest $SourceDsn $snapshot
    Save-JSON (Join-Path $output 'source-manifest.json') $sourceManifest
    $sourceFiles = Get-MediaManifest $sourceMedia
    Assert-MediaMatchesDatabase $sourceManifest $sourceFiles
    Save-JSON (Join-Path $output 'source-media.json') $sourceFiles

    Write-Host 'Dumping the exported transaction snapshot and copying stopped media files.'
    $null = Invoke-PG 'pg_dump' @('--no-password', '--format=custom', '--no-owner', '--no-acl', '--snapshot', $snapshot, '--file', $dumpPath, '--dbname', $SourceDsn)
    foreach ($file in $sourceFiles) { Copy-Item -LiteralPath (Join-Path $sourceMedia $file.name) -Destination (Join-Path $mediaBackup $file.name) }
    $copiedFiles = Get-MediaManifest $mediaBackup
    if ((Canonical-JSON $sourceFiles) -ne (Canonical-JSON $copiedFiles)) { throw 'Copied media hashes differ from the stopped source.' }

    Assert-NoSourceClients $exporterPid
    $sourceAfter = Get-DatabaseManifest $SourceDsn
    $mediaAfter = Get-MediaManifest $sourceMedia
    if ((Canonical-JSON $sourceManifest) -ne (Canonical-JSON $sourceAfter) -or (Canonical-JSON $sourceFiles) -ne (Canonical-JSON $mediaAfter)) {
        throw 'Source changed during backup. Discard this rehearsal result and repeat after establishing a real stop-write window.'
    }
    $report.source_unchanged = $true

    Write-Host 'Creating a new restore database and restoring without cleaning or replacing any database.'
    $null = Invoke-PG 'createdb' @('--no-password', '--maintenance-db', $SourceDsn, '--template=template0', '--encoding=UTF8', '--', $RestoreDatabase)
    $report.restore_database_created = $true
    $null = Invoke-PG 'pg_restore' @('--no-password', '--exit-on-error', '--single-transaction', '--no-owner', '--no-acl', '--dbname', $restoreDsn, $dumpPath)
    $restored = Get-DatabaseManifest $restoreDsn
    Save-JSON (Join-Path $output 'restore-manifest.json') $restored
    if ((Canonical-JSON $sourceManifest) -ne (Canonical-JSON $restored)) { throw 'Restored table counts, fingerprints, task/protection state or media metadata differ.' }
    Assert-MediaMatchesDatabase $restored $copiedFiles

    $report.database_verified = $true
    $report.media_verified = $true
    $report.media_files = $sourceFiles.Count
    $report.orphan_media_files = $sourceFiles.Count - @($sourceManifest.media).Count
    $report.protected_sessions = $sourceManifest.protected_sessions
    $report.unresolved_power_tasks = $sourceManifest.unresolved_power_tasks
    $report.gateway_connections = $sourceManifest.gateway_connections
    $report.dump_sha256 = (Get-FileHash -LiteralPath $dumpPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $report.status = 'PASS'
    # This file deliberately does not enable a profile, collector or real device.
    $restoreEnvironment = "DATABASE_URL=$restoreDsn`nALLOW_REAL_DEVICES=false`nALLOW_DEVICE_WRITES=false`nMEDIA_DIR=$mediaBackup`n"
    [IO.File]::WriteAllText((Join-Path $output 'restore.env'), $restoreEnvironment, [Text.UTF8Encoding]::new($false))
}
catch {
    $report.status = 'FAIL'
    $report.failure = $_.Exception.Message
    throw
}
finally {
    if ($null -ne $exporter) {
        try {
            if (-not $exporter.HasExited) {
                $exporter.StandardInput.WriteLine('ROLLBACK;')
                $exporter.StandardInput.Close()
                if (-not $exporter.WaitForExit(10000)) { $exporter.Kill($true) }
            }
            if ($null -ne $exporterErrors) { [IO.File]::WriteAllText((Join-Path $output 'snapshot-exporter.stderr.txt'), $exporterErrors.GetAwaiter().GetResult(), [Text.UTF8Encoding]::new($false)) }
        }
        finally { $exporter.Dispose() }
    }
    $report['finished_at_utc'] = [DateTime]::UtcNow.ToString('o')
    Save-JSON $reportPath $report
}

Write-Host "PASS: isolated database and media restored and verified. Report: $reportPath"
Write-Host 'The restore database, dump and media are retained. No application was started; device access remains disabled.'
[pscustomobject]$report
