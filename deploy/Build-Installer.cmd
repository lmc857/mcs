@echo off
setlocal EnableExtensions
title Build MCS Installer
cd /d "%~dp0"
set "VERSION=%~1"
if "%VERSION%"=="" set "VERSION=0.3.0"

echo ============================================
echo  MCS Installer Builder
echo  Version: %VERSION%
echo ============================================
echo.

if not exist "dist\mcs-windows-amd64.exe" (
  echo ERROR: dist\mcs-windows-amd64.exe not found.
  echo Extract the package to C:\MCS or D:\MCS and try again.
  pause
  exit /b 1
)
if not exist "deploy\mcs.iss" (
  echo ERROR: deploy\mcs.iss not found.
  pause
  exit /b 1
)

if not exist "deploy\MCS-Diagnose.cmd" if exist "MCS-Diagnose.cmd" copy /Y "MCS-Diagnose.cmd" "deploy\" >nul
if not exist "deploy\MCS-Repair.cmd" if exist "MCS-Repair.cmd" copy /Y "MCS-Repair.cmd" "deploy\" >nul
if not exist "deploy\MCS-Diagnose.cmd" (
  echo ERROR: deploy\MCS-Diagnose.cmd not found.
  echo Copy MCS-Diagnose.cmd from the package root into deploy\ and retry.
  pause
  exit /b 1
)
if not exist "deploy\MCS-Repair.cmd" (
  echo ERROR: deploy\MCS-Repair.cmd not found.
  echo Copy MCS-Repair.cmd from the package root into deploy\ and retry.
  pause
  exit /b 1
)

set "ISCC="
where ISCC.exe >nul 2>&1 && set "ISCC=ISCC.exe"
if not defined ISCC if exist "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" set "ISCC=C:\Program Files (x86)\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "C:\Program Files\Inno Setup 6\ISCC.exe" set "ISCC=C:\Program Files\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe" set "ISCC=%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe"

if not defined ISCC (
  echo ERROR: Inno Setup 6 was not found.
  echo.
  echo Install it from:
  echo   https://jrsoftware.org/isdl.php
  echo.
  echo After installation, double-click this file again.
  start "" "https://jrsoftware.org/isdl.php"
  pause
  exit /b 1
)

echo Using Inno Setup: %ISCC%
echo Building installer...
echo.
"%ISCC%" /DMyAppVersion=%VERSION% "deploy\mcs.iss"
if errorlevel 1 (
  echo.
  echo BUILD FAILED. See the messages above.
  pause
  exit /b 1
)

echo.
echo ============================================
echo  BUILD OK
echo  Output: dist\MCS-Setup-%VERSION%.exe
echo ============================================
if exist "dist\MCS-Setup-%VERSION%.exe" (
  start "" explorer "dist"
) else (
  echo WARNING: expected output file not found.
)
pause
