@echo off
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"
set "OUT=JukaHub.exe"
set "ARCH=x86_64-w64-mingw32"
set "SDLDIR=%cd%\.sdl2"
set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"

REM ----------------------------------------------------------------------------
REM  Robust MSYS2 / UCRT64 GCC bootstrap for JukaHub builds on Windows.
REM  This block tries, in order:
REM    1. A working gcc.exe already on PATH.
REM    2. An existing C:\msys64 UCRT64 toolchain.
REM    3. Automatic download + silent install of MSYS2 into C:\msys64.
REM    4. Refresh PATH into the current session so the build can continue.
REM  If anything fails after that, the build stops with a clear message.
REM ----------------------------------------------------------------------------

set "MSYS64=C:\msys64"
set "MSYS64_UCRT=%MSYS64%\ucrt64\bin"

REM Minimal startup diagnostics - show key paths without flooding output
echo JukaHub Build Script
if exist "%MSYS64_UCRT%\gcc.exe" (
    echo NOTE: MSYS2 UCRT64 GCC detected at %MSYS64_UCRT%\gcc.exe
)
if defined SDL2_DIR (
    echo NOTE: SDL2_DIR=%SDL2_DIR%
) else if exist "%SDLDIR%\x86_64-w64-mingw32\include\SDL2\SDL.h" (
    echo NOTE: Local SDL2 detected at %SDLDIR%
)
echo.

if /i "%~1"=="diag" goto diag
goto main

REM ----------------------------------------------------------------------------
REM  fail(message) - prints a message and exits with an error level.
REM ----------------------------------------------------------------------------
:fail
setlocal EnableExtensions EnableDelayedExpansion
if "%~1"=="" (
    set "FAIL_MESSAGE=<no message supplied>"
    set "FAIL_STEP=unknown step"
) else (
    set "FAIL_MESSAGE=%~1"
    set "FAIL_STEP=%~1"
)
echo =========================================================
echo  BUILD FAILED
if not "!FAIL_MESSAGE!"=="<no message supplied>" (
    echo  !FAIL_MESSAGE!
) else (
    echo  !FAIL_MESSAGE!
    echo.
    echo No step-specific message was supplied.
    echo Check the output immediately above this block for the last
    echo step that ran, plus the host state shown at startup.
    echo.
    echo Quick checks:
    echo   1. Do you have internet access right now?
    echo   2. Is a previous MSYS2 / Git bash window holding files open?
    echo   3. Does the current user have write access to %TEMP% and %MSYS64%?
)
echo =========================================================
echo.
endlocal
call :cleanup_temp
exit /b 1

REM ----------------------------------------------------------------------------
REM  cleanup_temp - remove temporary files on failure
REM ----------------------------------------------------------------------------
:cleanup_temp
REM Clean up temporary downloads and extraction files
if defined MSYS_EXE if exist "%MSYS_EXE%" (
    echo Removing MSYS2 installer download: %MSYS_EXE%
    del /q "%MSYS_EXE%" >nul 2>&1 || rem noop
)
if defined SDL2_TGZ if exist "%SDL2_TGZ%" (
    echo Removing SDL2 download: %SDL2_TGZ%
    del /q "%SDL2_TGZ%" >nul 2>&1 || rem noop
)
if defined IMG_TGZ if exist "%IMG_TGZ%" (
    echo Removing SDL2_image download: %IMG_TGZ%
    del /q "%IMG_TGZ%" >nul 2>&1 || rem noop
)
if defined TTF_TGZ if exist "%TTF_TGZ%" (
    echo Removing SDL2_ttf download: %TTF_TGZ%
    del /q "%TTF_TGZ%" >nul 2>&1 || rem noop
)
if exist "%TEMP%\jukasdl2" (
    echo Removing partial SDL2 extraction folder: %TEMP%\jukasdl2
    rd /s /q "%TEMP%\jukasdl2" >nul 2>&1 || rem noop
)
exit /b

REM ----------------------------------------------------------------------------
REM  Go bootstrap. Prefer any usable Go >= 1.25.0 already installed; otherwise
REM  verify and unpack the official Go archive under the current user's profile.
REM ----------------------------------------------------------------------------
:ensure_go
set "GO_TOOL="
set "GO_VERSION="
set "GOTOOLCHAIN=local"

for /f "delims=" %%G in ('where go 2^>nul') do if /i "%%~xG"==".exe" if not defined GO_TOOL call :probe_go "%%G"
if not defined GO_TOOL if exist "%LOCALAPPDATA%\Programs\Go\go1.25.0\bin\go.exe" call :probe_go "%LOCALAPPDATA%\Programs\Go\go1.25.0\bin\go.exe"
for /d %%D in ("%LOCALAPPDATA%\Programs\Go\go1.25.0-*") do if not defined GO_TOOL if exist "%%~fD\bin\go.exe" call :probe_go "%%~fD\bin\go.exe"
if not defined GO_TOOL if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" call :probe_go "%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GO_TOOL if exist "%ProgramFiles%\Go\bin\go.exe" call :probe_go "%ProgramFiles%\Go\bin\go.exe"
if not defined GO_TOOL if exist "%ProgramFiles(x86)%\Go\bin\go.exe" call :probe_go "%ProgramFiles(x86)%\Go\bin\go.exe"

if not defined GO_TOOL (
    echo No usable Go 1.25.0+ toolchain found; installing Go 1.25.0 for this user...
    call :install_go || exit /b 1
)
if not defined GO_TOOL exit /b 1

goto go_ready

:probe_go
set "GO_CANDIDATE=%~1"

