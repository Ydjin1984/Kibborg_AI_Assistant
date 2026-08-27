# ============================================================
# bench-gpu.ps1 — GPU-бенчмарк КИБОРГА по ТЗ §7/§12/§13
#
# Поднимает llama-server с аргументами профиля (те же флаги, что
# строит brainServerArgs в llm.go, включая mmproj), мерит на
# реальном HTTP-пути: load, TTFT, prefill tok/s, decode tok/s,
# пик VRAM обеих карт и утилизацию GPU. Пишет строку на каждую
# точку в gpu-profiles\bench-results.csv.
#
# Порт бенча — 8093, продакшн-мозг на 8083 не трогается. Но VRAM
# общая: запускай при остановленном стеке (Stop.cmd).
#
# Примеры:
#   .\gpu-profiles\bench-gpu.ps1 -Profile .\gpu-profiles\KIBORG-FAST.ini
#   .\gpu-profiles\bench-gpu.ps1 -Profile .\gpu-profiles\KIBORG-LONG.ini -CtxSizes 196608,262144
#   .\gpu-profiles\bench-gpu.ps1 -Profile .\gpu-profiles\BENCH-C.ini -CtxSizes 131072
#
# Замечание о нумерации (после свапа слотов, 26.08.2026):
#   llama.cpp: CUDA0 = RTX 3090, CUDA1 = RTX 3060 (--list-devices)
#   nvidia-smi: index 0 = RTX 3090, index 1 = RTX 3060
# VRAM читаем по имени карты, не по индексу — перестановки слотов не ломают CSV.
# ============================================================
param(
    [Parameter(Mandatory = $true)][string]$Profile,
    [string]$CtxSizes = "32768,65536,131072,196608,262144",
    [string]$Model = "",
    [int]$Port = 8093,
    [double]$PromptFrac = 0.5,
    [int]$NPredict = 256,
    [int]$LoadTimeoutSec = 420,
    [string]$OutCsv = ""
)
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Net.Http

$engineDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path) # engine-go/
$iniPath = Join-Path $engineDir "settings.ini"
if (-not (Test-Path $Profile)) { throw "профиль не найден: $Profile" }
if ($OutCsv -eq "") { $OutCsv = Join-Path (Split-Path -Parent $Profile) "bench-results.csv" }
$logDir = Join-Path (Split-Path -Parent $Profile) "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

function Read-IniKeys([string]$path) {
    $kv = @{}
    if (-not (Test-Path $path)) { return $kv }
    foreach ($line in Get-Content $path) {
        $t = $line.Trim()
        if ($t -eq "" -or $t.StartsWith("#") -or $t.StartsWith(";")) { continue }
        $i = $t.IndexOf("="); if ($i -lt 0) { continue }
        $kv[$t.Substring(0, $i).Trim()] = $t.Substring($i + 1).Trim()
    }
    return $kv
}

$base = Read-IniKeys $iniPath
$prof = Read-IniKeys $Profile
foreach ($k in $prof.Keys) { $base[$k] = $prof[$k] }

$llamaExe = $base["LLAMA_SERVER"]
if ($llamaExe -eq "" -or -not (Test-Path $llamaExe)) { throw "LLAMA_SERVER не найден: $llamaExe" }
$modelPath = if ($Model -ne "") { $Model } else { $base["MODEL_PATH"] }
if ($modelPath -eq "" -or -not (Test-Path $modelPath)) { throw "модель не найдена: $modelPath" }
$modelName = [IO.Path]::GetFileName($modelPath)
$profileName = [IO.Path]::GetFileNameWithoutExtension($Profile)

# mmproj как в проде: из settings.ini или авто-поиск рядом с моделью.
$mmproj = $base["MMPROJ_PATH"]
if ($mmproj -eq "" -or $mmproj -eq "auto") {
    $cand = Get-ChildItem (Split-Path -Parent $modelPath) -Filter "mmproj*.gguf" -ErrorAction SilentlyContinue | Select-Object -First 1
    $mmproj = if ($cand) { $cand.FullName } else { "" }
}
if ($mmproj -eq "off" -or $mmproj -eq "none") { $mmproj = "" }

# Потоки: LLAMA_THREADS, 0/пусто = физические ядра (как llamaThreadCount).
$threads = 0
[void][int32]::TryParse($base["LLAMA_THREADS"], [ref]$threads)
if ($threads -le 0) {
    $threads = (Get-CimInstance Win32_Processor | Measure-Object -Property NumberOfCores -Sum).Sum
    if (-not $threads -or $threads -lt 2) { $threads = 2 }
}

