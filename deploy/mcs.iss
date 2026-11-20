; MCS Windows installer (Inno Setup 6).
; Auto-detects LAN IP and a free HTTP port during install.
; Build on Windows:
;   powershell -ExecutionPolicy Bypass -File deploy\build-windows-installer.ps1
; or:
;   ISCC.exe /DMyAppVersion=0.2.0 deploy\mcs.iss
#define MyAppName "MCS"
#define MyAppPublisher "MCS"
#ifndef MyAppVersion
  #define MyAppVersion "0.2.0"
#endif
#define MyAppExeName "mcs.exe"

[Setup]
AppId=MCS-8FEC6E1E-6D0E-4A6E-9C5D-3E2A7B8C9D10
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\MCS
DefaultGroupName=MCS
DisableProgramGroupPage=yes
OutputDir=..\dist
OutputBaseFilename=MCS-Setup-{#MyAppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\{#MyAppExeName}
MinVersion=10.0
SetupLogging=yes

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："; Flags: unchecked

[Files]
Source: "..\dist\mcs-windows-amd64.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}"; Flags: ignoreversion
Source: "..\config.example.yaml"; DestDir: "{app}"; DestName: "config.yaml"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "install-service.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "prepare-config.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "open-console.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "diagnose-windows.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "repair-windows.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "MCS-Diagnose.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "MCS-Repair.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "Install-Debug.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "Repair-Service.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "Run-Console.cmd"; DestDir: "{app}"; Flags: ignoreversion

[Dirs]
Name: "{commonappdata}\MCS"; Permissions: users-modify
Name: "{commonappdata}\MCS\data"; Permissions: users-modify

[Icons]
Name: "{group}\MCS 控制台"; Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\open-console.ps1"""; WorkingDir: "{app}"
Name: "{group}\MCS 配置"; Filename: "notepad.exe"; Parameters: """{app}\config.yaml"""
Name: "{group}\MCS 一键诊断"; Filename: "{app}\MCS-Diagnose.cmd"
Name: "{group}\MCS 一键修复"; Filename: "{app}\MCS-Repair.cmd"
Name: "{group}\MCS 安装诊断"; Filename: "{app}\Install-Debug.cmd"
Name: "{group}\MCS 修复服务"; Filename: "{app}\Repair-Service.cmd"
Name: "{group}\卸载 MCS"; Filename: "{uninstallexe}"
Name: "{autodesktop}\MCS"; Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\open-console.ps1"""; WorkingDir: "{app}"; Tasks: desktopicon

[UninstallRun]
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\install-service.ps1"" -InstallDir ""{app}"" -DataDir ""{commonappdata}\MCS"" -Uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "MCSRemoveService"

[Code]
var
  ServerIP: String;
  HTTPPort: String;

function InitializeSetup(): Boolean;
begin
  ServerIP := ExpandConstant('{param:ServerIP|auto}');
  HTTPPort := ExpandConstant('{param:HTTPPort|0}');
  Result := True;
end;

procedure LogStep(const Msg: String);
var
  DirName, LogFile: String;
begin
  DirName := ExpandConstant('{commonappdata}\MCS');
  ForceDirectories(DirName);
  LogFile := DirName + '\installer.log';
  SaveStringToFile(LogFile, Msg + #13#10, True);
  Log(Msg);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Params, DataDir: String;
  Code: Integer;
begin
  DataDir := ExpandConstant('{commonappdata}\MCS');
  Params := '-NoProfile -ExecutionPolicy Bypass -File "' + ExpandConstant('{app}') + '\install-service.ps1" -InstallDir "' + ExpandConstant('{app}') + '" -DataDir "' + DataDir + '" -StopOnly';
  LogStep('PrepareToInstall: stopping old service');
  if Exec('powershell.exe', Params, '', SW_HIDE, ewWaitUntilTerminated, Code) then
    LogStep('PrepareToInstall: stop exit=' + IntToStr(Code))
  else
    LogStep('PrepareToInstall: stop exec failed');
  Result := '';
end;

function RunPS(FileName: String; Params: String; ShowCmd: Integer; Wait: Boolean; var Code: Integer): Boolean;
begin
  if Wait then
    Result := Exec(FileName, Params, '', ShowCmd, ewWaitUntilTerminated, Code)
  else
    Result := Exec(FileName, Params, '', ShowCmd, ewNoWait, Code);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Params: String;
  Code: Integer;
  InstallDir, DataDir: String;
begin
  if CurStep <> ssPostInstall then Exit;
  InstallDir := ExpandConstant('{app}');
  DataDir := ExpandConstant('{commonappdata}\MCS');
  ForceDirectories(DataDir);
  LogStep('PostInstall: start');

  Params := '-NoProfile -ExecutionPolicy Bypass -File "' + InstallDir + '\install-service.ps1" -InstallDir "' + InstallDir + '" -DataDir "' + DataDir + '" -StopOnly';
  RunPS('powershell.exe', Params, SW_HIDE, True, Code);

  Params := '-NoProfile -ExecutionPolicy Bypass -File "' + InstallDir + '\prepare-config.ps1" -Config "' + InstallDir + '\config.yaml" -DataDir "' + DataDir + '" -ServerIP "' + ServerIP + '" -HTTPPort "' + HTTPPort + '"';
  LogStep('PostInstall: prepare-config');
  if not RunPS('powershell.exe', Params, SW_HIDE, True, Code) or (Code <> 0) then
  begin
    LogStep('PostInstall: prepare-config failed, exit=' + IntToStr(Code));
    if not WizardSilent then
      MsgBox('自动检测 IP/端口失败。请查看：' + DataDir + '\prepare-config.log', mbError, MB_OK);
    Exit;
  end;
  LogStep('PostInstall: prepare-config OK');

  Params := '-NoProfile -ExecutionPolicy Bypass -File "' + InstallDir + '\install-service.ps1" -InstallDir "' + InstallDir + '" -DataDir "' + DataDir + '"';
  LogStep('PostInstall: install-service');
  if not RunPS('powershell.exe', Params, SW_HIDE, True, Code) or (Code <> 0) then
  begin
    LogStep('PostInstall: install-service failed, exit=' + IntToStr(Code));
    if not WizardSilent then
      MsgBox('MCS 服务安装失败。请查看：' + DataDir + '\install-service.log' + #13#10 + DataDir + '\installer.log', mbError, MB_OK);
    Exit;
  end;
  LogStep('PostInstall: install-service OK');

  LogStep('PostInstall: open console');
  Params := '-NoProfile -ExecutionPolicy Bypass -File "' + InstallDir + '\open-console.ps1"';
  RunPS('powershell.exe', Params, SW_SHOWNORMAL, False, Code);
end;
