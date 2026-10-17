@echo off
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"
title JukaHub Build

REM ============================================================================
REM  JukaHub build script for Windows (amd64)
REM  Auto-installs: Go (latest stable, SHA256-verified), GCC (w64devkit or
REM  MSYS2 UCRT64 fallback) and SDL2 / SDL2_image / SDL2_ttf dev libraries.
REM
REM  Usage:  build.bat            build + launch
REM          build.bat nolaunch   build only
REM          build.bat nobuild    set up toolchain only
REM          build.bat diag       print diagnostics
REM          build.bat clean      remove build outputs and local SDL copy
REM ============================================================================

REM ---------------- configuration ----------------
set "OUT=JukaHub.exe"
set "ARCH=x86_64-w64-mingw32"
set "GO_MIN_MINOR=25"
set "GO_FALLBACK=go1.25.0"
set "W64DK_VER=2.0.0"
set "SDL2_VER=2.30.5"
set "IMG_VER=2.8.2"
set "TTF_VER=2.22.0"

set "ROOT=%~dp0"
set "TOOLS=%LOCALAPPDATA%\JukaHub\tools"
set "SDLDIR=%ROOT%.sdl2"
set "SDL_ROOT=%SDLDIR%\%ARCH%"
set "MSYS64=C:\msys64"
set "SYS32=%SystemRoot%\System32"
set "PS=%SYS32%\WindowsPowerShell\v1.0\powershell.exe"
set "TAR=%SYS32%\tar.exe"
set "CURL=%SYS32%\curl.exe"
set "DLTMP=%TEMP%\jukahub-dl"
set "USER_GOTOOLCHAIN=%GOTOOLCHAIN%"
set "ERR=Unknown error."
set "MODE=%~1"

if /i "%MODE%"=="diag"  goto diag
if /i "%MODE%"=="clean" goto clean

echo =========================================================
echo  JukaHub build
echo =========================================================
if not exist "%TAR%" (
    set "ERR=%TAR% not found. Windows 10 1803 or newer is required."
    goto failed
)
if not exist "%CURL%" echo [warn] curl.exe not found - falling back to PowerShell downloads (slower).
if not exist "%TOOLS%" mkdir "%TOOLS%" >nul 2>&1
if not exist "%DLTMP%" mkdir "%DLTMP%" >nul 2>&1

call :ensure_go  || goto failed
call :ensure_gcc || goto failed
call :ensure_sdl || goto failed
if /i "%MODE%"=="nobuild" goto done

call :build      || goto failed
call :stage_dlls || goto failed

echo.
echo =========================================================
echo  Build succeeded: %ROOT%%OUT%
echo =========================================================
if defined CI goto done
if defined GITHUB_ACTIONS goto done
if /i "%MODE%"=="nolaunch" goto done
call :launch
goto done

:failed
echo.
echo =========================================================
echo  BUILD FAILED
echo  !ERR!
echo  Run "build.bat diag" for environment details.
echo =========================================================
if exist "%DLTMP%" rd /s /q "%DLTMP%" >nul 2>&1
if not defined CI if not defined GITHUB_ACTIONS pause
exit /b 1

:done
if exist "%DLTMP%" rd /s /q "%DLTMP%" >nul 2>&1
exit /b 0


REM ============================================================================
REM  download <url> <outfile> <label>   - 3 attempts, fast, shows progress
REM ============================================================================
:download
set "DL_URL=%~1"
set "DL_OUT=%~2"
echo Downloading %~3 ...
if not exist "%DLTMP%" mkdir "%DLTMP%" >nul 2>&1
for /l %%i in (1,1,3) do (
    if exist "!DL_OUT!" del /q "!DL_OUT!" >nul 2>&1
    if exist "%CURL%" (
        "%CURL%" -fL --progress-bar --retry 3 --retry-delay 3 --connect-timeout 30 -o "!DL_OUT!" "!DL_URL!"
    ) else (
        "%PS%" -NoProfile -ExecutionPolicy Bypass -Command "$ProgressPreference='SilentlyContinue'; [Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -Uri $env:DL_URL -OutFile $env:DL_OUT -UseBasicParsing"
    )
    if not errorlevel 1 if exist "!DL_OUT!" exit /b 0
    echo   attempt %%i failed, retrying...
    ping -n 4 127.0.0.1 >nul
)
set "ERR=Download failed after 3 attempts: %~1  (check internet / proxy / firewall)"
exit /b 1


