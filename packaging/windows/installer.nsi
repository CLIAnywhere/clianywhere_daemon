; CLIAnywhere Windows installer (NSIS)
;
; Built on Linux CI (no Wine needed):
;   makensis -NOCD -DVERSION=<x.y.z> packaging/windows/installer.nsi
; Run from the repository root so that "claw.exe" and "icon.ico" resolve.
;
; Per-user install (no UAC):
;   - Installs to %USERPROFILE%\.clianywhere (same dir daemon uses for config)
;   - Uninstall info under HKCU, Start Menu shortcut for current user only
;   - Uninstall keeps user data (daemon.accesskey, security_code, logs);
;     only binaries are removed

Unicode true

!ifndef VERSION
  !define VERSION "0.0.0"
!endif

!define PRODUCT "CLIAnywhere"
!define APP_EXE "claw.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT}"

Name "${PRODUCT} ${VERSION}"
OutFile "clianywhere-setup.exe"
InstallDir "$PROFILE\.clianywhere"
; Reuse install dir from a previous install if present
InstallDirRegKey HKCU "Software\${PRODUCT}" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowUninstDetails show

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES

; Finish page with "Run CLIAnywhere now" checkbox (checked by default)
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Run ${PRODUCT} now"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; Stop a running instance so files can be overwritten:
  ; first try graceful close (WM_CLOSE), then force-kill leftovers.
  ; taskkill exit code 0 = process was found (and signaled/killed),
  ; 128 = not running. Record whether an instance existed before install.
  StrCpy $R0 0
  nsExec::ExecToLog 'taskkill /T /IM ${APP_EXE}'
  Pop $0
  ${If} $0 == 0
    StrCpy $R0 1
  ${EndIf}
  Sleep 1500
  nsExec::ExecToLog 'taskkill /F /T /IM ${APP_EXE}'
  Pop $0
  ${If} $0 == 0
    StrCpy $R0 1
  ${EndIf}

  SetOutPath "$INSTDIR"
  File "${APP_EXE}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; Start Menu shortcuts (current user only)
  SetShellVarContext current
  CreateDirectory "$SMPROGRAMS\${PRODUCT}"
  CreateShortcut "$SMPROGRAMS\${PRODUCT}\${PRODUCT}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0
  CreateShortcut "$SMPROGRAMS\${PRODUCT}\Uninstall ${PRODUCT}.lnk" "$INSTDIR\uninstall.exe"
  CreateShortcut "$DESKTOP\${PRODUCT}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0

  ; Registry: remember install dir + Add/Remove Programs entry (per-user)
  WriteRegStr HKCU "Software\${PRODUCT}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "CLIAnywhere"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" "$0"

  ; Silent install (/S): no finish page is shown, so restart the app to
  ; restore prior state. Two triggers:
  ;   1. /restart on the command line — the self-update flow. The daemon
  ;      exits itself right after spawning the installer, so the taskkill
  ;      detection below misses it ($R0 stays 0) and an explicit flag is
  ;      the only reliable signal.
  ;   2. $R0 == 1 — a running instance was found (and killed) above; covers
  ;      manual silent installs over a live daemon.
  ; Pass --autostart so the daemon runs quietly without opening a browser
  ; (the user was using it before; a manual launch opens the browser).
  ClearErrors
  ${GetOptions} "$CMDLINE" "/restart" $R1
  ${If} ${Silent}
    ${If} $R0 == 1
    ${OrIfNot} ${Errors}
      Exec '"$INSTDIR\${APP_EXE}" --autostart'
    ${EndIf}
  ${EndIf}
SectionEnd

Section "Uninstall"
  ; Stop a running instance before removing files
  nsExec::ExecToLog 'taskkill /F /T /IM ${APP_EXE}'
  Pop $0

  ; Shortcuts
  SetShellVarContext current
  Delete "$SMPROGRAMS\${PRODUCT}\${PRODUCT}.lnk"
  Delete "$SMPROGRAMS\${PRODUCT}\Uninstall ${PRODUCT}.lnk"
  Delete "$DESKTOP\${PRODUCT}.lnk"
  RMDir "$SMPROGRAMS\${PRODUCT}"

  ; Binaries only — keep user data (daemon.accesskey, security_code, logs) in $INSTDIR
  Delete "$INSTDIR\${APP_EXE}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR" ; only removed if empty (config still there)

  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\${PRODUCT}"
SectionEnd
