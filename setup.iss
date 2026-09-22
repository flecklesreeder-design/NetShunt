; NetShunt 3.3.3 Inno Setup 安装脚本
; 开发者：chen

[Setup]
AppId={{A1152A57-157E-4726-9D42-903325084A02}}
AppName=NetShunt
AppVersion=3.3.3
AppVerName=NetShunt
AppPublisher=chen
AppPublisherURL=mailto:924636096@qq.com
AppSupportURL=mailto:924636096@qq.com
DefaultDirName={autopf}\NetShunt
DefaultGroupName=NetShunt
CloseApplications=force
UninstallDisplayIcon={app}\NetShunt.exe
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
OutputDir=installer_output
OutputBaseFileName=NetShunt_3.3.3_Setup
SetupIconFile=logo.ico
LicenseFile=license.txt
InfoBeforeFile=before_install.txt
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
DisableDirPage=no
DisableProgramGroupPage=no

[Languages]
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加图标:"; Flags: unchecked

[Files]
Source: "MicrosoftEdgeWebview2Setup.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "dist\NetShunt\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\NetShunt"; Filename: "{app}\NetShunt.exe"
Name: "{group}\卸载 NetShunt"; Filename: "{uninstallexe}"
Name: "{autodesktop}\NetShunt"; Filename: "{app}\NetShunt.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent"; StatusMsg: "正在安装 WebView2 Runtime..."; Flags: skipifdoesntexist; Check: NeedsWebView2
Filename: "{cmd}"; Parameters: "/C del /a /f /q ""%localappdata%\IconCache.db"" ""%localappdata%\Microsoft\Windows\Explorer\iconcache_*"" 2>nul & ie4uinit.exe -show"; StatusMsg: "正在刷新图标缓存..."; Flags: runhidden
Filename: "{app}\NetShunt.exe"; Description: "启动 NetShunt"; Flags: nowait postinstall skipifsilent shellexec runascurrentuser
Filename: "{app}\NetShunt.exe"; Flags: nowait skipifnotsilent shellexec runascurrentuser

[UninstallRun]
Filename: "{cmd}"; Parameters: "/C taskkill /F /IM NetShunt.exe"; Flags: runhidden nowait; RunOnceId: "KillNetShunt"

[UninstallDelete]
Type: filesandordirs; Name: "{app}\data"
Type: filesandordirs; Name: "{app}\logs"
Type: filesandordirs; Name: "{app}\EBWebView"
Type: filesandordirs; Name: "{app}"
Type: filesandordirs; Name: "{userappdata}\NetShunt"
Type: filesandordirs; Name: "{userappdata}\NetShunt.exe"

[Code]
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DelTree(ExpandConstant('{userappdata}\NetShunt'), True, True, True);
    DelTree(ExpandConstant('{userappdata}\NetShunt.exe'), True, True, True);
    DelTree(ExpandConstant('{app}'), True, True, True);
  end;
end;
function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
end;
function NeedsWebView2: Boolean;
begin
  Result := True;
  if RegKeyExists(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}') then
    Result := False;
  if RegKeyExists(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C63A8C4FE2}') then
    Result := False;
  if RegKeyExists(HKCU, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}') then
    Result := False;
  if RegKeyExists(HKCU, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C63A8C4FE2}') then
    Result := False;
end;