set "GO_VERSION="
set "GO_HOST_ARCH="
set "GO_HOST_OS="
set "GO_MAJOR="
set "GO_MINOR="
set "GO_PATCH="
if not exist "%GO_CANDIDATE%" exit /b 1
"%GO_CANDIDATE%" version >nul 2>&1 || exit /b 1
for /f "delims=" %%V in ('"%GO_CANDIDATE%" env GOVERSION 2^>nul') do set "GO_VERSION=%%V"
for /f "delims=" %%A in ('"%GO_CANDIDATE%" env GOHOSTARCH 2^>nul') do set "GO_HOST_ARCH=%%A"
for /f "delims=" %%O in ('"%GO_CANDIDATE%" env GOHOSTOS 2^>nul') do set "GO_HOST_OS=%%O"
if not defined GO_VERSION exit /b 1
if /i not "!GO_HOST_ARCH!"=="amd64" exit /b 1
if /i not "!GO_HOST_OS!"=="windows" exit /b 1
for /f "tokens=1-3 delims=." %%A in ("!GO_VERSION:go=!") do (
    set "GO_MAJOR=%%A"
    set "GO_MINOR=%%B"
    set "GO_PATCH=%%C"
)
if not defined GO_MAJOR exit /b 1
if not defined GO_MINOR exit /b 1
if not defined GO_PATCH exit /b 1
if !GO_MAJOR! LSS 1 exit /b 1
if !GO_MAJOR! EQU 1 if !GO_MINOR! LSS 25 exit /b 1
set "GO_MAJOR="
set "GO_MINOR="
set "GO_PATCH="
set "GO_TOOL=%GO_CANDIDATE%"
exit /b 0

:install_go
if not defined LOCALAPPDATA (
    echo ERROR: LOCALAPPDATA is not set; cannot choose a safe per-user Go install directory.
    exit /b 1
)
if not exist "%PS%" (
    echo ERROR: Windows PowerShell was not found at %PS%.
    exit /b 1
)
if not exist "%LOCALAPPDATA%\Programs\Go" (
    mkdir "%LOCALAPPDATA%\Programs\Go" >nul 2>&1 || (
        echo ERROR: Cannot create the per-user Go install directory under %LOCALAPPDATA%.
        exit /b 1
    )
)

