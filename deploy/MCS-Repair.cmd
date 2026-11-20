@echo off
setlocal
title MCS Repair
net session >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
set "PS1=%~dp0deploy\repair-windows.ps1"
if not exist "%PS1%" set "PS1=%~dp0repair-windows.ps1"
if not exist "%PS1%" (
  echo repair-windows.ps1 not found.
  pause
  exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%"
set "RC=%ERRORLEVEL%"
echo.
if not "%RC%"=="0" (
  echo Repair failed. Check C:\ProgramData\MCS\mcs.log
) else (
  echo Repair completed.
)
pause
