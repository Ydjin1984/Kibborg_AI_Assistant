@echo off
REM Force clean cmd.exe context (important when run from PowerShell or other hosts)
if not "%~1"=="_start_" (
    cmd /c "%~f0" _start_ %*
    exit /b
)

chcp 65001 >nul
cd /d "%~dp0"

:Menu
cls
echo ============================================================
echo   KIBBORG Go Engine - МЕНЮ / ДАШБОРД
echo ============================================================
echo/
echo 1. Скачать рекомендованные модели (Qwen + Gemma под твоё железо)
echo 2. Изменить глобальные настройки (путь к модели, порты, параметры llama и т.д. — именно так переключаешь модели!)
echo 3. Показать текущие настройки
echo 4. Собрать Go-бинарник
echo 5. Запустить полный стек (Мозг + Go Engine)
echo 6. Остановить полный стек
echo 7. Запустить Chrome с отладкой (порт 9222) — для команды /browser
echo 0. Выход
echo/
set /p choice="Выбери пункт: "

if "%choice%"=="1" goto Download
if "%choice%"=="2" goto Settings
if "%choice%"=="3" goto ShowSettings
if "%choice%"=="4" goto Build
if "%choice%"=="5" call Start.cmd
if "%choice%"=="6" call Stop.cmd
if "%choice%"=="7" goto ChromeDebug
if "%choice%"=="0" exit

goto Menu


:Download
echo/
echo Скачиваю рекомендованные модели (Qwen3.6-35B UD-IQ4_XS + mmproj + Gemma)...
powershell -NoProfile -ExecutionPolicy Bypass -Command "pip install -U -q huggingface_hub; hf download unsloth/Qwen3.6-35B-A3B-GGUF Qwen3.6-35B-A3B-UD-IQ4_XS.gguf --local-dir models\brain\Qwen3.6-35B-A3B; hf download unsloth/Qwen3.6-35B-A3B-GGUF mmproj-BF16.gguf --local-dir models\vision; hf download bartowski/google_gemma-4-26B-A4B-it-GGUF google_gemma-4-26B-A4B-it-IQ4_XS.gguf --local-dir models\brain\Gemma4-26B-A4B"
echo/
echo Скачивание завершено. Укажи MODEL_PATH и MMPROJ_PATH в settings.ini, чтобы выбрать нужную.
pause
goto Menu

:Settings
echo/
echo Редактирование глобальных настроек (MODEL_PATH, MMPROJ_PATH, порты, LLAMA_* параметры). Сохраняется в settings.ini.
echo После правки перезапусти стек, чтобы изменения применились.
echo/
if exist settings.ini goto :EditSettings

echo Создаю settings.ini по умолчанию...
powershell -NoProfile -Command @"
# Настройки Kibborg Go Engine — редактируй этот файл, чтобы выбрать модель и параметры. Код править не нужно!
# После изменений перезапусти стек.

# === МОДЕЛЬ МОЗГА (llama-server) ===
MODEL_PATH=models\brain\Gemma4-26B-A4B\google_gemma-4-26B-A4B-it-IQ4_XS.gguf

# Опциональный mmproj для зрения (Qwen и т.п.). Оставь пустым для Gemma или текстовых моделей.
MMPROJ_PATH=

# === ИСПОЛНЯЕМЫЙ ФАЙЛ LLAMA-SERVER (ОБЯЗАТЕЛЬНО) ===
# Положи llama-server.exe (и его .dll) сюда ИЛИ укажи полный путь к своей сборке.
LLAMA_SERVER=.\llama-server.exe
# Пример: LLAMA_SERVER=D:\llama.cpp\cuda-bin-b9550\llama-server.exe

# === ПОРТЫ ===
PORT_BRAIN=8080
PORT_ENGINE=8002

# === ПАРАМЕТРЫ LLAMA-SERVER (подбери под железо) ===
LLAMA_THREADS=28
LLAMA_CTX_SIZE=32768
LLAMA_GPU_LAYERS=99
# Для двух GPU:
# LLAMA_TENSOR_SPLIT=0.5,0.5
# LLAMA_MAIN_GPU=0
"@ | Out-File -FilePath settings.ini -Encoding UTF8

:EditSettings
notepad settings.ini
echo/
echo Настройки обновлены. Перезапусти стек, чтобы изменения вступили в силу.
pause
goto Menu

:ShowSettings
echo/
echo Текущие настройки:
if exist settings.ini (
    type settings.ini
) else (
    echo Нет settings.ini — используются значения по умолчанию (Gemma4 только мозг).
)
echo/
pause
goto Menu

:Build
echo/
echo Сборка Go-бинарника (kibborg-go-engine.exe)...
go build -buildvcs=false -o kibborg-go-engine.exe .
if %ERRORLEVEL% EQU 0 (
    echo Сборка успешна!
) else (
    echo Сборка не удалась. Убедись, что Go установлен.
)
pause
goto Menu

:ChromeDebug
echo/
echo Запускаю Google Chrome с удалённой отладкой на порту 9222 (нужно для команды /browser)...
REM Отдельный профиль runtime\chrome-debug — иначе, если Chrome уже открыт на обычном
REM профиле, флаг --remote-debugging-port молча не поднимет порт 9222. Открывай рабочие
REM вкладки именно в этом окне — агент работает с ними.
set "CHROME="
if exist "%ProgramFiles%\Google\Chrome\Application\chrome.exe" set "CHROME=%ProgramFiles%\Google\Chrome\Application\chrome.exe"
if not defined CHROME if exist "%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe" set "CHROME=%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe"
if not defined CHROME if exist "%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe" set "CHROME=%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe"
if not defined CHROME (
    echo Не нашёл chrome.exe в стандартных папках. Запусти вручную:
    echo     chrome.exe --remote-debugging-port=9222 --user-data-dir="%~dp0runtime\chrome-debug"
    pause
    goto Menu
)
start "" "%CHROME%" --remote-debugging-port=9222 --user-data-dir="%~dp0runtime\chrome-debug"
echo/
echo Chrome запущен. Открой нужную вкладку и пользуйся /browser в Telegram.
echo Проверка порта: открой в браузере  http://127.0.0.1:9222/json/version
pause
goto Menu
