@echo off
chcp 65001
setlocal EnableExtensions
cd /d "%~dp0"

echo ============================================================
echo   KIBBORG - START ves stack
echo ============================================================
echo   Движок сам поднимет то, чего ещё нет:
echo   - мозг llama-server, если порт свободен
echo   - Qwen3-TTS, если порт свободен
echo   - whisper.cpp, если задан в settings.ini
echo   Панель: 127.0.0.1:8090
echo   Стоп всего стека: Stop.cmd
echo.

set "EXE=%~dp0kibborg-go-engine.exe"

if not exist "%EXE%" (
    echo Binary not found: %EXE%
    echo Running lint + build via build.cmd ...
    call "%~dp0build.cmd"
    if errorlevel 1 (
        echo [FAIL] Lint/build failed. Install Go and try again, Menu 4.
        pause
        exit /b 1
    )
)

if not exist "%EXE%" (
    echo [FAIL] Still no binary after build: %EXE%
    pause
    exit /b 1
)

echo Checking for previous engine instance...
taskkill /FI "WINDOWTITLE eq KIBBORG-ENGINE*" /F
taskkill /IM kibborg-go-engine.exe /F
ping -n 3 127.0.0.1 > "%TEMP%\kibborg-wait.txt"

echo Applying GPU power limits (GPU_POWER_LIMIT_W, UAC only if change needed)...
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0apply-gpu-power.ps1"

echo Starting: %EXE%
echo Мозг грузится в VRAM 1-5 мин, либо берёт уже тёплый llama-server.
echo Панель: 127.0.0.1:8090
echo Стоп: Stop.cmd или Ctrl+C
echo.
"%EXE%"
set "RC=%ERRORLEVEL%"
echo.
echo Kibborg stopped. exit=%RC%
pause
exit /b %RC%