echo Downloading official Go 1.25.0 Windows amd64 archive...
set "GO_INSTALL="
for /f "tokens=1,* delims==" %%G in ('%PS% -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; try { $base=Join-Path $env:LOCALAPPDATA 'Programs\Go'; New-Item -ItemType Directory -Path $base -Force | Out-Null; $nonce=[guid]::NewGuid().ToString('N'); $zip=Join-Path $env:TEMP ('jukago-'+$nonce+'.windows-amd64.zip'); $stage=Join-Path $base ('.stage-'+$nonce); $install=Join-Path $base ('go1.25.0-'+$nonce); Invoke-WebRequest -Uri 'https://go.dev/dl/go1.25.0.windows-amd64.zip' -OutFile $zip -UseBasicParsing -TimeoutSec 300; $item=Get-Item -LiteralPath $zip; if ($item.Length -ne 67418204) { throw 'Go archive size does not match official release metadata.' }; $hash=(Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant(); if ($hash -ne '89efb4f9b30812eee083cc1770fdd2913c14d301064f6454851428f9707d190b') { throw 'Go archive SHA256 does not match the official release checksum.' }; Expand-Archive -LiteralPath $zip -DestinationPath $stage -Force; Move-Item -LiteralPath (Join-Path $stage 'go') -Destination $install; if (-not (Test-Path -LiteralPath (Join-Path $install 'bin\go.exe'))) { throw 'Go executable is missing after installation.' }; Remove-Item -LiteralPath $zip -Force -ErrorAction SilentlyContinue; Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue; Write-Output ('GO_INSTALL='+$install) } catch { if ($zip -and (Test-Path -LiteralPath $zip)) { Remove-Item -LiteralPath $zip -Force -ErrorAction SilentlyContinue }; if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }; if ($install -and (Test-Path -LiteralPath $install)) { Remove-Item -LiteralPath $install -Recurse -Force -ErrorAction SilentlyContinue }; [Console]::Error.WriteLine($_.Exception.Message); exit 1 }" 2^>^&1') do if /i "%%G"=="GO_INSTALL" set "GO_INSTALL=%%H"
if not defined GO_INSTALL (
    echo ERROR: Go download, checksum verification, or extraction failed.
    exit /b 1
)
if not exist "%GO_INSTALL%\bin\go.exe" (
    echo ERROR: The verified Go archive was not installed under %GO_INSTALL%.
    exit /b 1
)
set "GO_TOOL=%GO_INSTALL%\bin\go.exe"
call :probe_go "%GO_TOOL%" || (
    echo ERROR: The newly installed Go toolchain did not pass its version check.
    exit /b 1
)
echo Installed Go %GO_VERSION% at %GO_INSTALL%.
exit /b 0

:go_ready
for %%I in ("%GO_TOOL%") do set "GO_BIN=%%~dpI"
set "PATH=%GO_BIN%;%PATH%"
echo Using Go %GO_VERSION% from %GO_TOOL%.
"%GO_TOOL%" version || exit /b 1
exit /b 0

REM ----------------------------------------------------------------------------
REM  MAIN
REM ----------------------------------------------------------------------------
:main

call :ensure_go || (call :fail "Go 1.25.0 or newer is required, and Go setup failed." & exit /b 1)

echo =========================================================
echo  Checking for a working C compiler (GCC)
echo =========================================================
echo.

REM  Step 1: is gcc already usable from the current PATH?
echo Checking for GCC...

REM Try running gcc --version directly so the hot path does not depend on any
REM external search tool (where/findstr) and is fast even on slow shells.
where /q gcc 2>nul && (
    for /f "delims=" %%G in ('where gcc 2^>nul') do (
        if not defined GCC_PATH_ON_PATH set "GCC_PATH_ON_PATH=%%G"
    )
    gcc --version >nul 2>&1 && (
        echo Found GCC already on PATH: !GCC_PATH_ON_PATH!
        goto sdl_check
    ) || (
        echo WARNING: a command named gcc is on PATH (!GCC_PATH_ON_PATH!) but it did not respond to --version.
    )
)
REM If 'where' did not find one, check common install paths directly.
if exist "%ProgramFiles%\Git\usr\bin\gcc.exe" (
    echo Found GCC at %ProgramFiles%\Git\usr\bin\gcc.exe
    set "PATH=%ProgramFiles%\Git\usr\bin;%PATH%"
    goto sdl_check
)
if exist "%ProgramFiles(x86)%\Git\usr\bin\gcc.exe" (
    echo Found GCC at %ProgramFiles(x86)%\Git\usr\bin\gcc.exe
    set "PATH=%ProgramFiles(x86)%\Git\usr\bin;%PATH%"
    goto sdl_check
)
if exist "C:\mingw64\bin\gcc.exe" (
    echo Found GCC at C:\mingw64\bin\gcc.exe
    set "PATH=C:\mingw64\bin;%PATH%"
    goto sdl_check
)
if exist "C:\mingw32\bin\gcc.exe" (
    echo Found GCC at C:\mingw32\bin\gcc.exe
    set "PATH=C:\mingw32\bin;%PATH%"
    goto sdl_check
)

REM  Step 2: if MSYS2 is partially present, try to repair before reinstalling.
REM Be defensive: check for signs of an incomplete/corrupt installation.
if exist "%MSYS64%\usr\bin\bash.exe" (
    echo MSYS2 base exists; checking installation state...
    if not exist "%MSYS64%\etc\pacman.conf" (
        echo WARNING: MSYS2 present but pacman.conf missing; may be incomplete install.
        echo Will attempt repair, but may need full reinstall.
    )
    if not exist "%MSYS64%\var\lib\pacman\local" (
        echo WARNING: MSYS2 package database missing; installation may be corrupt.
    )
    echo Attempting repair via pacman...
    "%MSYS64%\usr\bin\bash" -lc "pacman -Sy --noconfirm && pacman -S --noconfirm --overwrite '*' mingw-w64-ucrt-x86_64-gcc base-devel" || (
        echo WARNING: MSYS2 repair via pacman failed; will attempt full reinstall below.
    )
    if exist "%MSYS64_UCRT%\gcc.exe" (
        set "PATH=%MSYS64_UCRT%;%PATH%"
        echo GCC is now usable from MSYS2 repair.
        goto sdl_check
    )
    echo Repair did not produce a working GCC; will proceed to full reinstall.
)

REM  Step 3: if an existing C:\msys64 UCRT64 toolchain is usable, use it directly.
if exist "%MSYS64_UCRT%\gcc.exe" (
    echo Found existing MSYS2 UCRT64 GCC at %MSYS64_UCRT%.
    set "PATH=%MSYS64_UCRT%;%PATH%"
    echo GCC is now usable from MSYS2.
    goto sdl_check
)

REM  Step 4: full automatic MSYS2 install with resume-aware retry logic.
set "MSYS_URL=https://github.com/msys2/msys2-installer/releases/latest/download/msys2-x86_64-latest.exe"
set "MSYS_EXE=%TEMP%\msys2-installer.exe"
set "DOWNLOAD_RETRIES=0"
set "MAX_DOWNLOAD_RETRIES=3"

REM Helper used by the MSYS2 install path to avoid surprising mass deletions.
:verify_and_remove_msys64_target
set "TARGET_TO_REMOVE=%MSYS64%"
if not defined TARGET_TO_REMOVE (
    call :fail "Internal error: MSYS64 install target is not defined."
    exit /b 1
)
if not exist "%TARGET_TO_REMOVE%" (
    echo Target %TARGET_TO_REMOVE% does not exist; nothing to remove.
    exit /b 0
)
if "%TARGET_TO_REMOVE%"=="" (
    call :fail "Internal error: MSYS64 install target is an empty path."
    exit /b 1
)
if "%TARGET_TO_REMOVE%"=="C:\" (
    call :fail "Refusing to remove C:\" because that would be catastrophic."
    exit /b 1
)
if "%TARGET_TO_REMOVE%"=="%SYSTEMDRIVE%\" (
    call :fail "Refusing to remove %SYSTEMDRIVE%\" because that would be catastrophic."
    exit /b 1
)
for /f "delims=" %%I in ("%TARGET_TO_REMOVE%\..") do set "TARGET_PARENT=%%I"
if /i "%TARGET_PARENT%"=="C:" (
    call :fail "Refusing to remove a top-level drive root target: %TARGET_TO_REMOVE%"
    exit /b 1
)
if /i "%TARGET_PARENT%"=="" (
    call :fail "Refusing to remove a target whose parent could not be resolved: %TARGET_TO_REMOVE%"
    exit /b 1
)
if /i not "%TARGET_TO_REMOVE:~-1%"=="\" (
    set "TARGET_TO_REMOVE=%TARGET_TO_REMOVE%\"
)
set "TARGET_SHOW=%TARGET_TO_REMOVE%"
if /i "%TARGET_SHOW:~-1%"=="\" set "TARGET_SHOW=%TARGET_SHOW:~0,-1%"
set "TARGET_ITEM_COUNT=0"
for /f %%C in ('dir /b /s "%TARGET_TO_REMOVE%" 2^>nul ^| find /c /v ""') do set "TARGET_ITEM_COUNT=%%C"
if %TARGET_ITEM_COUNT% LEQ 0 (
    call :fail "MSYS64 install target exists but appears empty/unreadable: %TARGET_SHOW%"
    exit /b 1
)
set "TOP_LEVEL_COUNT=0"
for /f %%I in ('dir /b /a:d "%TARGET_TO_REMOVE%" 2^>nul ^| find /c /v ""') do set "TOP_LEVEL_COUNT=%%I"
if !TOP_LEVEL_COUNT! GTR 20 (
    echo WARNING: target %TARGET_SHOW% appears large (~!TOP_LEVEL_COUNT! top-level entries).
)
echo About to remove existing MSYS64 tree: %TARGET_SHOW%
echo This tree contains approximately !TOP_LEVEL_COUNT! top-level entries.
rmdir /s /q "%TARGET_TO_REMOVE%" || (call :fail "Cannot remove existing MSYS64 tree at %TARGET_SHOW%. Close any MSYS2/bash windows using it and retry." & exit /b 1)
if exist "%TARGET_TO_REMOVE%" (
    call :fail "MSYS64 tree still exists after removal attempt: %TARGET_SHOW%"
    exit /b 1
)
exit /b 0


