[CmdletBinding()]
param(
    [string]$SocksHost = '127.0.0.1',
    [ValidateRange(1, 65535)]
    [int]$SocksPort = 1080,
    [ValidateSet('vk-video', 'vk-dc', 'telemost-video', 'wb-video', 'wb-dc', 'dion-video')]
    [string]$Mode = 'vk-video',
    [string]$ClientCommit = 'unknown',
    [string]$ServerCommit = 'unknown',
    [ValidateRange(1, 60)]
    [int]$FPS = 24,
    [ValidateRange(1, 200)]
    [int]$Batch = 30,
    [switch]$DualTrack,
    [ValidateRange(1, 100)]
    [int]$Iterations = 20,
    [ValidateRange(1024, 1073741824)]
    [long]$DownloadBytes = 10485760,
    [string]$OutputDirectory = 'benchmark-results'
)

$ErrorActionPreference = 'Stop'
if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
    throw 'curl.exe is required'
}

$proxy = '{0}:{1}' -f $SocksHost, $SocksPort
$shortURL = 'https://www.cloudflare.com/cdn-cgi/trace'
$downloadURL = "https://speed.cloudflare.com/__down?bytes=$DownloadBytes"
$writeOut = '%{http_code}|%{time_namelookup}|%{time_connect}|%{time_starttransfer}|%{time_total}|%{speed_download}|%{size_download}'

function Invoke-BenchmarkRequest {
    param([string]$URL)

    $started = [DateTimeOffset]::UtcNow
    $curlArgs = @(
        '--silent',
        '--show-error',
        '--location',
        '--max-time', '60',
        '--connect-timeout', '15',
        '--socks5-hostname', $proxy,
        '--output', 'NUL',
        '--write-out', $writeOut,
        $URL
    )
    $output = & curl.exe @curlArgs 2>&1
    $exitCode = $LASTEXITCODE
    $line = ($output | Select-Object -Last 1)
    $parts = "$line".Split('|')

    $result = [ordered]@{
        startedAt = $started.ToString('o')
        exitCode = $exitCode
        httpCode = 0
        dnsSeconds = 0.0
        connectSeconds = 0.0
        ttfbSeconds = 0.0
        totalSeconds = 0.0
        speedBytesPerSecond = 0.0
        downloadedBytes = 0
        error = $null
    }
    if ($exitCode -eq 0 -and $parts.Count -eq 7) {
        $culture = [System.Globalization.CultureInfo]::InvariantCulture
        $result.httpCode = [int]$parts[0]
        $result.dnsSeconds = [double]::Parse($parts[1], $culture)
        $result.connectSeconds = [double]::Parse($parts[2], $culture)
        $result.ttfbSeconds = [double]::Parse($parts[3], $culture)
        $result.totalSeconds = [double]::Parse($parts[4], $culture)
        $result.speedBytesPerSecond = [double]::Parse($parts[5], $culture)
        $result.downloadedBytes = [long][double]::Parse($parts[6], $culture)
    } else {
        $errorLines = @($output)
        if ($errorLines.Count -gt 1) {
            $errorLines = $errorLines[0..($errorLines.Count - 2)]
        }
        $result.error = ($errorLines -join ' ').Trim()
    }
    [pscustomobject]$result
}

$startedAt = [DateTimeOffset]::UtcNow
$shortRequests = @()
for ($i = 1; $i -le $Iterations; $i++) {
    Write-Host "Short HTTPS request $i/$Iterations"
    $shortRequests += Invoke-BenchmarkRequest -URL $shortURL
}

Write-Host "Download test: $DownloadBytes bytes"
$download = Invoke-BenchmarkRequest -URL $downloadURL
$successCount = @($shortRequests | Where-Object { $_.exitCode -eq 0 -and $_.httpCode -ge 200 -and $_.httpCode -lt 400 }).Count

$report = [ordered]@{
    schemaVersion = 1
    startedAt = $startedAt.ToString('o')
    finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    mode = $Mode
    clientCommit = $ClientCommit
    serverCommit = $ServerCommit
    fps = $FPS
    batch = $Batch
    dualTrack = [bool]$DualTrack
    tunEnabled = $false
    socksEndpoint = $proxy
    shortRequestSuccesses = $successCount
    shortRequestCount = $Iterations
    shortRequests = $shortRequests
    downloadBytesRequested = $DownloadBytes
    download = $download
}

New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$name = 'socks-{0}-{1}.json' -f $Mode, $startedAt.ToString('yyyyMMdd-HHmmss')
$path = Join-Path $OutputDirectory $name
$report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $path -Encoding utf8

Write-Host ''
Write-Host "Short HTTPS: $successCount/$Iterations"
Write-Host ('Download: exit={0} http={1} bytes={2} speed={3:N0} B/s time={4:N2}s' -f $download.exitCode, $download.httpCode, $download.downloadedBytes, $download.speedBytesPerSecond, $download.totalSeconds)
Write-Host "Report: $((Resolve-Path -LiteralPath $path).Path)"
if ($successCount -ne $Iterations -or $download.exitCode -ne 0) {
    exit 1
}
