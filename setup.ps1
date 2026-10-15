#Requires -Version 5.1
<#
setup.ps1 — download and install a prebuilt fuckyourcli release (no Go toolchain).

    irm https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.ps1 | iex

Use install.ps1 instead when you want to install from the local sources
(it builds with go and installs from .\build).

Options (when run as a file; with `| iex` use the environment variables):
    -Version <tag>     install a specific version (0.1.0 or v0.1.0)
    -InstallDir <dir>  install directory (default: %LOCALAPPDATA%\Programs\fyc)
    -SystemInstall     install into %ProgramFiles%\fyc (needs an elevated shell)
    -DryRun            print what would be downloaded, install nothing
    -Force             reinstall even if the same version is already installed
    -NoSkills          do not install the bundled skills
    -NoPathUpdate      do not touch the user PATH
    -NoProfileUpdate   do not write the completion line into $PROFILE
    -SkipSandboxSetup  do not run 'fuckyourcli sandbox setup' (no UAC)
    -NoCompletion      do not install the PowerShell completion

Environment: FYC_VERSION, FYC_INSTALL_DIR, FYC_SYSTEM_INSTALL=1, FYC_FORCE=1,
FYC_NO_SKILLS=1, FYC_NO_PATH_UPDATE=1, FYC_NO_PROFILE_UPDATE=1, FYC_SKIP_SANDBOX_SETUP=1,
FYC_NO_COMPLETION=1.

Note: run this from a normal PowerShell. Inside fyc's own Windows sandbox the
system TLS stack (Schannel) is unavailable and the download would fail.
#>
[CmdletBinding()]
param(
    [string] $Version = $env:FYC_VERSION,
    [string] $InstallDir = $env:FYC_INSTALL_DIR,
    [switch] $SystemInstall,
    [switch] $DryRun,
    [switch] $Force,
    [switch] $NoSkills,
    [switch] $NoPathUpdate,
    [switch] $NoProfileUpdate,
    [switch] $SkipSandboxSetup,
    [switch] $NoCompletion
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Environment variables are the only way to pass options through `irm | iex`.
if ($env:FYC_SYSTEM_INSTALL -eq '1') { $SystemInstall = $true }
if ($env:FYC_FORCE -eq '1') { $Force = $true }
if ($env:FYC_NO_SKILLS -eq '1') { $NoSkills = $true }
if ($env:FYC_NO_PATH_UPDATE -eq '1') { $NoPathUpdate = $true }
if ($env:FYC_NO_PROFILE_UPDATE -eq '1') { $NoProfileUpdate = $true }
if ($env:FYC_SKIP_SANDBOX_SETUP -eq '1') { $SkipSandboxSetup = $true }
if ($env:FYC_NO_COMPLETION -eq '1') { $NoCompletion = $true }

# PowerShell 5.1 on older Windows still negotiates TLS 1.0 by default.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # nothing to do on platforms where the enum is unavailable
}

# Windows-only, like install.ps1: everything below relies on Windows APIs
# (PROCESSOR_ARCHITECTURE, UAC, ACLs).
if ($env:OS -ne 'Windows_NT') {
    throw 'setup.ps1 is intended for Windows. On Linux/macOS use setup.sh.'
}

$repo = 'eduardogiraudi/fyc'
$bin = 'fuckyourcli'
$appName = 'fyc'

function Get-SetupArch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'x86_64' }
        'ARM64' { return 'arm64' }
        default { throw "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
    }
}

function Test-IsAdmin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

$arch = Get-SetupArch
$headers = @{ 'User-Agent' = 'fyc-setup' }

# ── release metadata ─────────────────────────────────────────────────────────
$api = "https://api.github.com/repos/$repo/releases"
if ($Version) {
    $tag = if ($Version.StartsWith('v')) { $Version } else { "v$Version" }
    $release = Invoke-RestMethod -Uri "$api/tags/$tag" -Headers $headers
} else {
    $release = Invoke-RestMethod -Uri "$api/latest" -Headers $headers
    $tag = $release.tag_name
}
if (-not $tag) { throw 'the release metadata has no tag_name' }

