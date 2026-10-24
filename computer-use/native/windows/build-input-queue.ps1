#Requires -Version 7.0

param(
    [Parameter(Mandatory)][string]$OutputDirectory,
    [string]$Clang = "clang",
    [string]$WindowsSysroot = ""
)

$ErrorActionPreference = "Stop"
$compiler = (Get-Command $Clang -CommandType Application -ErrorAction Stop).Source
$extension = if ($IsWindows) { ".exe" } else { "" }
$llvmReadobj = Join-Path (Split-Path $compiler) "llvm-readobj$extension"
if (-not (Test-Path -LiteralPath $llvmReadobj -PathType Leaf)) {
    throw "llvm-readobj must be installed beside Clang: $llvmReadobj"
}
if (-not $IsWindows -and [string]::IsNullOrWhiteSpace($WindowsSysroot)) {
    throw "Cross-compiling requires -WindowsSysroot with Windows SDK headers and target import libraries"
}
if ($WindowsSysroot) {
    $WindowsSysroot = (Resolve-Path -LiteralPath $WindowsSysroot).Path
    if (-not (Test-Path -LiteralPath (Join-Path $WindowsSysroot "include/windows.h"))) {
        throw "WindowsSysroot must contain the standard Windows SDK include/windows.h"
    }
}
[void](New-Item -ItemType Directory -Force -Path $OutputDirectory)
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
$sourceDirectory = Join-Path $PSScriptRoot "input-queue"
$targets = [ordered]@{
    x86 = "i686-unknown-windows-msvc"
    x64 = "x86_64-unknown-windows-msvc"
    arm64 = "aarch64-unknown-windows-msvc"
}
$stage = Join-Path $OutputDirectory (".input-queue-build-" + [Guid]::NewGuid().ToString("N"))
[void](New-Item -ItemType Directory -Path $stage)
try {
    foreach ($architecture in $targets.Keys) {
        $target = $targets[$architecture]
        $common = @("--target=$target", "-fuse-ld=lld", "-std=c11", "-O2",
                    "-ffreestanding", "-fno-stack-protector", "-Werror", "-nostdlib",
                    "-DUNICODE", "-D_UNICODE", "-Wl,/dynamicbase,/nxcompat")
        if ($WindowsSysroot) {
            $libraryDirectory = Join-Path $WindowsSysroot "lib/$target"
            foreach ($library in @("kernel32.lib", "user32.lib", "shell32.lib")) {
                if (-not (Test-Path -LiteralPath (Join-Path $libraryDirectory $library))) {
                    throw "Missing $architecture import library: $libraryDirectory/$library"
                }
            }
            $common += @("-isystem", (Join-Path $WindowsSysroot "include"), "-L$libraryDirectory")
        }
        $stem = Join-Path $stage "computer-use-input-queue-$architecture"
        & $compiler @common (Join-Path $sourceDirectory "input_queue_host.c") `
            "-Wl,/entry:mainCRTStartup,/subsystem:console" `
            "-Wl,kernel32.lib,user32.lib,shell32.lib" "-o" "$stem.exe"
        if ($LASTEXITCODE -ne 0) {
            throw "Input queue $architecture helper build failed with exit code $LASTEXITCODE"
        }
        & $compiler @common (Join-Path $sourceDirectory "input_queue_hook.c") `
            "-Wl,/dll,/entry:DllMain,kernel32.lib,user32.lib" "-o" "$stem.dll"
        if ($LASTEXITCODE -ne 0) {
            throw "Input queue $architecture DLL build failed with exit code $LASTEXITCODE"
        }
    }
    & (Join-Path $PSScriptRoot "check-input-queue.ps1") `
        -Directory $stage -LlvmReadobj $llvmReadobj
    foreach ($architecture in $targets.Keys) {
        foreach ($suffix in @("exe", "dll")) {
            $name = "computer-use-input-queue-$architecture.$suffix"
            Move-Item -LiteralPath (Join-Path $stage $name) -Destination (Join-Path $OutputDirectory $name) -Force
        }
    }
} finally {
    Remove-Item -LiteralPath $stage -Recurse -Force
}
Write-Output "Built Windows input queue helpers and hook DLLs for x86, x64, arm64"
