# Prints the image contents windows-make-shell's three Make legs (pr.yml) and
# their toolchain saver (main.yml) rely on, so a Blacksmith image change shows
# up here first: every GNU Make on the image, the preinstalled MSYS2 and its
# package database, any Cygwin root, Chocolatey and Git for Windows.
#
# Never fails and never touches the network: a missing tool is reported, and
# the step that needs it fails later with its own message. It runs no `go`:
# the image's Go would download go.mod's toolchain first.
$ErrorActionPreference = 'Continue'

function Show-Group {
    param([Parameter(Mandatory)][string] $Title, [Parameter(Mandatory)][scriptblock] $Body)

    Write-Output "::group::$Title"
    try {
        & $Body
    } catch {
        Write-Output "error: $($_.Exception.Message)"
    }
    Write-Output '::endgroup::'
}

function Get-FirstLine {
    param([Parameter(Mandatory)][string] $Exe, [string[]] $Arguments = @('--version'))

    try {
        $first = @(& $Exe @Arguments 2>&1 | Select-Object -First 1)
        if ($first.Count -eq 0) { return '(no output)' }
        return [string] $first[0]
    } catch {
        return "error: $($_.Exception.Message)"
    }
}

Show-Group 'Image' {
    Write-Output "ImageOS=$env:ImageOS ImageVersion=$env:ImageVersion"
    Write-Output ([Environment]::OSVersion.VersionString)
}

Show-Group 'GNU Make on the image' {
    $image = 'C:\mingw64\bin\mingw32-make.exe'
    if (Test-Path -LiteralPath $image -PathType Leaf) {
        $hash = (Get-FileHash -LiteralPath $image -Algorithm SHA256).Hash.ToLowerInvariant()
        Write-Output "${image}: $(Get-FirstLine $image) (sha256 $hash)"
    } else {
        Write-Output "${image}: absent"
    }
    foreach ($name in 'make.exe', 'mingw32-make.exe', 'gmake.exe') {
        foreach ($command in @(Get-Command $name -All -CommandType Application -ErrorAction SilentlyContinue)) {
            Write-Output "$($command.Source) (on PATH): $(Get-FirstLine $command.Source)"
        }
    }
}

Show-Group 'Chocolatey' {
    $choco = Get-Command choco.exe -ErrorAction SilentlyContinue
    if ($choco) { Write-Output "$($choco.Source): $(Get-FirstLine $choco.Source)" } else { Write-Output 'choco: absent' }
}

Show-Group 'MSYS2 (C:\msys64)' {
    $pacman = 'C:\msys64\usr\bin\pacman.exe'
    if (-not (Test-Path -LiteralPath $pacman -PathType Leaf)) {
        Write-Output 'C:\msys64: no pacman.exe'
        return
    }
    Get-ChildItem -LiteralPath 'C:\msys64\var\lib\pacman\sync' -File -ErrorAction SilentlyContinue |
        ForEach-Object { "sync db $($_.Name): $($_.Length) bytes, $($_.LastWriteTimeUtc.ToString('u'))" }
    Write-Output "SigLevel: $((& 'C:\msys64\usr\bin\pacman-conf.exe' SigLevel 2>&1) -join ' ')"
    $installed = @(& $pacman -Q 2>&1)
    Write-Output "$($installed.Count) packages installed:"
    $installed
}

Show-Group 'Cygwin roots' {
    foreach ($root in 'C:\cygwin', 'C:\cygwin64', 'C:\tools\cygwin') {
        Write-Output "${root}: $(if (Test-Path -LiteralPath "$root\bin\cygwin1.dll") { 'present' } else { 'absent' })"
    }
}

Show-Group 'Git for Windows' {
    $git = Get-Command git.exe -ErrorAction SilentlyContinue
    if ($git) { Write-Output "$($git.Source): $(Get-FirstLine $git.Source)" } else { Write-Output 'git: absent' }
}

exit 0
