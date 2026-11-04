<#
.SYNOPSIS
Provides the native Windows GNU Make for windows-make-shell's native leg.

.DESCRIPTION
Install (pr.yml's windows-make-shell) puts make.exe, GNU Make 4.4.1 with a
native Windows MAKE_HOST (Windows32 or *-mingw32), in -Destination and
prepends that directory to the job's PATH, so the native exercise step's
`Get-Command make.exe` finds it. It takes the first of:

  1. The image's MinGW-w64 make, C:\mingw64\bin\mingw32-make.exe. GitHub's
     windows-2025 image, which Blacksmith's is built from, unpacks niXman's
     mingw-builds there (runner-images' Install-Mingw64.ps1). That
     mingw32-make.exe is GNU Make 4.4.1 for x86_64-w64-mingw32 and imports
     only system DLLs, so a copy runs from any directory. No network.
  2. Chocolatey's make 4.4.1 binary from the main-saved cache in -CacheDir,
     used only if its SHA-256 is $ChocolateyMakeSha256. No network.
  3. choco install make --version=4.4.1: up to $ChocolateyAttempts attempts of
     at most $ChocolateyAttemptSeconds seconds each, $BackoffSeconds apart.
     The make.exe it installs must have SHA-256 $ChocolateyMakeSha256 too, or
     the leg fails before that binary reaches PATH.

Seed (main.yml's windows-make-shell-toolchains, on a cache miss) downloads
Chocolatey's make 4.4.1 package, checks its make.exe against the same pin and
leaves it in -CacheDir for actions/cache/save.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Install', 'Seed')][string] $Mode,
    [Parameter(Mandatory)][string] $CacheDir,
    [string] $Destination = (Join-Path $env:RUNNER_TEMP 'native-make')
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ImageMake = 'C:\mingw64\bin\mingw32-make.exe'
# make.exe of Chocolatey's make 4.4.1 package (ezwinports'
# make-4.4.1-without-guile-w32-bin.zip, MAKE_HOST Windows32). Bump it together
# with the native cache key in pr.yml and main.yml.
$ChocolateyMakeSha256 = 'cc6dc291113dcbcc7735835acbfc23c52ed037e4e124ff2d7a0aeae6df563a9f'
$ChocolateyPackageUri = 'https://community.chocolatey.org/api/v2/package/make/4.4.1'
$ChocolateyAttempts = 3
$ChocolateyAttemptSeconds = 120
$BackoffSeconds = @(15, 30)

# Returns $null when $Make is GNU Make 4.4.1 with a native Windows MAKE_HOST
# (the leg's contract, the same MAKE_HOST check as the exercise step), else
# the reason it is not.
function Get-NativeMakeProblem {
    param([Parameter(Mandatory)][string] $Make)

    try {
        $versionLines = @(& $Make --version)
        if ($LASTEXITCODE -ne 0) { return "--version exited $LASTEXITCODE" }
        if ($versionLines.Count -eq 0 -or $versionLines[0] -ne 'GNU Make 4.4.1') {
            return "it reports '$($versionLines | Select-Object -First 1)', not GNU Make 4.4.1"
        }
        $probe = @(& $Make --no-print-directory -f NUL --eval '$(info MAKE_HOST=$(MAKE_HOST))' --eval 'noop:;' noop)
        if ($LASTEXITCODE -ne 0) { return "the MAKE_HOST probe exited $LASTEXITCODE" }
        $makeHost = [string] (@($probe | Where-Object { $_ -like 'MAKE_HOST=*' }) -replace '^MAKE_HOST=', '' | Select-Object -First 1)
        if ($makeHost -ne 'Windows32' -and $makeHost -notmatch '-mingw32$') { return "MAKE_HOST is '$makeHost'" }
        return $null
    } catch {
        return $_.Exception.Message
    }
}

# Copies $Candidate to $Target and returns whether the copy meets the leg's
# contract. Logs with Write-Host: anything written to the output stream would
# become part of the return value.
function Test-CopiedNativeMake {
    param([Parameter(Mandatory)][string] $Candidate, [Parameter(Mandatory)][string] $Target, [Parameter(Mandatory)][string] $Source)

    Copy-Item -LiteralPath $Candidate -Destination $Target -Force
    $problem = Get-NativeMakeProblem -Make $Target
    if ($problem) {
        Write-Host "::notice title=Native GNU Make::$Source ($Candidate) is not usable: $($problem -replace '\s+', ' ')"
        Remove-Item -LiteralPath $Target -Force
        return $false
    }
    return $true
}

function Invoke-ChocolateyInstall {
    $choco = (Get-Command choco.exe -ErrorAction Stop).Source
    for ($attempt = 1; $attempt -le $ChocolateyAttempts; $attempt++) {
        $process = Start-Process -FilePath $choco -ArgumentList @('install', 'make', '--version=4.4.1', '--yes', '--no-progress') -NoNewWindow -PassThru
        $null = $process.Handle # keeps ExitCode readable after the process exits
        if ($process.WaitForExit($ChocolateyAttemptSeconds * 1000)) {
            if ($process.ExitCode -eq 0) { return }
            Write-Host "::warning title=Native GNU Make::choco install make, attempt $attempt of $ChocolateyAttempts, exited $($process.ExitCode)."
        } else {
            $process.Kill($true)
            Write-Host "::warning title=Native GNU Make::choco install make, attempt $attempt of $ChocolateyAttempts, did not finish in ${ChocolateyAttemptSeconds}s."
        }
        if ($attempt -lt $ChocolateyAttempts) {
            Start-Sleep -Seconds $BackoffSeconds[$attempt - 1]
        }
    }
    throw "choco install make --version=4.4.1 failed $ChocolateyAttempts times"
}

# Returns where the make.exe now in $Destination came from.
function Install-NativeMake {
    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    $target = Join-Path $Destination 'make.exe'

    # Tier 1: the image's MinGW-w64 make.
    if (-not (Test-Path -LiteralPath $ImageMake -PathType Leaf)) {
        Write-Host "The image has no $ImageMake."
    } elseif (Test-CopiedNativeMake -Candidate $ImageMake -Target $target -Source 'The image') {
        return "the image ($ImageMake)"
    }

    # Tier 2: Chocolatey's binary from the main-saved cache, if it is the pinned one.
    $cached = Join-Path $CacheDir 'make.exe'
    if (-not (Test-Path -LiteralPath $cached -PathType Leaf)) {
        Write-Host "The main-saved cache has no $cached."
    } else {
        $hash = (Get-FileHash -LiteralPath $cached -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($hash -ne $ChocolateyMakeSha256) {
            Write-Host "::warning title=Native GNU Make::The cached make.exe has SHA-256 $hash, not the pinned $ChocolateyMakeSha256; not using it."
        } elseif (Test-CopiedNativeMake -Candidate $cached -Target $target -Source 'The main-saved cache') {
            return "the main-saved cache (Chocolatey's make 4.4.1, sha256 $hash)"
        }
    }

    # Tier 3: Chocolatey, over the network.
    Write-Host '::warning title=Native GNU Make::Neither the image nor the main-saved cache has a usable GNU Make 4.4.1; installing it from Chocolatey.'
    Invoke-ChocolateyInstall
    $root = if ($env:ChocolateyInstall) { $env:ChocolateyInstall } else { 'C:\ProgramData\chocolatey' }
    $installed = Join-Path $root 'lib\make\tools\install\bin\make.exe'
    if (-not (Test-Path -LiteralPath $installed -PathType Leaf)) {
        throw "choco install make succeeded but left no $installed"
    }
    $hash = (Get-FileHash -LiteralPath $installed -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -ne $ChocolateyMakeSha256) {
        throw "Chocolatey installed make.exe with SHA-256 $hash, not the pinned $ChocolateyMakeSha256; not putting it on PATH"
    }
    if (-not (Test-CopiedNativeMake -Candidate $installed -Target $target -Source 'Chocolatey')) {
        throw "Chocolatey's make at $installed is not GNU Make 4.4.1 with a native Windows MAKE_HOST"
    }
    return "Chocolatey ($installed)"
}

function Invoke-DownloadWithRetry {
    param([Parameter(Mandatory)][string] $Uri, [Parameter(Mandatory)][string] $OutFile)

    for ($attempt = 1; $attempt -le $ChocolateyAttempts; $attempt++) {
        try {
            Invoke-WebRequest -Uri $Uri -OutFile $OutFile -TimeoutSec $ChocolateyAttemptSeconds
            return
        } catch {
            Write-Host "::warning::Downloading $Uri, attempt $attempt of $ChocolateyAttempts, failed: $($_.Exception.Message)"
            if ($attempt -lt $ChocolateyAttempts) {
                Start-Sleep -Seconds $BackoffSeconds[$attempt - 1]
            }
        }
    }
    throw "Could not download $Uri"
}

function Save-ChocolateyMake {
    $work = Join-Path $env:RUNNER_TEMP 'native-make-seed'
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $work | Out-Null
    $package = Join-Path $work 'make.4.4.1.nupkg'
    Invoke-DownloadWithRetry -Uri $ChocolateyPackageUri -OutFile $package

    # A .nupkg is a zip archive. Read the one entry needed directly:
    # Expand-Archive wants a .zip name and trips over entries such as
    # [Content_Types].xml.
    Add-Type -AssemblyName System.IO.Compression.ZipFile
    $make = Join-Path $work 'make.exe'
    $archive = [IO.Compression.ZipFile]::OpenRead($package)
    try {
        $entry = $archive.GetEntry('tools/install/bin/make.exe')
        if (-not $entry) {
            throw "Chocolatey's make 4.4.1 package has no tools/install/bin/make.exe"
        }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $make, $true)
    } finally {
        $archive.Dispose()
    }
    $hash = (Get-FileHash -LiteralPath $make -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -ne $ChocolateyMakeSha256) {
        throw "Chocolatey's make 4.4.1 package holds make.exe with SHA-256 $hash, not the pinned $ChocolateyMakeSha256"
    }
    $problem = Get-NativeMakeProblem -Make $make
    if ($problem) {
        throw "Chocolatey's make 4.4.1 is not usable: $problem"
    }
    New-Item -ItemType Directory -Force -Path $CacheDir | Out-Null
    Copy-Item -LiteralPath $make -Destination (Join-Path $CacheDir 'make.exe') -Force
    Write-Output "Seeded $CacheDir with Chocolatey's make 4.4.1 (sha256 $hash)."
}

switch ($Mode) {
    'Install' {
        $source = Install-NativeMake
        Add-Content -LiteralPath $env:GITHUB_PATH -Value $Destination
        Write-Output "Native GNU Make from $source, first on PATH for the rest of the job as $(Join-Path $Destination 'make.exe')."
    }
    'Seed' {
        Save-ChocolateyMake
    }
}
