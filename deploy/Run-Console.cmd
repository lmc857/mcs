@echo off
setlocal EnableExtensions
title MCS Console
cd /d "C:\Program Files\MCS"
if not exist "mcs.exe" (
  echo ERROR: C:\Program Files\MCS\mcs.exe not found.
  pause
  exit /b 1
)
echo If MCS service is running, stop it first:
echo   sc stop MCS
echo.
echo Starting mcs.exe in console mode. Press Ctrl+C to stop.
echo.
"mcs.exe" -config "C:\Program Files\MCS\config.yaml" -log "C:\ProgramData\MCS\console.log"
echo.
echo mcs.exe exited with code %ERRORLEVEL%.
pause
