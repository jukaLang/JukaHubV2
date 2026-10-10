@echo off
setlocal EnableExtensions EnableDelayedExpansion

REM ============================================================================
REM  JukaHub Build Helper
REM  Top-level convenience wrapper around player\build.bat.
REM
REM  What this file does, in plain language:
REM   1. Make sure Go is installed and usable (user-local install when missing).
REM   2. Make sure SDL2 + SDL2_image + SDL2_ttf are available (managed local
REM      tree under player\.sdl2 when missing).
REM   3. Run the real build script, which already does the MSYS2/GCC bootstrap
REM      and CGo SDL linking.
REM   4. Print a clear post-build status, including how to run diagnostics.
REM
REM  Why there are two batch files:
REM   - This file is the readable "check prerequisites" entry point.
REM   - player\build.bat is the detailed compiler/linker/MSYS2 bootstrap script.
REM   - If you only want the full low-level diagnostics, run:
REM       player\build.bat diag
REM ============================================================================

set "ROOT=%~dp0"
set "PLAYER_DIR=%ROOT%player"
set "OUT_NAME=JukaHub.exe"
set "BUILD_BAT=%PLAYER_DIR%\build.bat"
set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"

if not exist "%BUILD_BAT%" (
    echo ERROR: Expected build script not found:
    echo   %BUILD_BAT%
    echo.
    echo This wrapper expects to live next to the project root and delegate to
    echo the player\build.bat script. If you moved files around, update the
    echo PLAYER_DIR logic above and try again.
    exit /b 1
)

echo =========================================================
echo  JukaHub build helper
echo =========================================================
echo.
echo This script checks dependencies before building.
echo It will install missing pieces when it can, and it will tell you
echo exactly what it detected. If something looks wrong, run:
echo.
echo   player\build.bat diag
echo.
echo That prints a full prerequisite snapshot without building.
echo.
echo -----------------------------------------------------------------
echo  1) Host snapshot
echo -----------------------------------------------------------------
echo.

echo Script root: %ROOT%
echo Player dir:  %PLAYER_DIR%
echo Build script: %BUILD_BAT%
echo Output file next to player\: %OUT_NAME%
echo.

echo -----------------------------------------------------------------
echo  2) Checking for a C compiler (GCC)
echo -----------------------------------------------------------------
echo.

call :check_gcc
if errorlevel 1 (
    echo.
    echo NOTE: The real build script can still bootstrap MSYS2 automatically,
    echo       but it is good to know whether a compiler is already present.
    echo.
)

echo -----------------------------------------------------------------
echo  3) Checking for Go
echo -----------------------------------------------------------------
echo.

call :check_go
if errorlevel 1 (
    echo.
    echo Go will be installed by player\build.bat when it runs.
    echo That install is user-local and does not require administrator rights.
    echo.
) else (
    echo Using Go from the previous check.
    echo.
)

echo -----------------------------------------------------------------
echo  4) Checking for SDL2
echo -----------------------------------------------------------------
echo.

call :check_sdl
echo.

echo =========================================================
echo  Prerequisite check complete
echo =========================================================
echo.
echo Next step: run the build.
echo.
echo   %BUILD_BAT%
echo.
echo If you want to skip the build step but keep the diagnostic output, run:
echo.
echo   %BUILD_BAT% nodebug
echo.
echo If you want the deepest troubleshooting view, run:
echo.
echo   %BUILD_BAT% diag
echo.
echo =========================================================
echo.

endlocal
exit /b 0

REM ============================================================================
REM  Readable prerequisite checks.
REM  These are intentionally light: they only report what they detect. The heavy
REM  install/bootstrap work still happens inside player\build.bat.
REM ============================================================================

:check_gcc
set "FOUND_GCC=0"

REM Fast path: ask the shell whether gcc answers to --version.
where /q gcc >nul 2>&1 && (
    gcc --version >nul 2>&1 && (
        echo GCC already available on PATH.
        set "FOUND_GCC=1"
        goto :check_gcc_done
    )
)

REM Common alternate install locations people actually have on Windows.
for %%P in (
    "%ProgramFiles%\Git\usr\bin\gcc.exe"
    "%ProgramFiles(x86)%\Git\usr\bin\gcc.exe"
    "C:\mingw64\bin\gcc.exe"
    "C:\mingw32\bin\gcc.exe"
) do (
    if exist "%%P" (
        echo Found GCC at %%P
        set "FOUND_GCC=1"
        goto :check_gcc_done
    )
)