:msys_download_retry
if %DOWNLOAD_RETRIES% GEQ %MAX_DOWNLOAD_RETRIES% (
    call :fail "Failed to download MSYS2 installer after %MAX_DOWNLOAD_RETRIES% attempts. Check internet access or proxy settings."
    exit /b 1
)

set /a DOWNLOAD_RETRIES+=1
echo Downloading MSYS2 installer (attempt %DOWNLOAD_RETRIES% of %MAX_DOWNLOAD_RETRIES%)...

REM Prefer resume when a partial file already exists so we do not waste the full
REM download on intermittent failures. If resume silently writes a tiny/corrupt
REM file, fall back to a clean restart.
if exist "%MSYS_EXE%" (
    for %%A in ("%MSYS_EXE%") do set "MSYS_EXISTING_SIZE=%%~zA"
    if !MSYS_EXISTING_SIZE! GTR 1048576 (
        echo Partial download exists (!MSYS_EXISTING_SIZE! bytes); attempting resume...
        "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:MSYS_URL -OutFile $env:MSYS_EXE -UseBasicParsing -TimeoutSec 120 -Resume" || (
            echo Resume failed; restarting download from scratch...
            del /q "%MSYS_EXE%" >nul 2>&1 || rem noop
            set "MSYS_EXISTING_SIZE="
            "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:MSYS_URL -OutFile $env:MSYS_EXE -UseBasicParsing -TimeoutSec 120" || (
                echo Download failed; retrying in 15 seconds...
                timeout /t 15 /nobreak >nul 2>&1 || rem noop
                goto msys_download_retry
            )
        )
    ) else (
        echo Partial download too small (!MSYS_EXISTING_SIZE! bytes); restarting download from scratch...
        del /q "%MSYS_EXE%" >nul 2>&1 || rem noop
        set "MSYS_EXISTING_SIZE="
        "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:MSYS_URL -OutFile $env:MSYS_EXE -UseBasicParsing -TimeoutSec 120" || (
            echo Download failed; retrying in 15 seconds...
            timeout /t 15 /nobreak >nul 2>&1 || rem noop
            goto msys_download_retry
        )
    )
) else (
    "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:MSYS_URL -OutFile $env:MSYS_EXE -UseBasicParsing -TimeoutSec 120" || (
        echo Download failed; retrying in 15 seconds...
        timeout /t 15 /nobreak >nul 2>&1 || rem noop
        goto msys_download_retry
    )
)

if not exist "%MSYS_EXE%" (
    call :fail "MSYS2 installer download produced no file after attempt %DOWNLOAD_RETRIES%."
    exit /b 1
)

REM Validate download size before trusting the installer.
for %%A in ("%MSYS_EXE%") do set "MSYS_EXE_SIZE=%%~zA"
if %MSYS_EXE_SIZE% LSS 10485760 (
    echo WARNING: downloaded installer is only %MSYS_EXE_SIZE% bytes (expected >10MB).
    echo Deleting and retrying...
    del /q "%MSYS_EXE%" >nul 2>&1 || rem noop
    set "MSYS_EXE_SIZE="
    set /a DOWNLOAD_RETRIES+=1
    if %DOWNLOAD_RETRIES% GTR %MAX_DOWNLOAD_RETRIES% (
        call :fail "MSYS2 installer download is too small after %MAX_DOWNLOAD_RETRIES% attempts; aborting."
        exit /b 1
    )
    goto msys_download_retry
)
echo Downloaded MSYS2 installer: %MSYS_EXE_SIZE% bytes
set "MSYS_EXE_SIZE="
set "MSYS_EXISTING_SIZE="

call :verify_and_remove_msys64_target || exit /b 1

echo Running MSYS2 installer...
REM The current msys2-installer.exe accepts Windows-style silent switches:
REM   /install /quiet /norestart /root="path"
REM Older or alternate builds may accept Unix-style switches instead:
REM   -install -quiet -norestart -root="path"
REM We try the Windows form first, then the Unix form, then report both clearly.
set "MSYS_INSTALL_OK=0"
"%MSYS_EXE%" /install /quiet /norestart /root="%MSYS64%" >nul 2>&1 && set "MSYS_INSTALL_OK=1"
if "%MSYS_INSTALL_OK%"=="0" (
    "%MSYS_EXE%" -install -quiet -norestart -root="%MSYS64%" >nul 2>&1 && set "MSYS_INSTALL_OK=1"
)
if "%MSYS_INSTALL_OK%"=="0" (
    echo ERROR: MSYS2 installer did not accept either silent-switch form.
    echo Tried: /install /quiet /norestart /root="%MSYS64%"
    echo and: -install -quiet -norestart -root="%MSYS64%"
    echo.
    echo Downloaded installer: %MSYS_EXE%.
    echo You can run it manually, then restart this script.
    call :fail "MSYS2 installation failed. Try running the downloaded installer manually, or install MSYS2 separately."
    exit /b 1
)
if not exist "%MSYS64%\usr\bin\bash.exe" (
    call :fail "MSYS2 install reported success but bash.exe is missing at %MSYS64%\usr\bin\bash.exe."
    exit /b 1
)

REM  Step 5: update packages and install the exact toolchain we need.
set "MSYS_BASH=%MSYS64%\usr\bin\bash.exe"
set "PACMAN_SYNC=%MSYS_BASH% -lc \"pacman -Sy --noconfirm\""
set "PACMAN_INSTALL=%MSYS_BASH% -lc \"pacman -S --noconfirm --overwrite '*' mingw-w64-ucrt-x86_64-gcc\""

