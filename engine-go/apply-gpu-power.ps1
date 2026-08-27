# Applies GPU_POWER_LIMIT_W from settings.ini via nvidia-smi -pl.
# Why: RTX 3090 transient spikes drop the card off PCIe on this board
# (BSOD 0x124 WHEA). nvidia-smi -pl needs admin rights, so when a real
# change is required this script triggers ONE UAC prompt. If limits are
# already satisfied nothing is elevated and the stack starts silently.
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File apply-gpu-power.ps1 [-DryRun]
param(
    [string]$Spec = "",
    [switch]$DryRun
)

$ErrorActionPreference = "Continue"

if ($Spec -eq "") {
    $ini = Join-Path $PSScriptRoot "settings.ini"
    if (Test-Path -LiteralPath $ini) {
        foreach ($line in Get-Content -LiteralPath $ini) {
            if ($line -match '^\s*GPU_POWER_LIMIT_W\s*=\s*(.+?)\s*$') { $Spec = $Matches[1]; break }
        }
    }
}
if ([string]::IsNullOrWhiteSpace($Spec)) { exit 0 }

$gpus = @()
try { $gpus = @(nvidia-smi --query-gpu=index,name,power.limit --format=csv,noheader 2>$null) } catch {}
if ($gpus.Count -eq 0) { Write-Host "[GPU] nvidia-smi not available - power limits skipped"; exit 0 }

function Parse-Watts([string]$s) {
    $s = ($s -replace '[Ww\s]', '')
    $v = 0.0
    if ([double]::TryParse($s, [System.Globalization.NumberStyles]::Float,
            [System.Globalization.CultureInfo]::InvariantCulture, [ref]$v)) { return [int]$v }
    return -1
}

# Same semantics as engine-go/gpu_power.go: first matching rule wins per GPU.
$plan = @() # entries: @{ Idx; Name; Watts }
foreach ($ruleRaw in ($Spec -split '[,;]')) {
    $rule = $ruleRaw.Trim()
    $i = $rule.IndexOf(':')
    if ($i -le 0 -or $i -eq $rule.Length - 1) { continue }
    $namePart = $rule.Substring(0, $i).Trim().ToLowerInvariant()
    $watts = 0
    if (-not [int]::TryParse($rule.Substring($i + 1).Trim(), [ref]$watts)) { continue }
    if ($namePart -eq "" -or $watts -lt 50 -or $watts -gt 700) { continue }

    foreach ($g in $gpus) {
        $f = ($g -split ',') | ForEach-Object { $_.Trim() }
        if ($f.Count -lt 2 -or $f[0] -eq "") { continue }
        $idx = $f[0]; $gname = $f[1].ToLowerInvariant()
        if (-not $gname.Contains($namePart)) { continue }
        $cur = -1
        if ($f.Count -gt 2) { $cur = Parse-Watts $f[2] }
        if ($cur -ge 0 -and $cur -le $watts) {
            Write-Host "[GPU] $($f[1]): limit $watts W not needed - current $cur W is lower"
            break
        }
        $plan += [pscustomobject]@{ Idx = $idx; Name = $f[1]; Watts = $watts }
        break
    }
}

if ($plan.Count -eq 0) { Write-Host "[GPU] all power limits already applied"; exit 0 }

foreach ($p in $plan) { Write-Host ("[GPU] need: {0} -> -i {1} -pl {2}" -f $p.Name, $p.Idx, $p.Watts) }
if ($DryRun) { Write-Host "[GPU] dry-run: no elevation performed"; exit 0 }

$tmp = Join-Path $env:TEMP "kibborg-gpu-pl.ps1"
$lines = @('$ErrorActionPreference="Stop"')
foreach ($p in $plan) {
    $lines += ('& nvidia-smi -i {0} -pl {1} | Out-Null' -f $p.Idx, $p.Watts)
}
Set-Content -LiteralPath $tmp -Value $lines -Encoding ASCII

$elevated = $null
try {
    $elevated = Start-Process powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden `
        -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$tmp`""
} catch {
    Write-Host "[GPU] UAC declined or failed ($($_.Exception.Message)) - power limits NOT applied"
    exit 0
}

Remove-Item -LiteralPath $tmp -ErrorAction SilentlyContinue

if ($elevated.ExitCode -ne 0) {
    Write-Host "[GPU] elevated nvidia-smi exited with $($elevated.ExitCode)"
}

foreach ($g in (nvidia-smi --query-gpu=index,name,power.limit --format=csv,noheader)) {
    Write-Host "[GPU] now: $g"
}
exit 0
