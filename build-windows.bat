@echo off
REM Build claw.exe (web) for Windows — output: .\claw.exe in script dir.
setlocal enabledelayedexpansion

cd /d "%~dp0"

set "REQ_MAJOR=1"
set "REQ_MINOR=25"

REM --- Go toolchain check ---------------------------------------------------
where go >nul 2>nul
if errorlevel 1 (
    echo Error: Go is not installed or not in PATH.
    echo        Please install Go %REQ_MAJOR%.%REQ_MINOR% or later from https://go.dev/dl/
    goto :failed
)

for /f "delims=" %%v in ('go env GOVERSION') do set "GOVERSION=%%v"
set "GOVERSION=%GOVERSION:go=%"
for /f "tokens=1,2 delims=." %%a in ("%GOVERSION%") do (
    set "GOMAJOR=%%a"
    set "GOMINOR=%%b"
)
set "GOMAJOR=%GOMAJOR: =%"
set "GOMINOR=%GOMINOR: =%"

set "GOOK=0"
if %GOMAJOR% GTR %REQ_MAJOR% set "GOOK=1"
if %GOMAJOR% EQU %REQ_MAJOR% if %GOMINOR% GEQ %REQ_MINOR% set "GOOK=1"

if not "%GOOK%"=="1" (
    echo Error: Go %GOMAJOR%.%GOMINOR% is too old ^(need %REQ_MAJOR%.%REQ_MINOR%+^).
    echo        Please update Go from https://go.dev/dl/
    goto :failed
)
echo [go] %GOMAJOR%.%GOMINOR% detected, OK.

REM --- Build ----------------------------------------------------------------
echo [build] claw (windows/amd64, web)
go build -tags web -ldflags "-H windowsgui" -o claw.exe ./cmd/claw
if errorlevel 1 goto :failed
echo [done]  %CD%\claw.exe
endlocal
echo.
pause
exit /b 0

:failed
echo.
echo Build failed.
pause
exit /b 1
