@echo off
setlocal EnableExtensions
title MCS Install Debug
net session >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
set "APP=C:\Program Files\MCS"
set "DATA=C:\ProgramData\MCS"
echo ============================================
echo  MCS install debug
echo  APP=%APP%
echo  DATA=%DATA%
echo ============================================
echo.
set "PREP=%~dp0deploy\prepare-config.ps1"
if not exist "%PREP%" set "PREP=%APP%\prepare-config.ps1"
set "SVC=%~dp0deploy\install-service.ps1"
if not exist "%SVC%" set "SVC=%APP%\install-service.ps1"
if not exist "%PREP%" (
  echo ERROR: prepare-config.ps1 not found.
  echo Install MCS first, or run this from the installed app directory.
  pause
  exit /b 1
)
if not exist "%SVC%" (
  echo ERROR: install-service.ps1 not found.
  pause
  exit /b 1
)
if not exist "%DATA%" mkdir "%DATA%" >nul 2>&1

echo ==== 1. prepare-config ====
powershell -NoProfile -ExecutionPolicy Bypass -File "%PREP%" -Config "%APP%\config.yaml" -DataDir "%DATA%" -ServerIP auto -HTTPPort 0
set "PREP=%ERRORLEVEL%"
echo prepare exit=%PREP%
echo.

echo ==== 2. install-service ====
powershell -NoProfile -ExecutionPolicy Bypass -File "%SVC%" -InstallDir "%APP%" -DataDir "%DATA%"
set "INST=%ERRORLEVEL%"
echo install exit=%INST%
echo.

echo ==== 3. service status ====
sc.exe query MCS
echo.
echo ==== 4. config http/advertise ====
findstr /I /C:"http:" /C:"advertise:" /C:"data_dir:" "%APP%\config.yaml"
echo.
echo If this window shows an error, take a screenshot or copy the text.
pause