REM ============================================================================
REM  GO
REM ============================================================================
:ensure_go
set "GOTOOLCHAIN=local"
set "GO_EXE="
for /f "delims=" %%G in ('where go.exe 2^>nul') do if not defined GO_EXE call :probe_go "%%G"
if not defined GO_EXE if exist "%ProgramFiles%\Go\bin\go.exe" call :probe_go "%ProgramFiles%\Go\bin\go.exe"
if not defined GO_EXE if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" call :probe_go "%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GO_EXE for /d %%D in ("%TOOLS%\go1.*") do if not defined GO_EXE call :probe_go "%%~fD\go\bin\go.exe"
if not defined GO_EXE (
    echo No Go 1.%GO_MIN_MINOR%+ found - installing a private copy to %TOOLS%
    call :install_go || exit /b 1
)
set "GOTOOLCHAIN=%USER_GOTOOLCHAIN%"
for %%I in ("%GO_EXE%") do set "PATH=%%~dpI;%PATH%"
echo [ok] Go  !GO_VERSION!  ^(%GO_EXE%^)
exit /b 0

:probe_go
set "CAND=%~1"
set "V=" & set "PLAT=" & set "MAJ=" & set "MIN="
if not exist "%CAND%" exit /b 1
for /f "tokens=3,4" %%A in ('call "%CAND%" version 2^>nul') do (set "V=%%A" & set "PLAT=%%B")
if not defined V exit /b 1
if /i not "!PLAT!"=="windows/amd64" exit /b 1
for /f "tokens=1,2 delims=." %%A in ("!V:go=!") do (set "MAJ=%%A" & set "MIN=%%B")
if not "!MAJ!"=="1" exit /b 1
if not defined MIN exit /b 1
for /f "delims=0123456789" %%X in ("!MIN!") do exit /b 1
if !MIN! LSS %GO_MIN_MINOR% exit /b 1
set "GO_EXE=%CAND%"
set "GO_VERSION=!V!"
exit /b 0

:install_go
set "GO_VER="
if exist "%CURL%" for /f "delims=" %%V in ('call "%CURL%" -fsSL "https://go.dev/VERSION?m=text" 2^>nul') do if not defined GO_VER set "GO_VER=%%V"
if not defined GO_VER set "GO_VER=%GO_FALLBACK%"
echo %GO_VER%| findstr /r /x "go1\.[0-9][0-9]*\.[0-9][0-9]*" >nul || set "GO_VER=%GO_FALLBACK%"

set "GO_ZIP=%DLTMP%\%GO_VER%.windows-amd64.zip"
call :download "https://go.dev/dl/%GO_VER%.windows-amd64.zip" "%GO_ZIP%" "Go %GO_VER% (about 65-80 MB)" || exit /b 1
call :download "https://dl.google.com/go/%GO_VER%.windows-amd64.zip.sha256" "%GO_ZIP%.sha256" "Go checksum" || exit /b 1
call :verify_sha256 "%GO_ZIP%" "%GO_ZIP%.sha256" || (set "ERR=Go archive SHA256 mismatch - download corrupted or tampered." & exit /b 1)

set "GO_DEST=%TOOLS%\%GO_VER%"
if exist "%GO_DEST%" rd /s /q "%GO_DEST%" >nul 2>&1
mkdir "%GO_DEST%" >nul 2>&1 || (set "ERR=Cannot create %GO_DEST%" & exit /b 1)
echo Extracting Go ...
"%TAR%" -xf "%GO_ZIP%" -C "%GO_DEST%" || (set "ERR=Failed to extract the Go archive." & exit /b 1)
del /q "%GO_ZIP%" >nul 2>&1
call :probe_go "%GO_DEST%\go\bin\go.exe" || (set "ERR=Installed Go failed its self-check." & exit /b 1)
exit /b 0

