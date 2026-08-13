@echo off
REM Force clean cmd.exe context (important when run from PowerShell or other hosts)
if not "%~1"=="_start_" (
    cmd /c "%~f0" _start_ %*
    exit /b
)

setlocal enabledelayedexpansion
chcp 65001 >nul
cd /d "%~dp0"

echo ============================================================
echo   KIBBORG Go Engine - ОСТАНОВКА (Мозг + Go Инструменты)
echo ============================================================
echo/

call :LoadSettings

echo Остановка LLM-Мозга (порт %PORT_BRAIN%)...
taskkill /FI "WINDOWTITLE eq KIBBORG-BRAIN*" /F >nul 2>&1
taskkill /IM llama-server.exe /F >nul 2>&1

echo Остановка Go Trading Engine (порт %PORT_ENGINE%)...
taskkill /FI "WINDOWTITLE eq KIBBORG-ENGINE*" /F >nul 2>&1
taskkill /IM kibborg-go-engine.exe /F >nul 2>&1

echo/
echo Стек остановлен.
echo/
pause

goto :eof

:LoadSettings
set PORT_BRAIN=8083
set PORT_ENGINE=8002

if exist settings.ini (
    for /f "usebackq tokens=1,2 delims== eol=#" %%a in ("settings.ini") do (
        set "key=%%a"
        set "val=%%b"
        for /f "tokens=* delims= " %%x in ("!key!") do set "key=%%x"
        for /f "tokens=* delims= " %%x in ("!val!") do set "val=%%x"
        if not "!key!"=="" (
            set "!key!=!val!"
        )
    )
)
goto :eof
