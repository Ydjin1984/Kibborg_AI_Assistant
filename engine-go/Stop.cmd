@echo off
chcp 65001
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"

REM Do not redirect to nul: after UTF-8 Windows opens "%CD%\nul"
REM and shows "Internet security settings prevented opening these files".

echo ============================================================
echo   KIBBORG - STOP ves stack
echo ============================================================
echo.

call :LoadSettings

echo Останавливаю панель и Telegram-бота...
call :KillByTitle KIBBORG-ENGINE*
call :KillByImage kibborg-go-engine.exe

echo Останавливаю мозг llama-server, порт !PORT_BRAIN!...
call :KillByTitle KIBBORG-BRAIN*
call :KillByImage llama-server.exe

echo Останавливаю Qwen3-TTS, порт !PORT_TTS!...
call :KillByPort !PORT_TTS!

echo Останавливаю whisper.cpp, порт !PORT_WHISPER!...
call :KillByImage whisper-server.exe
call :KillByPort !PORT_WHISPER!

echo Останавливаю embed-сервер, порт !PORT_EMBED!...
call :KillByPort !PORT_EMBED!

echo.
echo Всё выключено: панель, мозг, озвучка.
echo TypeWhisper в трее не трогал.
echo.
if /i not "%~1"=="/nopause" pause
exit /b 0

:KillByTitle
taskkill /FI "WINDOWTITLE eq %~1" /F 2>&1 | find /i "PID"
if errorlevel 1 echo   окно %~1 не найдено.
goto :eof

:KillByImage
taskkill /IM "%~1" /F 2>&1 | find /i "PID"
if errorlevel 1 echo   %~1 не был запущен.
goto :eof

:KillByPort
set "KP=%~1"
if "!KP!"=="" goto :eof
powershell -NoProfile -ExecutionPolicy Bypass -Command " $port=[int]'!KP!'; $conns=@(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue); if($conns.Count -eq 0){ Write-Output ('  порт '+$port+' свободен.'); exit 0 }; $conns | ForEach-Object { $procId=[int]$_.OwningProcess; if($procId -lt 8){ return }; $p=Get-Process -Id $procId -ErrorAction SilentlyContinue; $name=if($p){$p.ProcessName}else{'?'}; try{ Stop-Process -Id $procId -Force -ErrorAction Stop; Write-Output ('  остановлен PID '+$procId+' '+$name+' порт '+$port) } catch { Write-Output ('  не смог PID '+$procId+' '+$name+': '+$_.Exception.Message) } }"
goto :eof

:LoadSettings
set PORT_BRAIN=8083
set PORT_ENGINE=8002
set PORT_TTS=7788
set PORT_WHISPER=8081
set PORT_EMBED=8082

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