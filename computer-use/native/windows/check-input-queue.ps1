#Requires -Version 7.0

param(
    [Parameter(Mandatory)][string]$Directory,
    [string]$LlvmReadobj = "llvm-readobj"
)

$ErrorActionPreference = "Stop"
$inspector = (Get-Command $LlvmReadobj -CommandType Application -ErrorAction Stop).Source
$machines = [ordered]@{ x86 = "I386"; x64 = "AMD64"; arm64 = "ARM64" }
foreach ($architecture in $machines.Keys) {
    foreach ($extension in @("exe", "dll")) {
        $path = Join-Path $Directory "computer-use-input-queue-$architecture.$extension"
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Input queue package is missing $path"
        }
        $inspection = & $inspector --file-headers --coff-imports --coff-exports $path
        if ($LASTEXITCODE -ne 0) {
            throw "Cannot inspect input queue binary: $path"
        }
        $text = $inspection -join "`n"
        $machine = [regex]::Match($text, '(?m)^\s*Machine: IMAGE_FILE_MACHINE_(\w+) ').Groups[1].Value
        if ($machine -ne $machines[$architecture]) {
            throw "Wrong PE architecture for ${path}: $machine"
        }
        $isDll = $text -match '(?m)^\s*IMAGE_FILE_DLL\s*\('
        if ($isDll -ne ($extension -eq "dll")) {
            throw "Wrong PE image type for $path"
        }
        $entry = [regex]::Match($text, '(?m)^\s*AddressOfEntryPoint: 0x([0-9A-Fa-f]+)').Groups[1].Value
        if (-not $entry -or [Convert]::ToUInt32($entry, 16) -eq 0) {
            throw "Input queue binary has no entry point: $path"
        }
        $exports = @([regex]::Matches($text, '(?ms)^Export \{.*?^\s*Name: ([^\r\n]+)') |
                     ForEach-Object { $_.Groups[1].Value.Trim() })
        if ($extension -eq "dll") {
            if ($exports.Count -ne 1 -or $exports[0] -cne "InputQueueGetMessage") {
                throw "Wrong input queue hook export for ${path}: $($exports -join ', ')"
            }
        } elseif ($exports.Count -ne 0) {
            throw "Input queue helper must not export DLL entry points: $path"
        }
        $imports = @([regex]::Matches($text, '(?ms)^Import \{.*?^\s*Name: ([^\r\n]+)') |
                     ForEach-Object { $_.Groups[1].Value.Trim().ToLowerInvariant() } | Sort-Object -Unique)
        $expected = if ($extension -eq "exe") {
            @("kernel32.dll", "shell32.dll", "user32.dll")
        } else {
            @("kernel32.dll", "user32.dll")
        }
        if ($imports.Count -ne $expected.Count -or (Compare-Object $imports $expected)) {
            throw "Unexpected input queue imports for ${path}: $($imports -join ', ')"
        }
        Write-Output "Validated $architecture $extension entry point, exports and imports"
    }
}