# goreleaser writes the Go os/arch (windows/amd64); the "friendly" names
# (Windows/x86_64) are accepted too, so the matcher survives a name change.
$assetRe = if ($arch -eq 'x86_64') { '_(windows|Windows)_(amd64|x86_64)\.zip$' } else { '_(windows|Windows)_(arm64|aarch64)\.zip$' }
$asset = $release.assets | Where-Object { $_.name -match $assetRe } | Select-Object -First 1
if (-not $asset) {
    # Show what the release actually ships instead of failing blind.
    $names = @($release.assets | ForEach-Object { $_.name })
    if ($names.Count -gt 0) {
        Write-Host "assets in ${tag}:"
        $names | ForEach-Object { Write-Host "  - $_" }
    }
    throw "release $tag has no Windows/$arch archive"
}

if (-not $InstallDir) {
    $InstallDir = if ($SystemInstall) {
        Join-Path $env:ProgramFiles $appName
    } else {
        Join-Path $env:LOCALAPPDATA "Programs\$appName"
    }
}
$dest = Join-Path $InstallDir "$bin.exe"

Write-Host "$bin $tag (Windows/$arch)"
Write-Host "  installer: $($asset.name)"
Write-Host "  target:    $dest"

if ($SystemInstall -and -not (Test-IsAdmin)) {
    throw '-SystemInstall needs an elevated PowerShell (Run as administrator)'
}

if ($DryRun) {
    Write-Host "  url:       $($asset.browser_download_url)"
    Write-Host 'dry run: nothing downloaded or installed'
    return
}

# Nothing to do when the wanted version is already installed. A binary built
# without -X main.version reports "dev", so the check simply never matches.
if (-not $Force -and (Test-Path -LiteralPath $dest)) {
    $current = (& $dest --version 2>$null) -join ' '
    if ($current -match '^v([^ ]+)' -and $Matches[1] -eq $tag.TrimStart('v')) {
        Write-Host "already installed: $dest (use -Force to reinstall)"
        return
    }
}

