$ErrorActionPreference = 'Stop'

# Native command failures are not terminating errors in Windows PowerShell 5.1.
function Invoke-Checked {
    param([string]$Command, [string[]]$Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed (exit $LASTEXITCODE). Setup stopped."
    }
}

function Ensure-Command([string]$Name, [string]$PackageId) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
            throw 'Install App Installer (winget), then run setup.ps1 again.'
        }
        Write-Host "Missing $Name. Installing $PackageId..."
        Invoke-Checked 'winget' @('install', '--id', $PackageId, '-e', '--source', 'winget', '--accept-source-agreements', '--accept-package-agreements')
        $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $env:Path = "$env:Path;$machinePath;$userPath"
        if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
            throw "$Name was installed but is not in PATH. Restart PowerShell and run setup.ps1 again."
        }
    }
}

Push-Location $PSScriptRoot
try {
    # Pick up tools installed since the current terminal was opened.
    $env:Path += ';' + [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
    Ensure-Command 'go' 'GoLang.Go'
    Ensure-Command 'node' 'OpenJS.NodeJS.LTS'
    Ensure-Command 'npm.cmd' 'OpenJS.NodeJS.LTS'

    Write-Host 'Downloading Go dependencies...'
    Invoke-Checked 'go' @('mod', 'download')
    Write-Host 'Running core tests...'
    Invoke-Checked 'go' @('test', '-count=1', './...')
    Invoke-Checked 'go' @('vet', './...')

    Push-Location (Join-Path $PSScriptRoot 'gui')
    try {
        $wailsVersion = & go list -m -f '{{.Version}}' github.com/wailsapp/wails/v2
        if ($LASTEXITCODE -ne 0) { throw 'Cannot determine Wails version from gui/go.mod.' }
        # Use the same CLI as the linked runtime, independently of a global Wails install.
        Invoke-Checked 'go' @('install', "github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion")
        $goBin = & go env GOBIN
        if ($LASTEXITCODE -ne 0) { throw 'Cannot determine GOBIN.' }
        if (-not $goBin) {
            $goPath = & go env GOPATH
            if ($LASTEXITCODE -ne 0) { throw 'Cannot determine GOPATH.' }
            $goBin = Join-Path ($goPath -split ';')[0] 'bin'
        }
        $wails = Join-Path $goBin 'wails.exe'
        Invoke-Checked 'go' @('mod', 'download')
        Push-Location 'frontend'
        try {
            Invoke-Checked 'npm.cmd' @('ci')
        } finally { Pop-Location }

        # Wails generates frontend bindings and dist before Go can compile embed assets.
        Write-Host 'Building GUI...'
        Invoke-Checked $wails @('build', '-platform', 'windows/amd64')
        Write-Host 'Running GUI tests...'
        Invoke-Checked 'go' @('test', '-count=1', './...')
        Invoke-Checked 'go' @('vet', './...')
    } finally { Pop-Location }
    Write-Host "Done: $PSScriptRoot\gui\build\bin\hrk-console-gui.exe"
} finally { Pop-Location }
