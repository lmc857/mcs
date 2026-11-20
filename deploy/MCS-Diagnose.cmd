@echo off
setlocal
title MCS Diagnose
net session >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
set "REPORT=%USERPROFILE%\Desktop\MCS-diagnose.txt"
set "PS1=%~dp0deploy\diagnose-windows.ps1"
if not exist "%PS1%" set "PS1=%~dp0diagnose-windows.ps1"
if not exist "%PS1%" (
  echo diagnose-windows.ps1 not found.
  pause
  exit /b 1
)
echo Collecting diagnostics...
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" > "%REPORT%" 2>&1
echo.
echo Report: %REPORT%
start "" notepad "%REPORT%"
pause