REM  Prefer a single combined update+install attempt. If that fails, fall back to
REM  update-then-install separately so a partially updated cache does not block us.
"%PACMAN_SYNC%" && "%PACMAN_INSTALL%" || (
    echo Combined update+install failed; retrying update and install as separate steps.
    "%PACMAN_SYNC%" || (call :fail "MSYS2 package database update failed." & exit /b 1)
    "%PACMAN_INSTALL%" || (call :fail "UCRT64 GCC installation failed." & exit /b 1)
)

REM If a previous run left pacman in a broken half-updated state, the next
REM attempt may need an extra --force flag. Be defensive without forcing on
REM the happy path.
if not exist "%MSYS64_UCRT%\gcc.exe" (
    echo UCRT64 gcc.exe not present after install; retrying once with overwrite forcing...
    "%MSYS_BASH%" -lc "pacman -S --noconfirm --overwrite '*' --force mingw-w64-ucrt-x86_64-gcc" || (call :fail "UCRT64 GCC still missing after forced reinstall attempt." & exit /b 1)
)

REM  Step 6: verify the toolchain is actually usable.
if not exist "%MSYS64_UCRT%\gcc.exe" (
    call :fail "MSYS2 installed but UCRT64 gcc.exe was not found at %MSYS64_UCRT%."
    exit /b 1
)

REM  Refresh PATH for this session.
REM We want the UCRT bin path first so the build uses the toolchain we just
REM installed, and we want the result to be easy to read and easy to verify.
if "%MSYS64_UCRT%"=="" (
    call :fail "Internal error: MSYS64_UCRT is not defined after install."
    exit /b 1
)
if not exist "%MSYS64_UCRT%\gcc.exe" (
    call :fail "Internal error: expected GCC path does not exist: %MSYS64_UCRT%\gcc.exe"
    exit /b 1
)
set "PATH=%MSYS64_UCRT%;%PATH%"
echo Refreshed in-session PATH to prefer %MSYS64_UCRT%.

REM Verify the exact GCC that will be used in this session.
set "GCC_TO_USE=%MSYS64_UCRT%\gcc.exe"
if not exist "%GCC_TO_USE%" (
    call :fail "Expected GCC does not exist: %GCC_TO_USE%"
    exit /b 1
)
set "GCC_OK=0"
for /l %%i in (1,1,3) do (
    "%GCC_TO_USE%" --version >nul 2>&1 && (
        set "GCC_OK=1"
        goto gcc_ready
    )
    timeout /t 2 /nobreak >nul 2>&1 || rem noop
)
if "%GCC_OK%"=="0" (
    echo WARNING: gcc.exe did not respond to --version after retries.
    echo Expected executable: %GCC_TO_USE%
    echo Current PATH first entry: %MSYS64_UCRT%
    echo.
    echo Attempting to persist PATH for future shells...
    if not defined CI if not defined GITHUB_ACTIONS (
        setx PATH "%MSYS64_UCRT%;%PATH%" >nul 2>&1 || rem noop
        setx /M PATH "%MSYS64_UCRT%;%PATH%" >nul 2>&1 || rem noop
    )
    call :fail "gcc.exe exists but did not respond to --version after retries."
    exit /b 1
)

gcc_ready:
set "GCC_TO_USE="
set "GCC_OK="

echo.
echo =========================================================
echo  GCC ready - continuing build without restart
echo =========================================================
echo.
goto sdl_check


REM =========================================================================
REM  SDL2 DISCOVERY + AUTO-DOWNLOAD
REM =========================================================================
:sdl_check

set "SDL2_VER=2.30.5"
set "IMG_VER=2.8.2"
set "TTF_VER=2.22.0"
set "SDL2_ROOT="
if defined SDL2_DIR (
    set "SDL2_ROOT=%SDL2_DIR%"
    call :validate_sdl
    if "!SDL_OK!"=="1" (
        echo Using complete SDL2 development/runtime files from %SDL2_DIR%.
        goto sdl_ready
    )
    echo WARNING: SDL2_DIR=%SDL2_DIR% is incomplete; falling back to the managed local SDL packages.
    set "SDL2_DIR="
    set "SDL2_ROOT="
)

set "SDL2_ROOT=%SDLDIR%\%ARCH%"
call :validate_sdl
if "!SDL_OK!"=="1" goto sdl_ready

echo.
echo =========================================================
echo  Downloading SDL2 development libraries
echo =========================================================
echo.

set "SDL2_URL=https://libsdl.org/release/SDL2-devel-%SDL2_VER%-mingw.tar.gz"
set "IMG_URL=https://libsdl.org/projects/SDL_image/release/SDL2_image-devel-%IMG_VER%-mingw.tar.gz"
set "TTF_URL=https://libsdl.org/projects/SDL_ttf/release/SDL2_ttf-devel-%TTF_VER%-mingw.tar.gz"

set "SDL2_TGZ=%TEMP%\jukasdl2.tar.gz"
set "IMG_TGZ=%TEMP%\jukasdl2_image.tar.gz"
set "TTF_TGZ=%TEMP%\jukasdl2_ttf.tar.gz"

call :download_file "%SDL2_URL%" "%SDL2_TGZ%" "SDL2" || (call :fail "SDL2 download failed; cannot continue without SDL2 headers." & exit /b 1)

call :download_file "%IMG_URL%" "%IMG_TGZ%" "SDL2_image" || (call :fail "SDL2_image download failed; cannot continue without SDL2_image headers." & exit /b 1)
call :download_file "%TTF_URL%" "%TTF_TGZ%" "SDL2_ttf" || (call :fail "SDL2_ttf download failed; cannot continue without SDL2_ttf headers." & exit /b 1)
goto sdl_extract


REM ----------------------------------------------------------------------------
REM  download_file(url, output_path, label) - download with retries
REM ----------------------------------------------------------------------------
:download_file
set "DL_URL=%~1"
set "DL_OUT=%~2"
set "DL_LABEL=%~3"

