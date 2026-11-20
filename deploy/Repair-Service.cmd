@echo off
setlocal EnableExtensions
title MCS Repair Service
net session >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
set "APP=C:\Program Files\MCS"
set "DATA=C:\ProgramData\MCS"
set "SVC=%~dp0deploy\install-service.ps1"
if not exist "%SVC%" set "SVC=%~dp0install-service.ps1"
if not exist "%SVC%" set "SVC=%APP%\install-service.ps1"
set "PREP=%~dp0deploy\prepare-config.ps1"
if not exist "%PREP%" set "PREP=%~dp0prepare-config.ps1"
if not exist "%PREP%" set "PREP=%APP%\prepare-config.ps1"
echo Using install-service.ps1: %SVC%
echo Using prepare-config.ps1: %PREP%
echo.
if not exist "%SVC%" (
  echo ERROR: install-service.ps1 not found.
  pause
  exit /b 1
)
if not exist "%PREP%" (
  echo ERROR: prepare-config.ps1 not found.
  pause
  exit /b 1
)
if not exist "%APP%\config.yaml" (
  echo ERROR: %APP%\config.yaml not found. Install MCS first.
  pause
  exit /b 1
)

echo ==== stop old service ====
powershell -NoProfile -ExecutionPolicy Bypass -File "%SVC%" -InstallDir "%APP%" -DataDir "%DATA%" -StopOnly
echo.
echo ==== fix config ====
powershell -NoProfile -ExecutionPolicy Bypass -File "%PREP%" -Config "%APP%\config.yaml" -DataDir "%DATA%" -ServerIP auto -HTTPPort 0
set "PREP=%ERRORLEVEL%"
echo prepare exit=%PREP%
echo.
echo ==== install service ====
powershell -NoProfile -ExecutionPolicy Bypass -File "%SVC%" -InstallDir "%APP%" -DataDir "%DATA%"
set "RC=%ERRORLEVEL%"
echo.
echo install-service exit=%RC%
echo.
sc.exe query MCS
echo.
if not "%RC%"=="0" (
  echo Repair failed. Try Install-Debug.cmd and send the window output.
) else (
  echo Service install command completed.
)
pause
