# SolarAgent installer for Windows - https://github.com/wetakush/solaragent (open source, MIT)
# usage (PowerShell):  irm https://raw.githubusercontent.com/wetakush/solaragent/main/install.ps1 | iex
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'wetakush/solaragent'
$arch = $env:PROCESSOR_ARCHITEW6432
if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
switch ($arch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { Write-Host "Unsupported architecture: $arch" -ForegroundColor Red; return }
}
$asset = "solar-windows-$arch.exe"
$dir = if ($env:SOLAR_INSTALL_DIR) { $env:SOLAR_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\solar' }
$base = "https://github.com/$repo/releases/latest/download"

Write-Host "$([char]0x2600) SolarAgent" -ForegroundColor Yellow -NoNewline
Write-Host " installing $asset -> $dir"
New-Item -ItemType Directory -Force -Path $dir | Out-Null

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("solar-" + [Guid]::NewGuid().ToString('N') + '.exe')
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $tmp
    $sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt").Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    $line = ($sums -split "`n") | Where-Object { $_ -match ("\s\*?" + [regex]::Escape($asset) + "\s*$") } | Select-Object -First 1
    if (-not $line) { throw "no checksum for $asset" }
    $want = ($line.Trim() -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToLower()
    if ($want -ne $got) { throw "checksum mismatch for $asset" }

    $exe = Join-Path $dir 'solar.exe'
    if (Test-Path $exe) {
        # a running solar.exe can't be overwritten, but it can be renamed
        Remove-Item "$exe.old" -Force -ErrorAction SilentlyContinue
        Rename-Item $exe "solar.exe.old" -Force
    }
    Move-Item $tmp $exe -Force
} finally {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', ($(if ($userPath) { "$userPath;" } else { '' }) + $dir), 'User')
    $env:Path = "$env:Path;$dir"
    Write-Host "  added $dir to your user PATH (restart other terminals to pick it up)"
}

$ver = & (Join-Path $dir 'solar.exe') --version
Write-Host "$([char]0x2713)" -ForegroundColor Green -NoNewline
Write-Host " installed $ver"
Write-Host '  run ' -NoNewline; Write-Host 'solar' -ForegroundColor Yellow -NoNewline; Write-Host ' to get started (auto-updates are on by default)'
