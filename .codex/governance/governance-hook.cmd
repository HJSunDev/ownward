@echo off
setlocal
set "GOVERNANCE_BINARY=%~dp0bin\governance-cli.exe"

if exist "%GOVERNANCE_BINARY%" goto run
if /I "%~1"=="hook" (
  echo {}
  exit /b 0
)
1>&2 echo governance runtime is not installed; run install-runtime.ps1
exit /b 1

:run
"%GOVERNANCE_BINARY%" %*
set "GOVERNANCE_EXIT=%ERRORLEVEL%"
if /I "%~1"=="hook" (
  if not "%GOVERNANCE_EXIT%"=="0" echo {}
  exit /b 0
)
exit /b %GOVERNANCE_EXIT%
