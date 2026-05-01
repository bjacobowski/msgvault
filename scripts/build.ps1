# Dev build script for Windows
# Usage: powershell -File scripts/build.ps1
#
# Sets up the MSYS2 toolchain environment (ucrt64 preferred, mingw64
# fallback) and the cgo flags arrow-go/v18 needs under MinGW 15, then
# delegates the actual build to `make build` so version stamping, tags,
# and the msgvault-omgnos output name stay defined in one place.
#
# Requirements (install under MSYS2):
#   pacman -S make mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-sqlite3

$ErrorActionPreference = 'Stop'

# Append a cgo flag only when missing, so users with their own
# CGO_CFLAGS/CGO_LDFLAGS (e.g. a custom SQLite include path) don't get
# them silently overwritten.
function Add-CgoFlag([string]$var, [string]$flag) {
    $existing = [Environment]::GetEnvironmentVariable($var, 'Process')
    if (-not $existing) {
        [Environment]::SetEnvironmentVariable($var, $flag, 'Process')
    } elseif ($existing -notmatch [regex]::Escape($flag)) {
        [Environment]::SetEnvironmentVariable($var, "$existing $flag", 'Process')
    }
}

# Detect MSYS2 toolchain. Prefer ucrt64 over mingw64 — newer CRT, better
# Unicode handling, what the project standardizes on.
$msys2Bin = $null
if (Test-Path "C:\msys64\ucrt64\include\sqlite3.h") {
    Add-CgoFlag "CGO_CFLAGS" "-IC:/msys64/ucrt64/include"
    $msys2Bin = "C:\msys64\ucrt64\bin"
} elseif (Test-Path "C:\msys64\mingw64\include\sqlite3.h") {
    Add-CgoFlag "CGO_CFLAGS" "-IC:/msys64/mingw64/include"
    $msys2Bin = "C:\msys64\mingw64\bin"
} else {
    Write-Host "No MSYS2 sqlite3 headers found. Install with:" -ForegroundColor Red
    Write-Host "  pacman -S mingw-w64-ucrt-x86_64-sqlite3" -ForegroundColor Red
    exit 1
}
if ((Test-Path "$msys2Bin\gcc.exe") -and ($env:Path -notlike "*$msys2Bin*")) {
    $env:Path = "$msys2Bin;$env:Path"
}

# arrow-go/v18 uses plain `inline` helpers (ArrowArrayIsReleased et al.)
# that MinGW 15 leaves undefined at link time. -fgnu89-inline forces an
# external definition; --allow-multiple-definition then tells ld to keep
# the first of the resulting duplicates in every TU.
Add-CgoFlag "CGO_CFLAGS" "-fgnu89-inline"
Add-CgoFlag "CGO_LDFLAGS" "-Wl,--allow-multiple-definition"

# Find make: MSYS2 ships GNU make as `make` if installed, sometimes also
# as `mingw32-make`. Prefer plain `make`.
$makeCmd = $null
foreach ($candidate in @('make', 'mingw32-make')) {
    if (Get-Command $candidate -ErrorAction SilentlyContinue) {
        $makeCmd = $candidate
        break
    }
}
if (-not $makeCmd) {
    Write-Host "GNU make not found on PATH. Install with: pacman -S make" -ForegroundColor Red
    exit 1
}

Write-Host "Building via $makeCmd build..."
& $makeCmd build
if ($LASTEXITCODE -ne 0) {
    Write-Host "Build failed." -ForegroundColor Red
    exit 1
}
Write-Host "Built: msgvault-omgnos.exe" -ForegroundColor Green
