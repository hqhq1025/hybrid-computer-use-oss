#Requires -Version 7.0

param(
    [Parameter(Mandatory)][string]$Directory,
    [string]$LlvmReadobj = "llvm-readobj"
)

$ErrorActionPreference = "Stop"
$check = Join-Path $PSScriptRoot "check-input-queue.ps1"
& $check -Directory $Directory -LlvmReadobj $LlvmReadobj
$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("input-queue-package-test-" + [Guid]::NewGuid().ToString("N"))
[void](New-Item -ItemType Directory -Path $temporary)
try {
    foreach ($architecture in @("x86", "x64", "arm64")) {
        foreach ($suffix in @("exe", "dll")) {
            Copy-Item -LiteralPath (Join-Path $Directory "computer-use-input-queue-$architecture.$suffix") -Destination $temporary
        }
    }
    $missing = "computer-use-input-queue-x86.exe"
    Remove-Item -LiteralPath (Join-Path $temporary $missing)
    $rejected = $false
    try { & $check -Directory $temporary -LlvmReadobj $LlvmReadobj | Out-Null }
    catch {
        if ($_.Exception.Message -notlike "Input queue package is missing *") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Package validation accepted a missing x86 helper" }
    Copy-Item -LiteralPath (Join-Path $Directory $missing) -Destination $temporary

    Copy-Item -LiteralPath (Join-Path $Directory "computer-use-input-queue-x64.dll") `
        -Destination (Join-Path $temporary "computer-use-input-queue-arm64.dll") -Force
    $rejected = $false
    try { & $check -Directory $temporary -LlvmReadobj $LlvmReadobj | Out-Null }
    catch {
        if ($_.Exception.Message -notlike "Wrong PE architecture *") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Package validation accepted an x64 DLL as arm64" }
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force
}
Write-Output "Package validation rejects missing and mismatched native artifacts"
