@echo off
REM Force clean cmd.exe context (important when run from PowerShell or the Menu)
if not "%~1"=="_start_" (
    cmd /c "%~f0" _start_ %*
    exit /b
)

chcp 65001 >nul
cd /d "%~dp0"

echo ============================================================
echo   KIBBORG Go Engine - ЗАПУСК
echo ============================================================
echo/

REM Один бинарник поднимает всё: Telegram-бот + мозг (llama-server через LLAMA_SERVER
REM из settings.ini) + whisper (если задан) + веб-панель. Отдельные старт-скрипты не нужны.
if not exist kibborg-go-engine.exe (
    echo Бинарник kibborg-go-engine.exe не найден — собираю...
    go build -buildvcs=false -o kibborg-go-engine.exe .
    if errorlevel 1 (
        echo [ОШИБКА] Сборка не удалась. Установи Go и повтори ^(или Меню -^> 4^).
        pause
        exit /b 1
    )
)

echo Запускаю Kibborg. Мозг грузится в VRAM 1-5 минут в фоне.
echo Веб-панель: http://127.0.0.1:8090   ^|   Остановка: Stop.cmd или Ctrl+C
echo/
kibborg-go-engine.exe
echo/
echo Kibborg остановлен.
pause