function Build-Args([int]$ctx) {
    $a = @("-m", $modelPath,
        "--ctx-size", "$ctx",
        "--n-gpu-layers", $(if ($base["LLAMA_GPU_LAYERS"]) { $base["LLAMA_GPU_LAYERS"] } else { "99" }),
        "--threads", "$threads", "--threads-batch", "$threads",
        "--port", "$Port",
        "--flash-attn", "on",
        "--parallel", "1",
        "--reasoning", "off", "--reasoning-budget", "0")
    if ($base["LLAMA_DEVICE"])          { $a += @("--device", $base["LLAMA_DEVICE"]) }
    if ($base["LLAMA_SPLIT_MODE"])      { $a += @("--split-mode", $base["LLAMA_SPLIT_MODE"]) }
    if ($base["LLAMA_TENSOR_SPLIT"])    { $a += @("--tensor-split", $base["LLAMA_TENSOR_SPLIT"]) }
    if ($base["LLAMA_MAIN_GPU"] -ne "" -and $null -ne $base["LLAMA_MAIN_GPU"]) { $a += @("--main-gpu", $base["LLAMA_MAIN_GPU"]) }
    if ($base["LLAMA_CACHE_TYPE_K"])    { $a += @("--cache-type-k", $base["LLAMA_CACHE_TYPE_K"]) }
    if ($base["LLAMA_CACHE_TYPE_V"])    { $a += @("--cache-type-v", $base["LLAMA_CACHE_TYPE_V"]) }
    if ($base["LLAMA_NO_KV_OFFLOAD"] -in @("true", "1", "yes", "on")) { $a += "--no-kv-offload" }
    if ($base["LLAMA_CTX_CHECKPOINTS"] -ne "") { $a += @("--ctx-checkpoints", $base["LLAMA_CTX_CHECKPOINTS"]) }
    if ($base["LLAMA_CACHE_RAM"] -ne "")       { $a += @("--cache-ram", $base["LLAMA_CACHE_RAM"]) }
    if ($base["LLAMA_BATCH"])           { $a += @("--batch-size", $base["LLAMA_BATCH"]) }
    if ($base["LLAMA_UBATCH"])          { $a += @("--ubatch-size", $base["LLAMA_UBATCH"]) }
    if ($mmproj -ne "")                 { $a += @("--mmproj", $mmproj) }
    return $a
}

$http = [System.Net.Http.HttpClient]::new()
$http.Timeout = [TimeSpan]::FromMinutes(30)

function Invoke-Json([string]$uri, $payload) {
    $json = $payload | ConvertTo-Json -Depth 6 -Compress
    $ct = [System.Net.Http.Headers.MediaTypeWithQualityHeaderValue]::new("application/json")
    $content = [System.Net.Http.StringContent]::new($json, [Text.Encoding]::UTF8, "application/json")
    $content.Headers.ContentType = $ct
    $resp = $http.PostAsync($uri, $content).GetAwaiter().GetResult()
    $body = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    $resp.Dispose()
    return $body
}

function Wait-BrainReady {
    $deadline = (Get-Date).AddSeconds($LoadTimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $h = $http.GetAsync("http://127.0.0.1:$Port/health").GetAwaiter().GetResult()
            if ($h.StatusCode -eq [System.Net.HttpStatusCode]::OK) { $h.Dispose(); return $true }
            $h.Dispose()
        } catch {}
        Start-Sleep -Milliseconds 800
    }
    return $false
}

function Stop-BenchBrain([int]$procId) {
    if ($procId -gt 0) { taskkill /PID $procId /T /F 2>$null | Out-Null }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)) { break }
        Start-Sleep -Milliseconds 400
    }
}

# Промпт-наполнитель: технический абзац, доля PromptFrac от контекста,
# точный размер добивается через /tokenize.
$filler = ("Local inference benchmark for an agentic assistant pipeline. The runner measures prompt processing speed, " +
    "time to first token and steady-state decode throughput across context sizes on consumer GPUs. KV cache placement, " +
    "tensor split ratio and batch geometry dominate the result; PCIe traffic between cards hurts generation speed. " +
    "Flash attention reduces memory traffic at long sequence lengths. ")
