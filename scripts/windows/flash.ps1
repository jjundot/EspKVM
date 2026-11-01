param(
    [Parameter(Mandatory=$true)][string]$Package,
    [Parameter(Mandatory=$true)][ValidatePattern('^COM[1-9][0-9]*$')][string]$Port,
    [string]$Python = 'python'
)
$ErrorActionPreference = 'Stop'
$packageRoot = (Resolve-Path -LiteralPath $Package).Path
$manifest = Get-Content -Raw -LiteralPath (Join-Path $packageRoot 'manifest.json') | ConvertFrom-Json
if ($manifest.version -ne 1 -or $manifest.chip -ne 'esp32s3' -or @($manifest.files).Count -eq 0) { throw 'Invalid flash manifest' }
if ($manifest.flash_settings.flash_mode -notin @('dio','dout','qio','qout') -or
    $manifest.flash_settings.flash_freq -notin @('20m','26m','40m','80m','120m') -or
    $manifest.flash_settings.flash_size -notin @('1MB','2MB','4MB','8MB','16MB','32MB','64MB','128MB','keep','detect')) { throw 'Invalid flash settings' }
$staging = Join-Path ([IO.Path]::GetTempPath()) ('espkvm-flash-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $staging | Out-Null
try {
    $arguments = @('-m','esptool','--chip','esp32s3','--port',$Port,'write_flash',
        '--flash_mode',$manifest.flash_settings.flash_mode,
        '--flash_freq',$manifest.flash_settings.flash_freq,
        '--flash_size',$manifest.flash_settings.flash_size)
    $offsets = @{}
    foreach ($entry in $manifest.files) {
        if ($entry.offset -notmatch '^0x[0-9a-fA-F]{1,8}$' -or $offsets.ContainsKey($entry.offset)) { throw 'Invalid or duplicate offset' }
        $offsets[$entry.offset] = $true
        if ([IO.Path]::GetFileName($entry.file) -ne $entry.file -or $entry.file -notmatch '^[a-zA-Z0-9_.-]+\.bin$') { throw 'Unsafe package filename' }
        $source = Join-Path $packageRoot $entry.file
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash -ine $entry.sha256) { throw 'Firmware SHA256 mismatch' }
        $destination = Join-Path $staging $entry.file
        Copy-Item -LiteralPath $source -Destination $destination
        $arguments += @($entry.offset, $destination)
    }
    & $Python @arguments
    if ($LASTEXITCODE -ne 0) { throw 'esptool flash failed' }
} finally {
    $resolved = [IO.Path]::GetFullPath($staging)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
        [IO.Path]::GetFileName($resolved) -notmatch '^espkvm-flash-[0-9a-f]{32}$') { throw 'Refusing cleanup outside task staging directory' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
