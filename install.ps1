# install.ps1 — install fuckyourcli on Windows.
#
# Build as current user (binary goes to %LOCALAPPDATA%) and install
# the binary, skills, and PowerShell completion (activated in $PROFILE). Finally, configure the
# dedicated sandbox (one-time: fyc sandbox setup auto-elevates with UAC).
#
#   .\install.ps1
#   .\install.ps1 -InstallDir C:\tools\fyc
#   .\install.ps1 -SkipBuild        # use an existing build\fuckyourcli.exe
#   .\install.ps1 -NoPathUpdate     # do not touch user PATH
#   .\install.ps1 -NoProfileUpdate  # do not touch $PROFILE
#   .\install.ps1 -SkipSandboxSetup # do not create dedicated sandbox user (no UAC)
#
# Directory override also via $env:FYC_INSTALL_DIR.
# If Windows blocks script execution:
#   powershell -ExecutionPolicy Bypass -File .\install.ps1

[CmdletBinding()]
param(
    [string]$InstallDir = $env:FYC_INSTALL_DIR,
    [switch]$SkipBuild,
    [switch]$NoPathUpdate,
    [switch]$NoProfileUpdate,
    [switch]$SkipSandboxSetup
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$BinaryName   = 'fuckyourcli'
$AppName      = 'fyc'
$SourceBinary = Join-Path 'build' ($BinaryName + '.exe')

function Step([string]$msg) { Write-Host ('==> ' + $msg) }
function Note([string]$msg) { Write-Host ('    ' + $msg) }

# --- 1. Windows only ---
if ($env:OS -ne 'Windows_NT') {
    throw 'install.ps1 is intended for Windows. On Linux use ./install.sh, on macOS ./install_mac.sh.'
}
$RealHome = $env:USERPROFILE

# --- 2. Installation directory ---
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $env:LOCALAPPDATA ('Programs\' + $AppName)
}
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)

# --- 3. Build as current user ---
if (-not $SkipBuild) {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'go not found in PATH: install Go 1.25+ (https://go.dev/dl/) or use -SkipBuild.'
    }
    $version = 'dev'
    if (Test-Path 'version') { $version = (Get-Content 'version' -Raw).Trim() }
    # Windows PowerShell can treat native stderr warnings as terminating errors.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $codexVersion = & go run -buildvcs=false ./cmd/update-codex-version
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($LASTEXITCODE -ne 0) { throw 'Unable to resolve a valid Codex fallback version.' }
    $codexVersion = ($codexVersion | Out-String).Trim()
    Step ('Building (version ' + $version + ', Codex ' + $codexVersion + ')...')
    $buildDir = Split-Path -Parent $SourceBinary
    if (-not (Test-Path $buildDir)) { New-Item -ItemType Directory -Force -Path $buildDir | Out-Null }
    & go build -buildvcs=false -ldflags "-X main.version=$version -X fuckyourcli/codexversion.BuildVersion=$codexVersion" -o $SourceBinary .
    if ($LASTEXITCODE -ne 0) { throw ('Build failed: go build returned ' + $LASTEXITCODE + '.') }
}
if (-not (Test-Path $SourceBinary)) {
    throw ($SourceBinary + ' not found: run .\install.ps1 first (without -SkipBuild).')
}

# --- 4. Remove old version + copy binary ---
Step ('Installing ' + $BinaryName + ' in ' + $InstallDir + '...')
if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null }
$dest = Join-Path $InstallDir ($BinaryName + '.exe')
if (Test-Path $dest) { Remove-Item -Force $dest }
Copy-Item -Force $SourceBinary $dest
Note ('binary -> ' + $dest)

