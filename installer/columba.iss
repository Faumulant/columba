; Columba（飞鸠）安装器 —— Inno Setup 6 脚本
;
; ─── 为什么是「按用户安装」而不是 Program Files ───────────────
;
; 最初写成 PrivilegesRequired=admin（装到 Program Files），实测在用户机器上
; 第一步解压就失败：
;     Setup was unable to create the directory
;     "C:\Users\<短名>\AppData\Local\Temp\is-XXXX.tmp"  Error 5: 拒绝访问
; 该机器 USERNAME 是 8.3 短名（Faumu），而用户目录是长名（Faumu），
; 提权后的进程访问短名路径被拒。这不是本项目能修的，但可以绕开：
;
; 改成 PrivilegesRequired=lowest → 解压发生在**普通用户上下文**，
; TEMP 确定可写（已实测 writable=True）；安装目录变成
; %LOCALAPPDATA%\Programs\Columba（{autopf} 在 lowest 下自动切到这里），
; 用户目录自己就能写，不需要任何提权。
;
; 唯一需要管理员的是**防火墙入站规则**（局域网自动发现依赖它）。
; 那一步单独用 runas 触发一次 UAC，用户拒绝也不影响程序其余部分可用
; （面板/发送/接收都正常，只是对端可能发现不到本机）。
;
; ─── 其他设计要点 ───────────────────────────────────────────
;
; 1. 目录布局必须保持 exe 与 extension\ 同级
;    core/ipc 的 FirefoxExtDir()/chromiumExtDir() 用 os.Executable() 推
;    <exe目录>\extension\{firefox,chromium}，布局一变就找不到扩展。
;
; 2. 系统集成交给 `drop.exe install`，安装器不重复实现
;    防火墙、drop: 协议、Native Messaging 宿主、开机自启、SendTo 底座都在里面
;    且已逐项验证。安装器只负责「放对文件 + 调用它」。
;
; 3. 卸载时「是否删除本机数据」做成可选
;    默认保留 %LOCALAPPDATA%\Drop（身份/信任库/历史），重装不用重新配对。

#define AppName "Columba"
#define AppNameCN "Columba（飞鸠）"
#define AppVersion "0.1"
#define AppPublisher "Faumulant"
#define AppURL "https://github.com/Faumulant/columba"
#define AppExeName "dropw.exe"

[Setup]
AppId={{8F3A2C71-4B9E-4D62-9C15-7E2B5A0D3F84}
AppName={#AppNameCN}
AppVersion={#AppVersion}
AppVerName={#AppNameCN} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}
AppUpdatesURL={#AppURL}/releases
; lowest：解压在普通用户上下文完成，避开提权后的 TEMP 访问问题（见文件头）
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppNameCN}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
; 允许用户在命令行用 /ALLUSERS 覆盖（需要管理员时才用）
PrivilegesRequiredOverridesAllowed=commandline
OutputDir=dist
OutputBaseFilename=Columba-{#AppVersion}-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayName={#AppNameCN}
UninstallDisplayIcon={app}\{#AppExeName}

[Languages]
Name: "chinesesimplified"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务:"; Flags: checkedonce

[Files]
; 主程序：drop.exe 是 CLI，dropw.exe 是无窗口 daemon（右键发送靠它不闪窗）
Source: "..\drop.exe";  DestDir: "{app}"; Flags: ignoreversion
Source: "..\dropw.exe"; DestDir: "{app}"; Flags: ignoreversion
; 浏览器扩展：必须与 exe 同级放在 extension\ 下（见文件头 1）
Source: "..\extension\chromium\*"; DestDir: "{app}\extension\chromium"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "..\extension\firefox\*";  DestDir: "{app}\extension\firefox";  Flags: ignoreversion recursesubdirs createallsubdirs
; 文档
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE";   DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppNameCN}"; Filename: "{app}\drop.exe"; Parameters: "panel"; IconFilename: "{app}\dropw.exe"
Name: "{group}\卸载 {#AppNameCN}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppNameCN}"; Filename: "{app}\drop.exe"; Parameters: "panel"; IconFilename: "{app}\dropw.exe"; Tasks: desktopicon

[Run]
; 用户级系统集成：drop: 协议、Native Messaging 宿主、开机自启、SendTo 底座。
; 这些都在 HKCU，普通权限即可，不需要 UAC。
;
; 防火墙那一步 `drop install` 内部会自己处理：失败只打印警告、不中断，
; 其余项照常注册（见 cmd_install.go:20-26）。所以这里**只调一次**，
; 不要为防火墙再跑一遍——重复调用会让同一份输出打两次、也会重复写注册表。
Filename: "{app}\drop.exe"; Parameters: "install"; StatusMsg: "正在注册系统集成（协议、扩展宿主、开机自启）…"; Flags: runhidden waituntilterminated
; 启动后台：--hidden 会以无窗口子进程重启自身，装完即用
Filename: "{app}\dropw.exe"; Parameters: "run --hidden"; Description: "启动 {#AppNameCN} 后台"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 先停 daemon：不停会因 exe 被占用而删不干净
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM dropw.exe"; Flags: runhidden; RunOnceId: "StopDaemon"
; 移除系统集成（协议/宿主/自启/SendTo）。防火墙规则需要管理员，尽力而为。
Filename: "{app}\drop.exe"; Parameters: "uninstall"; StatusMsg: "正在移除系统集成…"; Flags: runhidden waituntilterminated; RunOnceId: "RemoveIntegration"

[UninstallDelete]
Type: filesandordirs; Name: "{app}\extension"

[Code]
// 安装前停掉已在运行的实例，否则覆盖 exe 会因文件占用失败
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM dropw.exe', '', SW_HIDE,
       ewWaitUntilTerminated, ResultCode);
  Result := '';
end;

// 卸载时询问是否删除用户数据（做成可选）。
//
// 不在安装向导里放这个选项：安装时的答案无法影响「卸载」那一刻的行为，
// 放一个不起作用的开关只会误导。真正的决定点就是卸载时。
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: String;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{localappdata}\Drop');
    if DirExists(DataDir) then
    begin
      if MsgBox('是否同时删除本机数据？' + #13#10 + #13#10 +
                DataDir + #13#10 + #13#10 +
                '包含：设备身份、已配对设备、传输历史。' + #13#10 +
                '删除后无法恢复，重装需要重新配对设备。' + #13#10 + #13#10 +
                '选「否」将保留数据（推荐）。',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
      begin
        DelTree(DataDir, True, True, True);
      end;
    end;
  end;
end;

// 安装完成后提示防火墙状态：用户取消了 UAC 时给出可操作指引，
// 而不是让「发现不到设备」变成一个查不出原因的怪现象。
procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    if not WizardSilent then
    begin
      if Exec(ExpandConstant('{sys}\netsh.exe'),
              'advfirewall firewall show rule name="Columba"',
              '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode <> 0) then
      begin
        MsgBox('防火墙规则尚未注册。' + #13#10 + #13#10 +
               '程序本身可以正常使用，但同一局域网内的其他设备可能发现不到本机。' + #13#10 + #13#10 +
               '要补上这一步，请右键点击托盘图标退出程序，然后以管理员身份运行：' + #13#10 +
               ExpandConstant('{app}') + '\drop.exe install',
               mbInformation, MB_OK);
      end;
    end;
  end;
end;