:verify_sha256
set "VF=%~1"
set "VS=%~2"
"%PS%" -NoProfile -ExecutionPolicy Bypass -Command "$e=((Get-Content -LiteralPath $env:VS -Raw).Trim() -split '\s+')[0]; $a=(Get-FileHash -LiteralPath $env:VF -Algorithm SHA256).Hash; if ($a -ne $e) { Write-Host ('  expected ' + $e + ', got ' + $a); exit 1 }"
exit /b %errorlevel%


REM ============================================================================
REM  GCC (needed by cgo for the SDL2 bindings) - must be 64-bit x86_64
REM ============================================================================
:ensure_gcc
set "GCC_DIR="
for /f "delims=" %%G in ('where gcc.exe 2^>nul') do if not defined GCC_DIR call :probe_gcc "%%~dpG"
for %%D in ("%TOOLS%\w64devkit\bin" "%MSYS64%\ucrt64\bin" "%MSYS64%\mingw64\bin" "C:\mingw64\bin" "C:\TDM-GCC-64\bin") do if not defined GCC_DIR call :probe_gcc "%%~D"
if not defined GCC_DIR (
    echo No 64-bit GCC found - installing w64devkit to %TOOLS%
    call :install_w64devkit
)
if not defined GCC_DIR (
    echo w64devkit unavailable - falling back to MSYS2 UCRT64 ^(slower^)
    call :install_msys2_gcc
)
if not defined GCC_DIR (
    if "!ERR!"=="Unknown error." set "ERR=Could not find or install a 64-bit GCC."
    exit /b 1
)
set "PATH=%GCC_DIR%;%PATH%"
set "CC=gcc"
echo [ok] GCC !GCC_VER!  ^(%GCC_DIR%^)
exit /b 0

:probe_gcc
set "D=%~1"
if "%D:~-1%"=="\" set "D=%D:~0,-1%"
if not exist "%D%\gcc.exe" exit /b 1
set "M="
for /f "delims=" %%M in ('call "%D%\gcc.exe" -dumpmachine 2^>nul') do set "M=%%M"
if not defined M exit /b 1
if "!M:x86_64=!"=="!M!" exit /b 1
set "GCC_VER="
for /f "delims=" %%V in ('call "%D%\gcc.exe" -dumpversion 2^>nul') do set "GCC_VER=%%V"
set "GCC_DIR=%D%"
exit /b 0

:install_w64devkit
set "W64_EXE=%DLTMP%\w64devkit.exe"
call :download "https://github.com/skeeto/w64devkit/releases/download/v%W64DK_VER%/w64devkit-x64-%W64DK_VER%.exe" "%W64_EXE%" "w64devkit %W64DK_VER% (portable GCC)" || exit /b 1
if exist "%TOOLS%\w64devkit" rd /s /q "%TOOLS%\w64devkit" >nul 2>&1
echo Extracting w64devkit ...
"%W64_EXE%" -y -o"%TOOLS%" >nul || (set "ERR=w64devkit extraction failed." & exit /b 1)
del /q "%W64_EXE%" >nul 2>&1
call :probe_gcc "%TOOLS%\w64devkit\bin"
exit /b 0

:install_msys2_gcc
if not exist "%MSYS64%\usr\bin\bash.exe" (
    set "MSYS_SFX=%DLTMP%\msys2-base.sfx.exe"
    call :download "https://github.com/msys2/msys2-installer/releases/download/nightly-x86_64/msys2-base-x86_64-latest.sfx.exe" "!MSYS_SFX!" "MSYS2 base system" || exit /b 1
    echo Extracting MSYS2 to %MSYS64% ...
    "!MSYS_SFX!" -y -oC:\ >nul || (set "ERR=MSYS2 extraction failed." & exit /b 1)
)
if not exist "%MSYS64%\usr\bin\bash.exe" (set "ERR=MSYS2 bash.exe missing after install." & exit /b 1)
set "MSYSTEM=UCRT64"
set "CHERE_INVOKING=1"
echo Initialising MSYS2 ^(first run can take a minute^) ...
"%MSYS64%\usr\bin\bash.exe" -lc "true" >nul 2>&1
echo Installing UCRT64 GCC via pacman ...
"%MSYS64%\usr\bin\bash.exe" -lc "pacman -Sy --needed --noconfirm mingw-w64-ucrt-x86_64-gcc"
set "PAC_RC=%errorlevel%"
set "MSYSTEM="
set "CHERE_INVOKING="
if not "%PAC_RC%"=="0" (set "ERR=pacman failed to install GCC. Close any open MSYS2 windows and retry." & exit /b 1)
call :probe_gcc "%MSYS64%\ucrt64\bin"
exit /b 0