set "DL_RETRIES=0"
set "DL_MAX=3"
set "DL_MIN_SIZE=1048576"

:dl_retry
if %DL_RETRIES% GEQ %DL_MAX% (
    call :fail "Failed to download %DL_LABEL% after %DL_MAX% attempts. Check internet access."
    exit /b 1
)
set /a DL_RETRIES+=1
echo Downloading %DL_LABEL% (attempt %DL_RETRIES% of %DL_MAX%)...
if exist "%DL_OUT%" (
    for %%A in ("%DL_OUT%") do set "DL_EXISTING_SIZE=%%~zA"
    echo Partial download exists (%DL_EXISTING_SIZE% bytes), attempting resume...
    "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:DL_URL -OutFile $env:DL_OUT -UseBasicParsing -TimeoutSec 120 -Resume" || (
        echo Resume failed, restarting download...
        del /q "%DL_OUT%" >nul 2>&1
        set "DL_EXISTING_SIZE="
        "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:DL_URL -OutFile $env:DL_OUT -UseBasicParsing -TimeoutSec 120" || (
            echo %DL_LABEL% download failed, retrying in 15 seconds...
            timeout /t 15 /nobreak >nul 2>&1
            goto dl_retry
        )
    )
) else (
    "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri $env:DL_URL -OutFile $env:DL_OUT -UseBasicParsing -TimeoutSec 120" || (
        echo %DL_LABEL% download failed, retrying in 15 seconds...
        timeout /t 15 /nobreak >nul 2>&1
        goto dl_retry
    )
)
if not exist "%DL_OUT%" (
    echo %DL_LABEL% download produced no file, retrying...
    goto dl_retry
)
REM Validate download size
for %%A in ("%DL_OUT%") do set "DL_SIZE=%%~zA"
if %DL_SIZE% LSS %DL_MIN_SIZE% (
    echo WARNING: %DL_LABEL% download is only %DL_SIZE% bytes (expected >%DL_MIN_SIZE%). Deleting and retrying...
    del /q "%DL_OUT%" >nul 2>&1
    set "DL_SIZE="
    set "DL_EXISTING_SIZE="
    goto dl_retry
)
echo %DL_LABEL% downloaded successfully: %DL_SIZE% bytes
set "DL_SIZE="
set "DL_EXISTING_SIZE="
exit /b 0

:sdl_extract
echo Extracting SDL2 packages...
if exist "%TEMP%\jukasdl2" (
    echo Removing previous jukasdl2 extraction folder for a clean extraction.
    rmdir /s /q "%TEMP%\jukasdl2" >nul 2>&1
    if exist "%TEMP%\jukasdl2" (
        call :fail "Cannot remove previous SDL2 extraction folder at %TEMP%\jukasdl2."
        exit /b 1
    )
)
mkdir "%TEMP%\jukasdl2" >nul 2>&1 || (call :fail "Cannot create temp SDL2 extraction folder" & exit /b 1)

for %%F in ("%SDL2_TGZ%" "%IMG_TGZ%" "%TTF_TGZ%") do (
    if not exist "%%~F" (
        call :fail "SDL2 archive %%~F not found; download may have failed."
        exit /b 1
    )
    tar -xzf "%%~F" -C "%TEMP%\jukasdl2" || (call :fail "Extraction failed for %%~F" & exit /b 1)
)

REM Overlay the downloaded archives into the managed directory instead of deleting
REM an existing local tree; completeness is checked again after the copy.
mkdir "%SDLDIR%\%ARCH%\include" >nul 2>&1 || (call :fail "Cannot create SDL2 include dir" & exit /b 1)
mkdir "%SDLDIR%\%ARCH%\lib" >nul 2>&1 || (call :fail "Cannot create SDL2 lib dir" & exit /b 1)
mkdir "%SDLDIR%\%ARCH%\bin" >nul 2>&1 || (call :fail "Cannot create SDL2 bin dir" & exit /b 1)

for %%P in (SDL2-%SDL2_VER% SDL2_image-%IMG_VER% SDL2_ttf-%TTF_VER%) do (
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\include\*" "%SDLDIR%\%ARCH%\include\" >nul || (call :fail "SDL header copy failed." & exit /b 1)
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\lib\*" "%SDLDIR%\%ARCH%\lib\" >nul || (call :fail "SDL library copy failed." & exit /b 1)
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\bin\*" "%SDLDIR%\%ARCH%\bin\" >nul || (call :fail "SDL runtime copy failed." & exit /b 1)
)
set "SDL2_ROOT=%SDLDIR%\%ARCH%"
call :validate_sdl
if not "!SDL_OK!"=="1" (
    call :fail "SDL2, SDL_image, or SDL_ttf remains incomplete after download and extraction."
    exit /b 1
)

goto sdl_ready

:validate_sdl
set "SDL_OK=1"
for %%H in (SDL.h SDL_image.h SDL_ttf.h) do if not exist "%SDL2_ROOT%\include\SDL2\%%H" if not exist "%SDL2_ROOT%\include\%%H" (
    echo Missing SDL header: %%H under %SDL2_ROOT%\include
    set "SDL_OK=0"
)
for %%L in (SDL2 SDL2_image SDL2_ttf) do (
    set "SDL_LIB_OK=0"
    for %%F in (lib%%L.a lib%%L.dll.a) do (
        if exist "%SDL2_ROOT%\lib\%%F" set "SDL_LIB_OK=1"
        if exist "%SDL2_ROOT%\lib64\%%F" set "SDL_LIB_OK=1"
    )
    if "!SDL_LIB_OK!"=="0" (
        echo Missing %%L link library under %SDL2_ROOT%\lib or lib64
        set "SDL_OK=0"
    )
)
if not exist "%SDL2_ROOT%\lib\libSDL2main.a" if not exist "%SDL2_ROOT%\lib64\libSDL2main.a" (
    echo Missing SDL2 main library under %SDL2_ROOT%\lib or lib64
    set "SDL_OK=0"
)
for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll) do if not exist "%SDL2_ROOT%\bin\%%D" if not exist "%SDL2_ROOT%\%%D" if not exist "%~dp0%%D" (
    echo Missing SDL runtime file: %%D
    set "SDL_OK=0"
)
exit /b 0