# --- 5. Skills in the native config directory (same as store.ConfigDir) ---
$ConfigDir = if ($env:FYC_CONFIG_DIR) { $env:FYC_CONFIG_DIR } elseif ($env:APPDATA) { Join-Path $env:APPDATA $AppName } else { Join-Path $RealHome ('.config\' + $AppName) }
$SkillsDir = Join-Path $ConfigDir 'skills'
if (Test-Path 'skills') {
    Step ('Copying skills to ' + $SkillsDir + '...')
    if (-not (Test-Path $SkillsDir)) { New-Item -ItemType Directory -Force -Path $SkillsDir | Out-Null }
    Copy-Item -Recurse -Force (Join-Path 'skills' '*') $SkillsDir
} else {
    Step 'No skills/ directory to install.'
}

# --- 5b. models.dev catalog (config dir) ---
$ModelsDir = $ConfigDir
if (Test-Path 'models\catalog.json') {
    Step ('Copying models catalog to ' + (Join-Path $ModelsDir 'models.json') + '...')
    if (-not (Test-Path $ModelsDir)) { New-Item -ItemType Directory -Force -Path $ModelsDir | Out-Null }
    Copy-Item -Force 'models\catalog.json' (Join-Path $ModelsDir 'models.json')
} else {
    Step 'No models\catalog.json to install.'
}


# --- 7. User PATH ---
if ($NoPathUpdate) {
    Step 'User PATH not modified (-NoPathUpdate).'
} else {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ([string]::IsNullOrEmpty($userPath)) { $userPath = '' }
    $entries = @($userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $present = @($entries | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }).Count -gt 0
    if ($present) {
        Step ($InstallDir + ' is already in user PATH.')
    } else {
        $newPath = (@($entries) + $InstallDir) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path = $env:Path + ';' + $InstallDir
        Step ('Added ' + $InstallDir + ' to user PATH.')
        Note 'Restart terminal for PATH to take effect.'
    }
}

# --- 8. PowerShell Completion ---
$compDir  = Join-Path $InstallDir 'completions'
$compFile = Join-Path $compDir ($BinaryName + '.ps1')
if (-not (Test-Path $compDir)) { New-Item -ItemType Directory -Force -Path $compDir | Out-Null }
Step ('Installing PowerShell completion in ' + $compFile + '...')
# The script comes from completions\ (single source of truth, also shipped
# inside the release archives for setup.ps1).
$completerSrc = Join-Path 'completions' 'fuckyourcli.ps1'
if (-not (Test-Path -LiteralPath $completerSrc)) {
    throw 'completions\fuckyourcli.ps1 not found: run the installer from the repository root.'
}
Copy-Item -Force -LiteralPath $completerSrc -Destination $compFile

# --- 8b. Activating completion in $PROFILE ---
# Unlike bash/zsh (install.sh/install_mac.sh copy completion to
# an auto-loaded directory) PowerShell doesn't read anything automatically: the
# dot-source line must be written to $PROFILE, otherwise tabs remain silent.
$profileLineAdded = $false
if ($NoProfileUpdate) {
    Step ('Completion not activated (-NoProfileUpdate); add manually:  . "' + $compFile + '"')
} elseif ([string]::IsNullOrWhiteSpace($PROFILE)) {
    Note ('Unable to determine $PROFILE; activate manually:  . "' + $compFile + '"')
} else {
    $profileDir = Split-Path -Parent $PROFILE
    if (-not (Test-Path $profileDir)) { New-Item -ItemType Directory -Force -Path $profileDir | Out-Null }
    $profileLine = '. "' + $compFile + '"'
    $already = $false
    if (Test-Path $PROFILE) {
        $profileText = Get-Content -Raw -Path $PROFILE
        if ($null -ne $profileText -and $profileText.Contains($compFile)) { $already = $true }
    }
    if ($already) {
        Step ('Completion already active in $PROFILE (' + $PROFILE + ').')
        $profileLineAdded = $true
    } else {
        Add-Content -Path $PROFILE -Value ("`r`n# fuckyourcli: PowerShell completion`r`n" + $profileLine + "`r`n") -Encoding UTF8
        Step ('Completion activated in $PROFILE (' + $PROFILE + ').')
        Note 'Restart terminal for $PROFILE to be reloaded.'
        $profileLineAdded = $true
    }
}

# --- 8c. ExecutionPolicy ---
# A $PROFILE is itself a script: under Restricted/AllSigned PowerShell won't
# execute it and completion would remain inert. If (and only if) the
# PERSISTENT policy blocks it, we ask the user: the change is GLOBAL for
# the user (all scripts, both Windows PowerShell 5.1 and 7), never silent.
# NB: Get-ExecutionPolicy (without -List) would also include the Process scope
# (e.g. the Bypass used to launch THIS script) and would mask the
# persistent value: here we only consider persisted scopes.
$policyOk = $true
$effective = 'Undefined'
if ($NoProfileUpdate) {
    Note 'ExecutionPolicy not verified (-NoProfileUpdate).'
} else {
    $policyList = @(Get-ExecutionPolicy -List)
    $locked = $false
    foreach ($scopeName in @('MachinePolicy', 'UserPolicy', 'CurrentUser', 'LocalMachine')) {
        # Note: $_.Scope is an enum (Microsoft.PowerShell.ExecutionPolicyScope),
        # so the comparison must be done with -eq (which converts the string to the type
        # enum of the left member); using it as a hashtable key and then
        # searching for it by string would find nothing.
        $entry = $policyList | Where-Object { $_.Scope -eq $scopeName } | Select-Object -First 1
        if ($null -eq $entry -or $entry.ExecutionPolicy -eq 'Undefined') { continue }
        if ($effective -eq 'Undefined') { $effective = $entry.ExecutionPolicy }
        if (($scopeName -eq 'MachinePolicy' -or $scopeName -eq 'UserPolicy') -and ($entry.ExecutionPolicy -eq 'Restricted' -or $entry.ExecutionPolicy -eq 'AllSigned')) {
            $locked = $true
        }
    }
    if ($effective -eq 'Undefined') { $effective = 'Restricted' }

    if ($effective -ne 'Restricted' -and $effective -ne 'AllSigned') {
        Step ('ExecutionPolicy: ' + $effective + ' (local scripts are allowed, ok).')
    } elseif ($locked) {
        $policyOk = $false
        Note ('ExecutionPolicy blocked by group policy (' + $effective + '): $PROFILE will not be executed.')
        Note 'Administrator intervention is required to enable local scripts.'
    } else {
        Note ('ExecutionPolicy = ' + $effective + ': PowerShell would not execute $PROFILE,')
        Note 'so completion would not load and tabs would remain inactive.'
        $answer = ''
        try {
            $answer = Read-Host 'Set RemoteSigned for the current user? The change is GLOBAL (all user scripts). [y/N]'
        } catch {
            $answer = ''
        }
        if ($answer -match '^(y|yes|s)$') {
            try {
                Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
                Step ('ExecutionPolicy (was ' + $effective + '): set to RemoteSigned (user scope).')
            } catch {
                $policyOk = $false
                Note ('Unable to modify ExecutionPolicy: ' + $_.Exception.Message)
                Note 'Unlock manually with:  Set-ExecutionPolicy -Scope CurrentUser RemoteSigned'
            }
        } else {
            $policyOk = $false
            Note 'ExecutionPolicy left unchanged: completion will not load.'
            Note 'Activate it whenever you want with:  Set-ExecutionPolicy -Scope CurrentUser RemoteSigned'
        }
    }
}

# --- 9. Verifica ---
try {
    $ver = & $dest --version 2>&1
    Step ('Verification: ' + (($ver | Out-String).Trim()))
} catch {
    Note 'Unable to execute the newly installed binary: verify manually.'
}

# --- 10. Dedicated sandbox (Windows, one-time) ---
# Create local user FycSandbox + group FycSandboxUsers and save credentials with
# DPAPI. No ACEs are planted (identity + credentials only): fast.
# Idempotent (version marker). fyc sandbox setup auto-elevates with UAC
# keeping the SAME user: DPAPI and marker end up in the right profile.
# Non-blocking: if UAC is rejected, installation continues anyway.
if ($SkipSandboxSetup) {
    Step 'Dedicated sandbox not configured (-SkipSandboxSetup).'
} else {
    Step 'Configuring dedicated sandbox (one-time: will ask for UAC)...'
    Note 'only identity + credentials: no ACEs planted (fast).'
    $prevEA = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
            $PSNativeCommandUseErrorActionPreference = $false
        }
        & $dest sandbox setup
        $sbxRc = $LASTEXITCODE
    } catch {
        Note ('sandbox: setup not performed: ' + $_.Exception.Message)
        $sbxRc = -1
    } finally {
        $ErrorActionPreference = $prevEA
    }
    if ($sbxRc -eq 0) {
        Note 'dedicated sandbox ready (details:  fuckyourcli sandbox status).'
    } else {
        Note ('sandbox: setup not completed (exit ' + $sbxRc + '); you can run it later with:  ' + $BinaryName + ' sandbox setup')
    }
}