REM ============================================================================
REM  SDL2 + SDL2_image + SDL2_ttf (MinGW dev packages)
REM ============================================================================
:ensure_sdl
if defined SDL2_DIR (
    set "SDL_ROOT=%SDL2_DIR%"
    call :validate_sdl quiet && goto sdl_ok
    echo [warn] SDL2_DIR=%SDL2_DIR% is incomplete - using managed copy in .sdl2
    set "SDL_ROOT=%SDLDIR%\%ARCH%"
)
call :validate_sdl quiet && goto sdl_ok

echo Fetching SDL2 development libraries ...
call :fetch_sdl_pkg "https://github.com/libsdl-org/SDL/releases/download/release-%SDL2_VER%/SDL2-devel-%SDL2_VER%-mingw.tar.gz" "SDL2-%SDL2_VER%" || exit /b 1
call :fetch_sdl_pkg "https://github.com/libsdl-org/SDL_image/releases/download/release-%IMG_VER%/SDL2_image-devel-%IMG_VER%-mingw.tar.gz" "SDL2_image-%IMG_VER%" || exit /b 1
call :fetch_sdl_pkg "https://github.com/libsdl-org/SDL_ttf/releases/download/release-%TTF_VER%/SDL2_ttf-devel-%TTF_VER%-mingw.tar.gz" "SDL2_ttf-%TTF_VER%" || exit /b 1
call :validate_sdl || (set "ERR=SDL2 libraries are still incomplete after download." & exit /b 1)

:sdl_ok
set "CGO_CFLAGS="-I%SDL_ROOT%\include" "-I%SDL_ROOT%\include\SDL2" %CGO_CFLAGS%"
set "CGO_LDFLAGS="-L%SDL_ROOT%\lib" %CGO_LDFLAGS%"
echo [ok] SDL2  ^(%SDL_ROOT%^)
exit /b 0

:fetch_sdl_pkg
set "PKG_TGZ=%DLTMP%\%~2.tar.gz"
call :download "%~1" "%PKG_TGZ%" "%~2" || exit /b 1
if exist "%DLTMP%\x" rd /s /q "%DLTMP%\x" >nul 2>&1
mkdir "%DLTMP%\x" >nul 2>&1
"%TAR%" -xzf "%PKG_TGZ%" -C "%DLTMP%\x" || (set "ERR=Failed to extract %~2." & exit /b 1)
if not exist "%DLTMP%\x\%~2\%ARCH%" (set "ERR=Unexpected folder layout inside %~2 archive." & exit /b 1)
xcopy /e /i /y /q "%DLTMP%\x\%~2\%ARCH%" "%SDL_ROOT%" >nul || (set "ERR=Failed to copy %~2 files." & exit /b 1)
rd /s /q "%DLTMP%\x" >nul 2>&1
del /q "%PKG_TGZ%" >nul 2>&1
exit /b 0

:validate_sdl
set "SDL_OK=1"
for %%F in (include\SDL2\SDL.h include\SDL2\SDL_image.h include\SDL2\SDL_ttf.h lib\libSDL2.dll.a lib\libSDL2main.a lib\libSDL2_image.dll.a lib\libSDL2_ttf.dll.a bin\SDL2.dll bin\SDL2_image.dll bin\SDL2_ttf.dll) do (
    if not exist "%SDL_ROOT%\%%F" (
        if /i not "%~1"=="quiet" echo   missing: %SDL_ROOT%\%%F
        set "SDL_OK=0"
    )
)
if "%SDL_OK%"=="1" exit /b 0
exit /b 1