# ── download + verify ────────────────────────────────────────────────────────
$work = Join-Path ([IO.Path]::GetTempPath()) ("fyc-setup-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $zip = Join-Path $work $asset.name
    Write-Host "downloading $($asset.name)..."
    Invoke-WebRequest -Uri $asset.browser_download_url -Headers $headers -OutFile $zip

    $checksums = $release.assets | Where-Object { $_.name -eq 'checksums.txt' } | Select-Object -First 1
    if ($checksums) {
        $csPath = Join-Path $work 'checksums.txt'
        Invoke-WebRequest -Uri $checksums.browser_download_url -Headers $headers -OutFile $csPath
        $line = Get-Content -LiteralPath $csPath |
            Where-Object { $_ -match ([regex]::Escape($asset.name) + '$') } |
            Select-Object -First 1
        if (-not $line) { throw "checksums.txt has no entry for $($asset.name)" }
        $expected = ($line -split '\s+')[0].ToUpperInvariant()
        $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToUpperInvariant()
        if ($expected -ne $actual) { throw "checksum mismatch for $($asset.name)" }
        Write-Host 'checksum verified'
    } else {
        Write-Warning 'the release has no checksums.txt: skipping checksum verification'
    }

    # ── extract + install ────────────────────────────────────────────────────
    $extract = Join-Path $work 'extract'
    Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force
    $src = Join-Path $extract "$bin.exe"
    if (-not (Test-Path -LiteralPath $src)) { throw "the archive does not contain $bin.exe" }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -LiteralPath $src -Destination $dest -Force

    if ($NoPathUpdate) {
        Write-Host 'PATH: not modified (-NoPathUpdate)'
    } else {
        $scope = if ($SystemInstall) { 'Machine' } else { 'User' }
        $currentPath = [Environment]::GetEnvironmentVariable('Path', $scope)
        if (-not $currentPath) { $currentPath = '' }
        $pathEntries = @($currentPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        $present = @($pathEntries | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }).Count -gt 0
        if (-not $present) {
            $newPath = (@($pathEntries) + $InstallDir) -join ';'
            [Environment]::SetEnvironmentVariable('Path', $newPath, $scope)
            Write-Host "added $InstallDir to the $scope PATH (open a new terminal to use it)"
        }
        $env:Path = "$($env:Path.TrimEnd(';'));$InstallDir"
    }

    $installed = (& $dest --version 2>$null) -join ' '
    if (-not $installed) { $installed = "$bin $tag" }
    Write-Host "installed: $installed"

    # ── post-install configuration (same steps as install.ps1) ───────────────
    $realHome = $env:USERPROFILE
    $configDir = if ($env:FYC_CONFIG_DIR) { $env:FYC_CONFIG_DIR } elseif ($env:APPDATA) { Join-Path $env:APPDATA $appName } else { Join-Path $realHome ('.config\' + $appName) }
    $skillsDir = ''
    $compFile = ''

    if ($NoSkills) {
        Write-Host 'skills: skipped (-NoSkills)'
    } elseif (Test-Path -LiteralPath (Join-Path $extract 'skills')) {
        $skillsDir = Join-Path $configDir 'skills'
        New-Item -ItemType Directory -Force -Path $skillsDir | Out-Null
        Copy-Item -Recurse -Force (Join-Path $extract 'skills\*') $skillsDir
        Write-Host "skills: $skillsDir"
    } else {
        Write-Warning 'the archive has no skills/ directory: skipping'
    }

    $modelsDir = $configDir
    if (Test-Path -LiteralPath (Join-Path $extract 'models\catalog.json')) {
        New-Item -ItemType Directory -Force -Path $modelsDir | Out-Null
        Copy-Item -Force (Join-Path $extract 'models\catalog.json') (Join-Path $modelsDir 'models.json')
        Write-Host "catalog: $(Join-Path $modelsDir 'models.json')"
    } else {
        Write-Warning 'the archive has no models/catalog.json: skipping'
    }

    if ($NoCompletion) {
        Write-Host 'completion: skipped (-NoCompletion)'
    } elseif (Test-Path -LiteralPath (Join-Path $extract 'completions\fuckyourcli.ps1')) {
        $compDir = Join-Path $InstallDir 'completions'
        $compFile = Join-Path $compDir 'fuckyourcli.ps1'
        New-Item -ItemType Directory -Force -Path $compDir | Out-Null
        Copy-Item -Force (Join-Path $extract 'completions\fuckyourcli.ps1') $compFile
        if ($NoProfileUpdate) {
            Write-Host "completion: $compFile (not activated: add to `$PROFILE -> . `"$compFile`")"
        } elseif ([string]::IsNullOrWhiteSpace($PROFILE)) {
            Write-Warning "completion: cannot determine `$PROFILE; activate manually: . `"$compFile`""
        } else {
            # PowerShell loads nothing automatically: without this line in
            # $PROFILE, tab completion stays silent.
            $profileDir = Split-Path -Parent $PROFILE
            if (-not (Test-Path -LiteralPath $profileDir)) {
                New-Item -ItemType Directory -Force -Path $profileDir | Out-Null
            }
            $active = $false
            if (Test-Path -LiteralPath $PROFILE) {
                $profileText = Get-Content -Raw -LiteralPath $PROFILE
                if ($profileText -and $profileText.Contains($compFile)) { $active = $true }
            }
            if ($active) {
                Write-Host "completion: $compFile (already active in `$PROFILE)"
            } else {
                Add-Content -LiteralPath $PROFILE -Encoding UTF8 -Value ("`r`n# fuckyourcli: PowerShell completion`r`n. `"$compFile`"`r`n")
                Write-Host "completion: $compFile (activated in `$PROFILE, restart the terminal)"
            }
            # A $PROFILE is itself a script: under Restricted/AllSigned PowerShell
            # will not execute it and completion would stay inert. Only the
            # *persisted* scopes are considered (the Process scope — e.g. the
            # Bypass used to launch this script — would mask the real value), and
            # the change is asked for, never silent.
            # NB: $_.Scope is an enum, so the comparison is done with -eq against
            # the string (which converts to the left operand's type).
            $policyList = @(Get-ExecutionPolicy -List)
            $locked = $false
            $effective = 'Undefined'
            foreach ($scopeName in @('MachinePolicy', 'UserPolicy', 'CurrentUser', 'LocalMachine')) {
                $entry = $policyList | Where-Object { $_.Scope -eq $scopeName } | Select-Object -First 1
                if ($null -eq $entry -or $entry.ExecutionPolicy -eq 'Undefined') { continue }
                if ($effective -eq 'Undefined') { $effective = $entry.ExecutionPolicy }
                if (($scopeName -eq 'MachinePolicy' -or $scopeName -eq 'UserPolicy') -and ($entry.ExecutionPolicy -eq 'Restricted' -or $entry.ExecutionPolicy -eq 'AllSigned')) {
                    $locked = $true
                }
            }
            # No persisted scope set: Windows' built-in default is Restricted.
            if ($effective -eq 'Undefined') { $effective = 'Restricted' }

            if ($effective -ne 'Restricted' -and $effective -ne 'AllSigned') {
                Write-Host ('ExecutionPolicy: ' + $effective + ' (local scripts are allowed, ok)')
            } elseif ($locked) {
                Write-Warning "ExecutionPolicy blocked by group policy ($effective): `$PROFILE will not load; an administrator must allow local scripts"
            } else {
                Write-Host "ExecutionPolicy is $effective: `$PROFILE would not be executed, so completion would stay inactive."
                $answer = ''
                try {
                    $answer = Read-Host 'Set RemoteSigned for the current user? The change is GLOBAL (all user scripts). [y/N]'
                } catch {
                    $answer = ''
                }
                if ($answer -match '^(y|yes|s)$') {
                    try {
                        Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
                        Write-Host 'ExecutionPolicy: set to RemoteSigned (current user)'
                    } catch {
                        Write-Warning ('cannot set ExecutionPolicy: ' + $_.Exception.Message)
                    }
                } else {
                    Write-Warning 'ExecutionPolicy left unchanged: completion will not load'
                }
            }
        }
    } else {
        Write-Warning 'the archive has no completions/: skipping'
    }

    # Dedicated sandbox account (one-time, raises UAC). Not fatal if refused.
    if ($SkipSandboxSetup) {
        Write-Host 'sandbox: not configured (-SkipSandboxSetup)'
    } else {
        Write-Host 'configuring the dedicated sandbox (one-time: it will ask for UAC)...'
        $prevPolicy = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
                $PSNativeCommandUseErrorActionPreference = $false
            }
            & $dest sandbox setup
            $sandboxRc = $LASTEXITCODE
        } catch {
            $sandboxRc = -1
        } finally {
            $ErrorActionPreference = $prevPolicy
        }
        if ($sandboxRc -eq 0) {
            Write-Host 'sandbox: ready'
        } else {
            Write-Warning "sandbox: setup not completed (exit $sandboxRc); run it later: $bin sandbox setup"
        }
    }
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}

# ── summary (same as install.ps1) ────────────────────────────────────────────
Write-Host ''
Write-Host '--------------------------------------------------'
Write-Host 'Installation completed successfully!'
Write-Host "  binary:     $dest"
if ($skillsDir) { Write-Host "  skills:     $skillsDir" } else { Write-Host '  skills:     not installed' }
Write-Host "  catalog:    $(Join-Path $configDir 'models.json')"
if ($compFile) {
    Write-Host "  completion: $compFile"
    Write-Host '              (loaded from $PROFILE: restart the terminal if tab is still silent)'
} else {
    Write-Host '  completion: not installed'
}
Write-Host "  config:     $configDir  (config.json)"
if ($SkipSandboxSetup) {
    Write-Host '  sandbox:    not configured; run:  fuckyourcli sandbox setup'
}
Write-Host ''
Write-Host "Note: on Windows the 'sh' tool executes commands via cmd.exe (cmd syntax),"
Write-Host 'not with a POSIX shell.'
Write-Host "Start '$bin' from any directory."
