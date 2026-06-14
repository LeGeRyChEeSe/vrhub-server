@echo off
REM build.cmd — build the vrhub-server binary for Windows.
REM
REM Two artefacts are produced under bin\:
REM   1. vrhub-server.exe          — the Go binary
REM   2. vrhub-server.exe.manifest — the sidecar manifest that
REM      requests requireAdministrator (UAC elevation at launch).
REM
REM Windows picks up the sidecar manifest automatically when both
REM files sit next to the executable. The manifest makes the
REM launcher ask for admin consent on every start, so the internal
REM firewall helper (cmd\server\main.go → internal\firewall) can
REM invoke `netsh advfirewall firewall add rule` and open the
REM listening TCP port without manual firewall clicks.
REM
REM Run from the repository root:
REM   build.cmd
REM Then launch with:
REM   bin\vrhub-server.exe

setlocal

set "BIN_DIR=bin"
set "EXE=%BIN_DIR%\vrhub-server.exe"
set "MANIFEST_SRC=cmd\server\vrhub-server.exe.manifest"
set "MANIFEST_DST=%BIN_DIR%\vrhub-server.exe.manifest"

if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

echo === Building vrhub-server.exe ===
go build -o "%EXE%" .\cmd\server\
if errorlevel 1 (
    echo build failed.
    exit /b 1
)

echo === Copying sidecar manifest ===
copy /Y "%MANIFEST_SRC%" "%MANIFEST_DST%" >nul
if errorlevel 1 (
    echo failed to copy manifest.
    exit /b 1
)

echo.
echo Build OK.
echo   %EXE%
echo   %MANIFEST_DST%
echo.
echo Launch with:
echo   %EXE%
echo A UAC consent prompt will appear (admin rights required for
echo the firewall helper to open the TCP port automatically).

endlocal
