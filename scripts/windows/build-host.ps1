param([switch]$Test)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
Push-Location $root
try {
    cmake --preset host-windows
    if ($LASTEXITCODE -ne 0) { throw 'CMake configuration failed' }
    $targets = @('host')
    if ($Test) { $targets += @('test-host', 'test-protocol') }
    cmake --build --preset host-windows --target @targets
    if ($LASTEXITCODE -ne 0) { throw 'Host build failed' }
} finally { Pop-Location }