:sdl_ready
set "SDL_LIB_FLAGS=-L%SDL2_ROOT%\lib"
if exist "%SDL2_ROOT%\lib64" set "SDL_LIB_FLAGS=-L%SDL2_ROOT%\lib -L%SDL2_ROOT%\lib64"
set "SDL_RUNTIME_DIR=%SDL2_ROOT%\bin"
set "CGO_CFLAGS=-I%SDL2_ROOT%\include %CGO_CFLAGS%"
set "CGO_LDFLAGS=%SDL_LIB_FLAGS% %CGO_LDFLAGS%"
echo SDL2, SDL_image, and SDL_ttf headers, libraries, and runtime files are ready.


REM =========================================================================
REM  BUILD
REM =========================================================================
:build
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

REM SDL environment flags were set from the validated SDK above.

if /i "%1"=="nobuild" goto after_build

echo.
echo =========================================================
echo  Building %OUT%
echo =========================================================
echo.

echo Running: "%GO_TOOL%" build -o "%OUT%" .
"%GO_TOOL%" build -o "%OUT%" .
if errorlevel 1 (
    echo.
    echo NOTE: go build did not return success.
    echo The build output above should contain the first error.
    echo.
    if exist "%OUT%" (
        echo NOTE: %OUT% exists despite the non-zero exit; it may be incomplete or stale.
    ) else (
        echo NOTE: %OUT% was not created.
    )
    call :fail "go build failed while compiling JukaHub.exe; see the compiler output above for the first error."
    exit /b 1
)


REM =========================================================================
REM  POST-BUILD
REM =========================================================================
:after_build

if not exist "%OUT%" (
    call :fail "Expected %OUT% was not created after build; the build may have printed errors above."
    exit /b 1
)

REM Stage required SDL DLLs beside the executable.
for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll) do (
    if not exist "%~dp0%%D" if exist "%SDL_RUNTIME_DIR%\%%D" copy /y "%SDL_RUNTIME_DIR%\%%D" "%~dp0%%D" >nul
    if not exist "%~dp0%%D" if exist "%SDL2_ROOT%\%%D" copy /y "%SDL2_ROOT%\%%D" "%~dp0%%D" >nul
    if not exist "%~dp0%%D" (
        call :fail "Required SDL runtime DLL %%D is missing next to %OUT%."
        exit /b 1
    )
)

echo.
echo Build succeeded: %OUT%
echo.
call :cleanup_temp

if defined GITHUB_ACTIONS goto :eof
if defined CI goto :eof

if /i "%1"=="nodebug" goto :launch_without_diag

call :state_dump 0

:launch_without_diag

if "%1"=="nodebug" (
    echo Diagnostic output above was printed because 'nodebug' mode was requested.
    echo Launching %OUT%...
)

echo Launching %OUT%...
start "" "%OUT%"
exit /b 0

REM =========================================================================
REM  DIAGNOSTIC MODE
REM =========================================================================
REM  Usage: build.bat diag
REM  Prints the build-relevant host state without performing a build.
REM  This is intended for debugging, not for normal builds.
REM =========================================================================
:diag

setlocal EnableExtensions EnableDelayedExpansion

echo =========================================================
echo  Build diagnostics (no build performed)
echo =========================================================
echo.
call :state_dump 1
endlocal
goto :eof

REM ----------------------------------------------------------------------------
REM  state_dump - print concise success-state or full diagnostic details.
REM   0 : concise build-state summary; 1 : full diagnostic snapshot.
REM ----------------------------------------------------------------------------
:state_dump
setlocal EnableExtensions EnableDelayedExpansion
set "PBS_VERBOSE=%~1"

call :diag_line "script directory" "%~dp0"
call :diag_line "arch" "%ARCH%"
call :diag_line "OUT" "%OUT%"
call :diag_line "SDLDIR" "%SDLDIR%"
call :diag_line "SDL2_DIR (env)" "%SDL2_DIR%"
call :diag_line "PATH first entry" "%PATH:~0,200%"

set "HEADER=%SDLDIR%\%ARCH%\include\SDL2\SDL.h"
if exist "%MSYS64_UCRT%\gcc.exe" (
    call :diag_line "using GCC" "%MSYS64_UCRT%\gcc.exe"
) else (
    call :diag_line "using GCC" "not found at %MSYS64_UCRT%\gcc.exe"
)
call :diag_line "SDL2 header" "%HEADER%"

for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll) do if exist "%~dp0%%D" (
    call :diag_line "%%D next to script" "present"
) else (
    call :diag_line "%%D next to script" "missing"
)

