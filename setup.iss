; ChenFlow 3.0 Inno Setup 安装脚本
; 开发者：chen

[Setup]
AppName=ChenFlow
AppVersion=3.0
AppVerName=ChenFlow
AppPublisher=chen
AppPublisherURL=mailto:924636096@qq.com
AppSupportURL=mailto:924636096@qq.com
DefaultDirName={autopf}\ChenFlow
DefaultGroupName=ChenFlow
UninstallDisplayIcon={app}\ChenFlow.exe
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
OutputDir=installer_output
OutputBaseFileName=ChenFlow_3.0_Setup
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
Source: "dist\ChenFlow\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\ChenFlow"; Filename: "{app}\ChenFlow.exe"
Name: "{group}\卸载 ChenFlow"; Filename: "{uninstallexe}"
Name: "{autodesktop}\ChenFlow"; Filename: "{app}\ChenFlow.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent"; StatusMsg: "正在安装 WebView2 Runtime..."; Flags: skipifdoesntexist; Check: NeedsWebView2
Filename: "{app}\ChenFlow.exe"; Description: "启动 ChenFlow"; Flags: nowait postinstall skipifsilent shellexec runascurrentuser

[UninstallRun]
Filename: "{cmd}"; Parameters: "/C taskkill /F /IM ChenFlow.exe"; Flags: runhidden nowait; RunOnceId: "KillChenFlow"

[UninstallDelete]
Type: filesandordirs; Name: "{app}\data"
Type: filesandordirs; Name: "{app}\logs"
Type: filesandordirs; Name: "{app}\EBWebView"
Type: filesandordirs; Name: "{app}"
Type: filesandordirs; Name: "{userappdata}\ChenFlow"
Type: filesandordirs; Name: "{userappdata}\ChenFlow.exe"
Type: filesandordirs; Name: "{userappdata}\chenflow-go.exe"

[Code]
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DelTree(ExpandConstant('{userappdata}\ChenFlow'), True, True, True);
    DelTree(ExpandConstant('{userappdata}\ChenFlow.exe'), True, True, True);
    DelTree(ExpandConstant('{userappdata}\chenflow-go.exe'), True, True, True);
    DelTree(ExpandConstant('{app}'), True, True, True);
  end;
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
