[CmdletBinding()]
param(
    [string]$Executable,
    [ValidateRange(1, 3600)][int]$TimeoutSeconds = 180
)
$ErrorActionPreference = 'Stop'
if (-not $Executable) { $Executable = Join-Path $PSScriptRoot '..\Build\oktapod.exe' }
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$doctorText = & $Executable config doctor --json
if ($LASTEXITCODE -ne 0) { throw 'Configuration check failed.' }
$config = ($doctorText -join "`n") | ConvertFrom-Json
$address = $config.listen_address -replace '^0\.0\.0\.0:', '127.0.0.1:' -replace '^\[::\]:', '[::1]:'
$url = "http://$address"
$process = Get-Process -Name ([IO.Path]::GetFileNameWithoutExtension($Executable)) -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $Executable } | Select-Object -First 1
if (-not $process) {
    $process = Start-Process -FilePath $Executable -ArgumentList 'serve' -WorkingDirectory (Split-Path $Executable) -WindowStyle Hidden -PassThru
}
$startupLog = Join-Path $config.data_root 'logs\oktapod-startup.log'
Write-Host "Waiting for Oktapod (PID $($process.Id)); signed runtime verification may take a minute."
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
while ([DateTime]::UtcNow -lt $deadline) {
    $process.Refresh()
    if ($process.HasExited) { throw "Oktapod exited. See $startupLog" }
    $response = $null
    $reader = $null
    try {
        $request = [Net.HttpWebRequest]::Create("$url/api/v1/health")
        $request.Proxy = $null
        $request.Timeout = 2000
        $request.ReadWriteTimeout = 2000
        $response = $request.GetResponse()
        $reader = New-Object IO.StreamReader($response.GetResponseStream())
        $health = $reader.ReadToEnd() | ConvertFrom-Json
        if ($response.ContentType -match 'application/json' -and $health.status -eq 'ok') {
            $port = ([Uri]$url).Port
            $listener = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue |
                Where-Object { $_.OwningProcess -eq $process.Id }
            if (-not $listener) { throw 'Health response belongs to another process.' }
            Write-Host "Oktapod ready: $url/ (PID $($process.Id))"
            return
        }
    } catch { } finally {
        if ($reader) { $reader.Dispose() }
        if ($response) { $response.Dispose() }
    }
    Start-Sleep -Seconds 1
}
throw "Readiness timed out; process was left running. Inspect $startupLog before retrying."
