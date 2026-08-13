@echo off
setlocal EnableExtensions
chcp 65001 >nul
cd /d "%~dp0"

if defined GOPATH set "PATH=%PATH%;%GOPATH%\bin"
if exist "%USERPROFILE%\go\bin" set "PATH=%PATH%;%USERPROFILE%\go\bin"

echo ============================================================
echo   KIBBORG Go Engine - LINT + BUILD
echo ============================================================
echo.

where go >nul 2>&1
if errorlevel 1 (
    echo [FAIL] Go not found in PATH.
    exit /b 1
)

echo [1/4] gofmt -l .
gofmt -l . > "%TEMP%\kibborg-gofmt.txt" 2>&1
if errorlevel 1 (
    echo [FAIL] gofmt failed.
    type "%TEMP%\kibborg-gofmt.txt" 2>nul
    exit /b 1
)
for %%A in ("%TEMP%\kibborg-gofmt.txt") do set "GOFMT_SIZE=%%~zA"
if not "%GOFMT_SIZE%"=="0" (
    echo Unformatted files:
    type "%TEMP%\kibborg-gofmt.txt"
    echo.
    echo [FAIL] gofmt: run  gofmt -w .
    exit /b 1
)
echo   OK

echo [2/4] go vet ./...
go vet ./...
if errorlevel 1 (
    echo [FAIL] go vet failed - build stopped.
    exit /b 1
)
echo   OK

echo [3/4] staticcheck ./...
where staticcheck >nul 2>&1
if errorlevel 1 (
    echo   staticcheck missing - installing...
    go install honnef.co/go/tools/cmd/staticcheck@latest
    if errorlevel 1 (
        echo [FAIL] could not install staticcheck.
        exit /b 1
    )
    if defined GOPATH set "PATH=%PATH%;%GOPATH%\bin"
    if exist "%USERPROFILE%\go\bin" set "PATH=%PATH%;%USERPROFILE%\go\bin"
)
staticcheck ./...
if errorlevel 1 (
    echo [FAIL] staticcheck failed - build stopped.
    exit /b 1
)
echo   OK

echo [4/4] go build -buildvcs=false -o kibborg-go-engine.exe .
go build -buildvcs=false -o kibborg-go-engine.exe .
if errorlevel 1 (
    echo [FAIL] go build failed.
    exit /b 1
)

echo.
echo DONE: kibborg-go-engine.exe  [lint + build OK]
exit /b 0