$tokCache = @{}
function Tokenize([string]$text) {
    if (-not $tokCache.ContainsKey($text)) {
        $r = (Invoke-Json "http://127.0.0.1:$Port/tokenize" @{ content = $text }) | ConvertFrom-Json
        $tokCache[$text] = @($r.tokens).Count
    }
    return $tokCache[$text]
}
function Build-Prompt([int]$targetTok) {
    $oneTok = Tokenize $filler
    $ratio = $filler.Length / [math]::Max($oneTok, 1)
    $need = [int]($targetTok * $ratio / $filler.Length * $filler.Length) # chars needed
    $need = [Math]::Max($need, $filler.Length)
    $sb = [Text.StringBuilder]::new($need + $filler.Length)
    while ($sb.Length -lt $need) { [void]$sb.Append($filler) }
    $text = $sb.ToString().Substring(0, [Math]::Min($need, $sb.Length))
    for ($i = 0; $i -lt 3; $i++) {
        $n = Tokenize $text
        if ([Math]::Abs($n - $targetTok) -le [Math]::Max(16, $targetTok / 50)) { break }
        $adj = [int]($text.Length * $targetTok / [math]::Max($n, 1))
        $sb2 = [Text.StringBuilder]::new([Math]::Max($adj + $filler.Length, 1024))
        while ($sb2.Length -lt $adj) { [void]$sb2.Append($filler) }
        $text = $sb2.ToString().Substring(0, $adj)
    }
    return $text
}

# Сэмплер VRAM/util: nvidia-smi пишет CSV с именем карты (порядок слотов не важен).
function Start-VramLog([string]$file) {
    $args = @("--query-gpu=name,memory.used,utilization.gpu", "--format=csv,noheader,nounits", "-lms", "500", "-f", $file)
    return (Start-Process -FilePath "nvidia-smi" -ArgumentList $args -PassThru -WindowStyle Hidden)
}
function Read-VramMax([string]$file) {
    $max3060 = 0L; $max3090 = 0L; $maxUtil = 0
    foreach ($line in Get-Content $file -ErrorAction SilentlyContinue) {
        $f = $line.Split(",")
        if ($f.Count -lt 3) { continue }
        $name = $f[0].Trim(); $mem = [long]$f[1].Trim(); $util = [int]$f[2].Trim()
        if ($name -match "3060") { if ($mem -gt $max3060) { $max3060 = $mem } }
        elseif ($name -match "3090") { if ($mem -gt $max3090) { $max3090 = $mem } }
        if ($util -gt $maxUtil) { $maxUtil = $util }
    }
    return @{ gpu3060 = $max3060; gpu3090 = $max3090; util = $maxUtil }
}

function Measure-Run([string]$prompt, [int]$npredict) {
    $payload = @{ prompt = $prompt; n_predict = $npredict; stream = $true; temperature = 0.1;
        cache_prompt = $true; reasoning_budget = 0 } | ConvertTo-Json -Depth 4 -Compress
    $req = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Post, "http://127.0.0.1:$Port/completion")
    $req.Content = [System.Net.Http.StringContent]::new($payload, [Text.Encoding]::UTF8, "application/json")
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $resp = $http.SendAsync($req, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
    if (-not $resp.IsSuccessStatusCode) { $resp.Dispose(); throw "HTTP $($resp.StatusCode)" }
    $stream = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
    $reader = [IO.StreamReader]::new($stream)
    $ttft = 0.0; $timings = $null; $genChars = 0
    while ($null -ne ($line = $reader.ReadLine())) {
        if (-not $line.StartsWith("data: ")) { continue }
        $body = $line.Substring(6)
        if ($body -eq "[DONE]") { break }
        try { $chunk = $body | ConvertFrom-Json } catch { continue }
        if ($chunk.content -and $chunk.content.Length -gt 0 -and $ttft -eq 0.0) { $ttft = $sw.Elapsed.TotalSeconds }
        if ($chunk.timings) { $timings = $chunk.timings }
        if ($chunk.stopped) { break }
    }
    $sw.Stop()
    $reader.Dispose(); $stream.Dispose(); $resp.Dispose(); $req.Dispose()
    return @{ ttft = $ttft; timings = $timings }
}

