@echo off
setlocal EnableExtensions
chcp 65001 >nul
cd /d "%~dp0"

echo ============================================================
echo   KIBBORG Go Engine - START
echo ============================================================
echo.

set "EXE=%~dp0kibborg-go-engine.exe"

if not exist "%EXE%" (
    echo Binary not found: %EXE%
    echo Running lint + build via build.cmd ...
    call "%~dp0build.cmd"
    if errorlevel 1 (
        echo [FAIL] Lint/build failed. Install Go and try again ^(Menu - 4^).
        pause
        exit /b 1
    )
)

if not exist "%EXE%" (
    echo [FAIL] Still no binary after build: %EXE%
    pause
    exit /b 1
)

REM --- Single instance: kill any previous engine so Web :8090 and Telegram getUpdates
REM don't collide (port bind error / HTTP 409 Conflict). Brain (llama-server) is left
REM running so the model stays warm in VRAM (STOP_BRAIN_ON_EXIT=false).
echo Checking for previous engine instance...
taskkill /FI "WINDOWTITLE eq KIBBORG-ENGINE*" /F >nul 2>&1
taskkill /IM kibborg-go-engine.exe /F >nul 2>&1
REM Brief pause so Windows releases ports 8090/8002 after kill.
timeout /t 2 /nobreak >nul

echo Starting: %EXE%
echo Brain loads into VRAM for 1-5 min in background ^(or reuses already-running llama-server^).
echo Web UI: http://127.0.0.1:8090
echo Stop: Stop.cmd or Ctrl+C
echo.
"%EXE%"
set "RC=%ERRORLEVEL%"
echo.
echo Kibborg stopped. exit=%RC%
pause
exit /b %RC%