if "%PBS_VERBOSE%"=="1" (

    if defined GITHUB_ACTIONS (
        call :diag_line "env GITHUB_ACTIONS" "defined"
    ) else (
        call :diag_line "env GITHUB_ACTIONS" "not set"
    )
    if defined CI (
        call :diag_line "env CI" "defined"
    ) else (
        call :diag_line "env CI" "not set"
    )

    echo.
echo ---- GCC state ----

    where gcc >nul 2>&1 && (
        call :diag_line "gcc on PATH" "yes (where succeeded)"
    ) || (
        call :diag_line "gcc on PATH" "no (where failed)"
    )

    set "GCC_TEST=%ProgramFiles%\Git\usr\bin\gcc.exe"
    if exist "%GCC_TEST%" (
        call :diag_line "Git-bash GCC" "%GCC_TEST%"
    ) else (
        call :diag_line "Git-bash GCC" "not found"
    )

    set "GCC_TEST=%ProgramFiles(x86)%\Git\usr\bin\gcc.exe"
    if exist "%GCC_TEST%" (
        call :diag_line "Git-bash GCC (x86)" "%GCC_TEST%"
    ) else (
        call :diag_line "Git-bash GCC (x86)" "not found"
    )

    set "GCC_TEST=C:\mingw64\bin\gcc.exe"
    if exist "%GCC_TEST%" (
        call :diag_line "mingw64 GCC" "%GCC_TEST%"
    ) else (
        call :diag_line "mingw64 GCC" "not found"
    )

    set "GCC_TEST=C:\mingw32\bin\gcc.exe"
    if exist "%GCC_TEST%" (
        call :diag_line "mingw32 GCC" "%GCC_TEST%"
    ) else (
        call :diag_line "mingw32 GCC" "not found"
    )

    set "GCC_TEST=%MSYS64_UCRT%\gcc.exe"
    if exist "%GCC_TEST%" (
        call :diag_line "MSYS2 UCRT64 GCC" "%GCC_TEST%"
        if not defined CI if not defined GITHUB_ACTIONS (
            "%GCC_TEST%" --version >nul 2>&1 && (
                call :diag_line "MSYS2 UCRT64 GCC --version" "responded"
            ) || (
                call :diag_line "MSYS2 UCRT64 GCC --version" "did not respond"
            )
        )
    ) else (
        call :diag_line "MSYS2 UCRT64 GCC" "not found"
    )

    if exist "%MSYS64%\usr\bin\bash.exe" (
        call :diag_line "MSYS2 bash.exe" "%MSYS64%\usr\bin\bash.exe"
    ) else (
        call :diag_line "MSYS2 bash.exe" "not found"
    )

    if exist "%MSYS64%\etc\pacman.conf" (
        call :diag_line "MSYS2 pacman.conf" "present"
    ) else (
        call :diag_line "MSYS2 pacman.conf" "missing"
    )

    if exist "%MSYS64%\var\lib\pacman\local" (
        call :diag_line "MSYS2 pacman db" "present"
    ) else (
        call :diag_line "MSYS2 pacman db" "missing"
    )

    echo.
echo ---- SDL2 state ----

    if defined SDL2_DIR (
        call :diag_line "SDL2_DIR (in use)" "%SDL2_DIR%"
        call :diag_line "SDL2_DIR include" "%SDL2_DIR%\include"
        call :diag_line "SDL2_DIR lib" "%SDL2_DIR%\lib"
    ) else if exist "%HEADER%" (
        call :diag_line "SDL2 local tree" "%SDLDIR%"
        call :diag_line "SDL2 lib dir" "%SDLDIR%\%ARCH%\lib"
        call :diag_line "SDL2 lib *.a" "%SDLDIR%\%ARCH%\lib\*.a"
    ) else (
        call :diag_line "SDL2 (in use)" "not configured / not found"
    )

    if exist "%SDLDIR%\%ARCH%\bin\SDL2.dll" (
        call :diag_line "SDL2.dll (%ARCH%\bin)" "present"
    ) else (
        call :diag_line "SDL2.dll (%ARCH%\bin)" "missing"
    )

    if defined MSYS_EXE (
        if exist "%MSYS_EXE%" (
            for %%A in ("%MSYS_EXE%") do set "MSYS_EXE_SIZE=%%~zA"
            call :diag_line "MSYS_EXE" "%MSYS_EXE% (%MSYS_EXE_SIZE% bytes)"
            set "MSYS_EXE_SIZE="
        ) else (
            call :diag_line "MSYS_EXE" "%MSYS_EXE% (missing)"
        )
    ) else (
        call :diag_line "MSYS_EXE" "not set"
    )

    if defined SDL2_TGZ (
        if exist "%SDL2_TGZ%" (
            for %%A in ("%SDL2_TGZ%") do set "SDL2_TGZ_SIZE=%%~zA"
            call :diag_line "SDL2_TGZ" "%SDL2_TGZ% (%SDL2_TGZ_SIZE% bytes)"
            set "SDL2_TGZ_SIZE="
        ) else (
            call :diag_line "SDL2_TGZ" "%SDL2_TGZ% (missing)"
        )
    ) else (
        call :diag_line "SDL2_TGZ" "not set"
    )

    if defined IMG_TGZ (
        if exist "%IMG_TGZ%" (
            for %%A in ("%IMG_TGZ%") do set "IMG_TGZ_SIZE=%%~zA"
            call :diag_line "IMG_TGZ" "%IMG_TGZ% (%IMG_TGZ_SIZE% bytes)"
            set "IMG_TGZ_SIZE="
        ) else (
            call :diag_line "IMG_TGZ" "%IMG_TGZ% (missing)"
        )
    ) else (
        call :diag_line "IMG_TGZ" "not set"
    )

    if defined TTF_TGZ (
        if exist "%TTF_TGZ%" (
            for %%A in ("%TTF_TGZ%") do set "TTF_TGZ_SIZE=%%~zA"
            call :diag_line "TTF_TGZ" "%TTF_TGZ% (%TTF_TGZ_SIZE% bytes)"
            set "TTF_TGZ_SIZE="
        ) else (
            call :diag_line "TTF_TGZ" "%TTF_TGZ% (missing)"
        )
    ) else (
        call :diag_line "TTF_TGZ" "not set"
    )

    if exist "%TEMP%\jukasdl2" (
        call :diag_line "TEMP\jukasdl2" "present"
    ) else (
        call :diag_line "TEMP\jukasdl2" "missing"
    )
)

endlocal
exit /b 0

REM ----------------------------------------------------------------------------
REM  diag_line - small labeled-value printer used by :state_dump.
REM ----------------------------------------------------------------------------
:diag_line
setlocal EnableExtensions EnableDelayedExpansion
set "DL_KEY=%~1"
set "DL_VAL=%~2"
if not defined DL_VAL (
    set "DL_VAL=<empty>"
)
if "!DL_VAL!"=="" set "DL_VAL=<empty>"
echo [DIAG] !DL_KEY! = !DL_VAL!
endlocal
exit /b 0