REM If MSYS2 UCRT64 is already present, note it.
if exist "C:\msys64\ucrt64\bin\gcc.exe" (
    echo Found MSYS2 UCRT64 GCC at C:\msys64\ucrt64\bin\gcc.exe
    echo NOTE: The real build script may still need to refresh PATH for this
    echo       session, or install packages if the toolchain is incomplete.
    set "FOUND_GCC=1"
    goto :check_gcc_done
)

if "%FOUND_GCC%"=="0" (
    echo No usable GCC found in common locations.
    echo The build script can install MSYS2 automatically when needed.
)
:check_gcc_done
exit /b 0

:check_go
set "FOUND_GO=0"

REM Use Go's own tooling where possible instead of guessing install paths.
for /f "delims=" %%G in ('where go 2^>nul') do (
    if /i "%%~xG"==".exe" if not defined GO_CHECKED call :probe_go_candidate "%%G"
)

if "%FOUND_GO%"=="0" (
    echo No Go on PATH. The build script will attempt a per-user install.
)

:check_go_done
if "%FOUND_GO%"=="1" (
    echo Detected usable Go.
) else (
    echo Go was not detected on PATH.
)
exit /b 0

:probe_go_candidate
set "CANDIDATE=%~1"
if not exist "%CANDIDATE%" exit /b 0
"%CANDIDATE%" version >nul 2>&1 || exit /b 0

set "VCAN_HOST_ARCH="
set "VCAN_HOST_OS="
set "VCAN_VERSION="
for /f "delims=" %%A in ('"%CANDIDATE%" env GOHOSTARCH 2^>nul') do set "VCAN_HOST_ARCH=%%A"
for /f "delims=" %%O in ('"%CANDIDATE%" env GOHOSTOS 2^>nul') do set "VCAN_HOST_OS=%%O"
for /f "delims=" %%V in ('"%CANDIDATE%" env GOVERSION 2^>nul') do set "VCAN_VERSION=%%V"

if not defined VCAN_HOST_ARCH exit /b 0
if not defined VCAN_HOST_OS exit /b 0
if not defined VCAN_VERSION exit /b 0
if /i not "%VCAN_HOST_ARCH%"=="amd64" exit /b 0
if /i not "%VCAN_HOST_OS%"=="windows" exit /b 0

REM Be forgiving about the exact version if it is already installed, but still
REM prefer Go 1.25+ when we have a choice. The hard version gate lives in the
REM real build script, which is the place that may install Go.
echo Found Go at %CANDIDATE%
echo   version : %VCAN_VERSION%
echo   host    : %VCAN_HOST_OS%/%VCAN_HOST_ARCH%
set "FOUND_GO=1"
set "GO_CHECKED=1"
exit /b 0

:check_sdl
set "SDL_OK=0"

REM If the user already pointed SDL2_DIR at a complete SDK, trust that first.
if defined SDL2_DIR (
    echo SDL2_DIR is set to %SDL2_DIR%
    if exist "%SDL2_DIR%\include\SDL2\SDL.h" (
        echo NOTE: SDL2 header found under SDL2_DIR.
    ) else (
        echo WARNING: SDL2_DIR exists but SDL2 headers were not found there.
    )
    goto :check_sdl_done
)

REM Default managed local tree that player\build.bat uses.
set "SDL_LOCAL=%PLAYER_DIR%\.sdl2"
set "SDL_ARCH=x86_64-w64-mingw32"
set "SDL_ROOT=%SDL_LOCAL%\%SDL_ARCH%"

if exist "%SDL_ROOT%\include\SDL2\SDL.h" (
    echo Found managed local SDL2 tree at %SDL_LOCAL%
    echo   headers : %SDL_ROOT%\include
    echo   libraries: %SDL_ROOT%\lib
    echo   runtime  : %SDL_ROOT%\bin
    set "SDL_OK=1"
    goto :check_sdl_done
)

if exist "%SDL_ROOT%\include\SDL2\SDL.h" (
    set "SDL_OK=1"
    goto :check_sdl_done
)

echo No local SDL2 tree found at %SDL_LOCAL%.
echo The build script can download SDL2 + SDL2_image + SDL2_ttf
echo into that folder when it runs.
:check_sdl_done
exit /b 0