REM ============================================================================
REM  BUILD / STAGE / LAUNCH
REM ============================================================================
:build
set "CGO_ENABLED=1"
set "GOOS=windows"
set "GOARCH=amd64"
REM Windows Defender heuristically quarantines the Go linker output when it is
REM written under %TEMP%\go-build* (false positive on the self-update code).
REM Keep the Go build cache and temp files under %TOOLS% so the link succeeds.
if not exist "%TOOLS%\gocache" mkdir "%TOOLS%\gocache" >nul 2>&1
if not exist "%TOOLS%\gotmp" mkdir "%TOOLS%\gotmp" >nul 2>&1
set "GOCACHE=%TOOLS%\gocache"
set "TMP=%TOOLS%\gotmp"
set "TEMP=%TOOLS%\gotmp"
set "TMPDIR=%TOOLS%\gotmp"
REM A running JukaHub.exe locks the file and makes the link step fail.
taskkill /im "%OUT%" /f >nul 2>&1
if exist "go.mod" (
    echo Downloading Go modules ...
    "%GO_EXE%" mod download || (set "ERR=go mod download failed - check network / GOPROXY." & exit /b 1)
)
echo.
echo Building %OUT% ...
echo   NOTE: the FIRST build compiles the SDL2 cgo bindings and can take
echo   3-10 minutes. Later builds use the Go cache and take seconds.
echo.
"%GO_EXE%" build -v -ldflags "-s -w" -o "%OUT%" . || (set "ERR=go build failed - see the compiler output above." & exit /b 1)
if not exist "%OUT%" (set "ERR=go build reported success but %OUT% is missing." & exit /b 1)
exit /b 0

:stage_dlls
for %%D in ("%SDL_ROOT%\bin\*.dll") do copy /y "%%~fD" "%ROOT%" >nul || (set "ERR=Could not copy %%~nxD next to %OUT%." & exit /b 1)
REM Some GCC distributions need these runtime DLLs beside cgo binaries.
for %%D in (libwinpthread-1.dll libgcc_s_seh-1.dll) do if exist "%GCC_DIR%\%%D" copy /y "%GCC_DIR%\%%D" "%ROOT%" >nul
for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll) do if not exist "%ROOT%%%D" (set "ERR=%%D missing next to %OUT%." & exit /b 1)
exit /b 0

:launch
echo Launching %OUT% ...
start "" /D "%ROOT%" "%ROOT%%OUT%"
exit /b 0


REM ============================================================================
REM  DIAG / CLEAN
REM ============================================================================
:diag
echo =========================================================
echo  JukaHub build diagnostics
echo =========================================================
echo Script dir  : %ROOT%
echo Tools dir   : %TOOLS%
if exist "%CURL%" (echo curl.exe    : present) else (echo curl.exe    : MISSING)
if exist "%TAR%"  (echo tar.exe     : present) else (echo tar.exe     : MISSING)
echo.
echo --- Go ---
where go.exe 2>nul || echo go on PATH  : none
for /d %%D in ("%TOOLS%\go1.*") do echo managed Go  : %%~fD
echo.
echo --- GCC ---
where gcc.exe 2>nul || echo gcc on PATH : none
if exist "%TOOLS%\w64devkit\bin\gcc.exe" echo w64devkit   : %TOOLS%\w64devkit\bin\gcc.exe
if exist "%MSYS64%\ucrt64\bin\gcc.exe"   echo MSYS2 UCRT64: %MSYS64%\ucrt64\bin\gcc.exe
echo.
echo --- SDL2 ---
if defined SDL2_DIR (echo SDL2_DIR    : %SDL2_DIR%& set "SDL_ROOT=%SDL2_DIR%")
echo SDL root    : %SDL_ROOT%
call :validate_sdl && echo SDL2 files  : complete
echo.
for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll %OUT%) do if exist "%ROOT%%%D" (echo %%D : present) else (echo %%D : missing)
exit /b 0

:clean
echo Cleaning build outputs ...
taskkill /im "%OUT%" /f >nul 2>&1
if exist "%ROOT%%OUT%" del /q "%ROOT%%OUT%"
for %%D in (SDL2.dll SDL2_image.dll SDL2_ttf.dll libwinpthread-1.dll libgcc_s_seh-1.dll) do if exist "%ROOT%%%D" del /q "%ROOT%%%D"
if exist "%SDLDIR%" rd /s /q "%SDLDIR%"
if exist "%DLTMP%" rd /s /q "%DLTMP%"
echo Done. Downloaded Go/GCC remain in %TOOLS% ^(delete manually to reinstall^).
exit /b 0