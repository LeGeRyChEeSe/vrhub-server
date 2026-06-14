@echo off
REM run-with-logs.bat — launch the server with stdout/stderr captured
REM to disk so the operator can read the access log after the fact.
REM This wrapper exists because Start-Process -Verb RunAs (which we
REM need for the UAC elevation that netsh advfirewall requires) does
REM not accept -RedirectStandardOutput / -RedirectStandardError.
REM Launch via:
REM   Start-Process -FilePath run-with-logs.bat -Verb RunAs

cd /d "%~dp0"

if exist server-out.log del server-out.log
if exist server-err.log del server-err.log

bin\vrhub-server.exe > server-out.log 2> server-err.log
