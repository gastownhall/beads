<#
.SYNOPSIS
Installs Cygwin with GNU Make and Git from windows-make-shell's package cache,
without the network, or builds that cache.

.DESCRIPTION
Install (pr.yml's windows-make-shell, when actions/cache restored -CacheDir)
installs Cygwin into -Root from the cache alone, then writes root=<Root> to
the step's outputs. -CacheDir holds

  setup-x86_64.exe     Cygwin's installer. Used only with a valid Authenticode
                       signature by one of Cygwin's code-signing certificates
                       ($CygwinSignerSha256).
  packages\<mirror>\   One mirror's package tree, as setup downloads it:
                       x86_64\setup.zst, its detached signature
                       x86_64\setup.zst.sig, and the package archives.

setup runs with -L (install from the local package directory, no network).
Get-SignedPackageTree first makes sure the only index setup can read is
setup.zst, which setup verifies against setup.zst.sig with the key built into
it; setup then checks every package archive against setup.zst's SHA-512
before it installs anything.

Seed (main.yml's windows-make-shell-toolchains, on a cache miss) builds
-CacheDir from cygwin-install-action's online install: -SetupExe is the
installer the action verified, -PackageDir the package tree it downloaded.
setup leaves an unsigned setup.ini there, so Seed fetches the mirror's signed
setup.zst in its place, then proves the cache with an Install into a scratch
root. The job saves the cache only if that succeeds, so a mirror update
between the two downloads fails the seed instead of saving a cache whose
metadata names archives it lacks.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Install', 'Seed')][string] $Mode,
    [Parameter(Mandatory)][string] $CacheDir,
    [string] $Root,
    [string] $SetupExe,
    [string] $PackageDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Packages = 'git,make'
# The SHA-256 of Cygwin's code-signing certificates, as cygwin-install-action
# pins them for its own installer check (check-installer-sig, v6). Bump them
# together with that action.
$CygwinSignerSha256 = @(
    '2CE11DA3A675A9D631E06A28DDFD6F730B9CC6989B43BD30AD7CC79D219CF2BD',
    '3AB2B14363B58A1856D4902C737797C899118095F7025DF7832C1B52483865A0'
)
$DownloadAttempts = 3
$DownloadAttemptSeconds = 60
$BackoffSeconds = @(15, 30)

function Assert-CygwinSetup {
    param([Parameter(Mandatory)][string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "No Cygwin installer at $Path"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    $signer = '-'
    if ($signature.SignerCertificate) {
        $signer = $signature.SignerCertificate.GetCertHashString([Security.Cryptography.HashAlgorithmName]::SHA256)
    }
    if ($signature.Status -ne 'Valid' -or $signer -notin $CygwinSignerSha256) {
        throw "$Path is not Cygwin's signed installer (signature status $($signature.Status), signer certificate $signer)"
    }
}

# What setup trusts in a local package tree (-L -l <dir>), from its source: it
# reads <dir>\setup.rc first, whose extrakeys entry adds keys it accepts index
# signatures from; it takes the index from <dir>\x86_64 if that directory
# exists, else from <dir>\<mirror>\x86_64; there it reads the first non-empty
# file of setup.zst, setup.xz, setup.bz2 and setup.ini; and in local mode it
# installs from an index that has no .sig file without verifying it. So a
# cache is used only if its package root holds one mirror directory (not named
# x86_64) and nothing else, and that mirror's index directory holds no setup.*
# file but a non-empty setup.zst and its non-empty signature. -Force: setup
# sees hidden files too.
$SignedIndexFiles = @('setup.zst', 'setup.zst.sig')

# Returns the index directory of the only mirror under $PackageRoot after
# checking it, or throws.
function Get-SignedPackageTree {
    param([Parameter(Mandatory)][string] $PackageRoot)

    $rootFiles = @(Get-ChildItem -LiteralPath $PackageRoot -File -Force)
    if ($rootFiles.Count -ne 0) {
        throw "$PackageRoot holds $($rootFiles.Name -join ', ') beside its mirror directory; setup reads setup.rc from there, so nothing else may be there"
    }
    $mirrors = @(Get-ChildItem -LiteralPath $PackageRoot -Directory -Force)
    if ($mirrors.Count -ne 1) {
        throw "$PackageRoot holds $($mirrors.Count) mirror directories, want exactly 1"
    }
    if ($mirrors[0].Name -eq 'x86_64') {
        throw "$PackageRoot's mirror directory is named $($mirrors[0].Name), which setup would read as its index directory"
    }
    $arch = Join-Path $mirrors[0].FullName 'x86_64'
    if (-not (Test-Path -LiteralPath $arch -PathType Container)) {
        throw "$($mirrors[0].FullName) has no x86_64 index directory"
    }
    $indexFiles = @(Get-ChildItem -LiteralPath $arch -File -Force | Where-Object { $_.Name -like 'setup.*' })
    $unexpected = @($indexFiles | Where-Object { $_.Name -notin $SignedIndexFiles })
    if ($unexpected.Count -ne 0) {
        throw "$arch holds $($unexpected.Name -join ', '), which setup could read instead of the signed setup.zst"
    }
    foreach ($name in $SignedIndexFiles) {
        $file = @($indexFiles | Where-Object { $_.Name -eq $name })
        if ($file.Count -ne 1 -or $file[0].Length -eq 0) {
            throw "$arch must hold exactly one non-empty $name (it holds $($file.Count)), or setup would not verify the package index"
        }
    }
    return $arch
}

function Install-FromCache {
    param([Parameter(Mandatory)][string] $Cache, [Parameter(Mandatory)][string] $InstallRoot)

    $setup = Join-Path $Cache 'setup-x86_64.exe'
    $packageRoot = Join-Path $Cache 'packages'
    Assert-CygwinSetup -Path $setup
    $null = Get-SignedPackageTree -PackageRoot $packageRoot
    New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null

    # setup is a GUI program: piping its output makes PowerShell wait for it.
    & $setup @('-q', '-n', '-L', '-l', $packageRoot, '-R', $InstallRoot, '-P', $Packages) | Out-Default
    if ($LASTEXITCODE -ne 0) {
        throw "Cygwin setup exited $LASTEXITCODE"
    }
    $bash = Join-Path $InstallRoot 'bin\bash.exe'
    & $bash --noprofile --norc -c 'set -e; export PATH=/usr/bin; make --version >/dev/null; git --version >/dev/null; make --version | head -n 1; git --version'
    if ($LASTEXITCODE -ne 0) {
        throw "The Cygwin install in $InstallRoot has no working make or git"
    }
}

function Invoke-DownloadWithRetry {
    param([Parameter(Mandatory)][string] $Uri, [Parameter(Mandatory)][string] $OutFile)

    for ($attempt = 1; $attempt -le $DownloadAttempts; $attempt++) {
        try {
            Invoke-WebRequest -Uri $Uri -OutFile $OutFile -TimeoutSec $DownloadAttemptSeconds
            return
        } catch {
            Write-Host "::warning::Downloading $Uri, attempt $attempt of $DownloadAttempts, failed: $($_.Exception.Message)"
            if ($attempt -lt $DownloadAttempts) {
                Start-Sleep -Seconds $BackoffSeconds[$attempt - 1]
            }
        }
    }
    throw "Could not download $Uri"
}

function Save-PackageCache {
    Assert-CygwinSetup -Path $SetupExe
    $mirrors = @(Get-ChildItem -LiteralPath $PackageDir -Directory)
    if ($mirrors.Count -ne 1) {
        throw "$PackageDir holds $($mirrors.Count) mirror directories, want exactly 1"
    }

    Remove-Item -LiteralPath $CacheDir -Recurse -Force -ErrorAction SilentlyContinue
    $packageRoot = Join-Path $CacheDir 'packages'
    New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null
    Copy-Item -LiteralPath $SetupExe -Destination (Join-Path $CacheDir 'setup-x86_64.exe')
    Copy-Item -LiteralPath $mirrors[0].FullName -Destination $packageRoot -Recurse

    # setup names a mirror's directory after its escaped URL.
    $mirror = [Uri]::UnescapeDataString($mirrors[0].Name).TrimEnd('/')
    $arch = Join-Path (Join-Path $packageRoot $mirrors[0].Name) 'x86_64'
    foreach ($name in $SignedIndexFiles) {
        Invoke-DownloadWithRetry -Uri "$mirror/x86_64/$name" -OutFile (Join-Path $arch $name)
    }
    Remove-Item -LiteralPath (Join-Path $arch 'setup.ini') -Force -ErrorAction SilentlyContinue

    Install-FromCache -Cache $CacheDir -InstallRoot (Join-Path $env:RUNNER_TEMP 'cygwin-seed-check')
    Write-Output "Seeded $CacheDir from $mirror."
}

switch ($Mode) {
    'Install' {
        if (-not $Root) {
            throw 'Install needs -Root'
        }
        Install-FromCache -Cache $CacheDir -InstallRoot $Root
        Add-Content -LiteralPath $env:GITHUB_OUTPUT -Value "root=$Root"
    }
    'Seed' {
        if (-not $SetupExe -or -not $PackageDir) {
            throw 'Seed needs -SetupExe and -PackageDir'
        }
        Save-PackageCache
    }
}