$rows = @()
$ctxList = $CtxSizes.Split(",") | ForEach-Object { [int]$_.Trim() }
Write-Host "== KIBORG GPU bench ==" -ForegroundColor Cyan
Write-Host ("профиль: {0}  модель: {1}" -f $profileName, $modelName)
Write-Host ("устройства: DEVICE={0} SPLIT={1}/{2} KV={3}" -f $base["LLAMA_DEVICE"], $base["LLAMA_SPLIT_MODE"], $base["LLAMA_TENSOR_SPLIT"], $base["LLAMA_CACHE_TYPE_K"])
Write-Host ("потоки: {0}   контексты: {1}" -f $threads, ($ctxList -join " "))

foreach ($ctx in $ctxList) {
    $row = [ordered]@{ ts = (Get-Date -Format "yyyy-MM-dd HH:mm:ss"); profile = $profileName; model = $modelName;
        ctx = $ctx; prompt_tok = 0; gen_tok = 0; load_s = 0; ttft_s = 0; prefill_tok_s = 0; gen_tok_s = 0;
        vram3090_max_mib = 0; vram3060_max_mib = 0; gpu_util_max_pct = 0; status = "OK" }
    $proc = $null; $sampler = $null; $vramFile = Join-Path $logDir "vram-$ctx.log"
    Write-Host ("--- ctx {0}: старт сервера..." -f $ctx) -NoNewline
    try {
        $proc = Start-Process -FilePath $llamaExe -ArgumentList (Build-Args $ctx) -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $logDir "server-$ctx.log") -RedirectStandardError (Join-Path $logDir "server-$ctx.err.log")
        $t0 = Get-Date
        if (-not (Wait-BrainReady)) { throw "сервер не поднялся за ${LoadTimeoutSec}с (лог: server-$ctx.err.log)" }
        $row.load_s = [math]::Round(((Get-Date) - $t0).TotalSeconds, 1)

        $targetTok = [int]($ctx * $PromptFrac)
        $prompt = Build-Prompt $targetTok
        $row.prompt_tok = Tokenize $prompt
        [void](Invoke-Json "http://127.0.0.1:$Port/completion" @{ prompt = "Hello"; n_predict = 8; temperature = 0.1 }) # warmup

        $sampler = Start-VramLog $vramFile
        $res = Measure-Run $prompt $NPredict
        $row.ttft_s = [math]::Round($res.ttft, 2)
        if ($res.timings) {
            $row.gen_tok = [int]$res.timings.predicted_n
            $row.prefill_tok_s = [math]::Round($res.timings.prompt_per_second, 1)
            $row.gen_tok_s = [math]::Round($res.timings.predicted_per_second, 1)
        }
    } catch {
        $row.status = "FAIL: $($_.Exception.Message)"
        Write-Host " FAIL" -ForegroundColor Red
    } finally {
        if ($sampler) { taskkill /PID $sampler.Id /F 2>$null | Out-Null }
        Start-Sleep -Milliseconds 600
        if (Test-Path $vramFile) {
            $v = Read-VramMax $vramFile
            $row.vram3090_max_mib = $v.gpu3090; $row.vram3060_max_mib = $v.gpu3060; $row.gpu_util_max_pct = $v.util
        }
        if ($proc -and -not $proc.HasExited) { Stop-BenchBrain $proc.Id }
    }
    $rows += [pscustomobject]$row
    if ($row.status -eq "OK") { Write-Host (" OK: prefill {0} t/s, decode {1} t/s, TTFT {2}s, VRAM 3090={3}MiB" -f $row.prefill_tok_s, $row.gen_tok_s, $row.ttft_s, $row.vram3090_max_mib) -ForegroundColor Green }
}

# Дописываем в CSV (файл общий для всех профилей — сравнение в одной таблице).
$new = $rows | ConvertTo-Csv -NoTypeInformation
if (Test-Path $OutCsv) {
    $old = Get-Content $OutCsv | Select-Object -First 1
    if ($old -ne ($new | Select-Object -First 1)) { throw "CSV $OutCsv имеет другую схему — перенеси его и запусти заново" }
    $new | Select-Object -Skip 1 | Add-Content $OutCsv
} else {
    $new | Set-Content $OutCsv
}
Write-Host "== Результаты ==" -ForegroundColor Cyan
$rows | Format-Table profile, ctx, load_s, ttft_s, prefill_tok_s, gen_tok_s, vram3090_max_mib, vram3060_max_mib, status -AutoSize
Write-Host "CSV: $OutCsv"
