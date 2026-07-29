#ifndef AppVersion
  #define AppVersion "0.1.3"
#endif
#ifndef SourceExe
  #define SourceExe "athena-launcher.exe"
#endif
#ifndef OutputDir
  #define OutputDir "."
#endif

[Setup]
AppId={{D044733B-4DF1-44B4-9231-C154E41DF30E}
AppName=Athena
AppVersion={#AppVersion}
AppPublisher=Athena
AppPublisherURL=https://github.com/good-fish-man/athena-launcher
DefaultDirName={localappdata}\Programs\Athena
DefaultGroupName=Athena
DisableProgramGroupPage=yes
OutputDir={#OutputDir}
OutputBaseFilename=Athena-Setup_{#AppVersion}_windows_amd64
Compression=lzma2
SolidCompression=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayName=Athena
WizardStyle=modern

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "athena-launcher.exe"; Flags: ignoreversion
Source: "{#SourcePath}\..\..\LICENSE"; DestDir: "{app}\licenses"; DestName: "LICENSE.txt"; Flags: ignoreversion
Source: "{#SourcePath}\..\..\NOTICE"; DestDir: "{app}\licenses"; DestName: "NOTICE.txt"; Flags: ignoreversion
Source: "{#SourcePath}\..\..\THIRD_PARTY_NOTICES.md"; DestDir: "{app}\licenses"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Athena"; Filename: "{app}\athena-launcher.exe"; Parameters: "launch"; WorkingDir: "{app}"
Name: "{autodesktop}\Athena"; Filename: "{app}\athena-launcher.exe"; Parameters: "launch"; WorkingDir: "{app}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts:"; Flags: checkedonce

[Run]
Filename: "{app}\athena-launcher.exe"; Parameters: "launch"; Description: "Start Athena"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{app}\athena-launcher.exe"; Parameters: "stop"; Flags: runhidden; RunOnceId: "StopAthena"