# --- 11. Summary ---
Write-Host ''
Write-Host '--------------------------------------------------'
Write-Host 'Installation completed successfully!'
Note ('binary:    ' + $dest)
Note ('skills:     ' + $SkillsDir)
Note ('catalog:    ' + (Join-Path $ModelsDir 'models.json'))
Note ('config:     ' + $ConfigDir + '  (config.json)')
if ($NoProfileUpdate) {
    Note ('completion: NOT activated (-NoProfileUpdate); add to $PROFILE:  . "' + $compFile + '"')
} elseif ($profileLineAdded -and $policyOk) {
    Note ('completion: active in $PROFILE (' + $PROFILE + ')')
} elseif ($profileLineAdded) {
    Note ('completion: $PROFILE updated, but ExecutionPolicy (' + $effective + ') blocks it')
} else {
    Note ('completion: add to $PROFILE:  . "' + $compFile + '"')
}
if ($SkipSandboxSetup) {
    Note 'sandbox:    not configured; run:  fuckyourcli sandbox setup'
} else {
    Note 'sandbox:    if setup failed:  fuckyourcli sandbox setup'
}
Write-Host ''
Note "Note: on Windows the 'sh' tool executes commands via cmd.exe (cmd syntax),"
Note 'not with a POSIX shell.'
Note ("Start '" + $BinaryName + "' from any directory.")
