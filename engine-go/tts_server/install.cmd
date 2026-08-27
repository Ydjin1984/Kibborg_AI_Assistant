@echo off
chcp 65001 >nul
setlocal EnableExtensions
cd /d "%~dp0"

echo ============================================================
echo   Kibborg TTS - Qwen3-TTS 0.6B GPU install
echo ============================================================
echo.

if not exist ".venv\Scripts\python.exe" (
  echo Creating venv...
  python -m venv .venv
  if errorlevel 1 exit /b 1
)

echo Installing PyTorch CUDA...
".venv\Scripts\python.exe" -m pip install --upgrade pip
".venv\Scripts\python.exe" -m pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu126
if errorlevel 1 (
  echo cu126 failed, trying cu124...
  ".venv\Scripts\python.exe" -m pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  if errorlevel 1 exit /b 1
)

echo Installing faster-qwen3-tts (CUDA Graphs)...
".venv\Scripts\python.exe" -m pip uninstall -y qwen-tts 2>nul
".venv\Scripts\python.exe" -m pip install -r requirements.txt
if errorlevel 1 exit /b 1

echo.
echo CUDA check:
".venv\Scripts\python.exe" -c "import torch; print('cuda=', torch.cuda.is_available()); print('gpus=', torch.cuda.device_count()); print([torch.cuda.get_device_name(i) for i in range(torch.cuda.device_count())])"
echo.
echo Done. Model downloads on first start ~2GB.
exit /b 0
