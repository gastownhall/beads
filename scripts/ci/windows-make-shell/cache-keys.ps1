# Computes the variable parts of windows-make-shell's toolchain cache keys,
# for pr.yml's restores and main.yml's saver alike (both run this script, so
# the keys cannot drift apart):
#
#   msys2-sync-db  SHA-256 of the image's MSYS2 msys sync database. It pins
#                  every package version pacman resolves `git make` to on
#                  this image, so a new Blacksmith image gets new packages.
#                  Unset when the image has no database (the MSYS2 install
#                  step then fails with the reason).
#   month          The UTC month: the Cygwin package snapshot is refreshed
#                  once a month, never per commit.
$ErrorActionPreference = 'Stop'

$month = [DateTime]::UtcNow.ToString('yyyyMM', [Globalization.CultureInfo]::InvariantCulture)
Add-Content -LiteralPath $env:GITHUB_OUTPUT -Value "month=$month"
Write-Output "month=$month"

$syncDb = 'C:\msys64\var\lib\pacman\sync\msys.db'
if (Test-Path -LiteralPath $syncDb -PathType Leaf) {
    $hash = (Get-FileHash -LiteralPath $syncDb -Algorithm SHA256).Hash.ToLowerInvariant()
    Add-Content -LiteralPath $env:GITHUB_OUTPUT -Value "msys2-sync-db=$hash"
    Write-Output "msys2-sync-db=$hash"
} else {
    Write-Output "::warning title=No MSYS2 sync database::$syncDb is missing, so the MSYS2 package cache is skipped."
}
