@echo off
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"

goto :main

REM ----------------------------------------------------------------------------
REM  fail(message)
REM ----------------------------------------------------------------------------
:fail
echo.
echo ERROR: %~1
echo.
pause
exit /b 1

REM ----------------------------------------------------------------------------
REM  MAIN
REM ----------------------------------------------------------------------------
:main

set "OUT=JukaHub.exe"
set "ARCH=x86_64-w64-mingw32"
set "SDLDIR=%cd%\.sdl2"
set "PS=powershell -NoProfile -ExecutionPolicy Bypass -Command"

echo Checking for GCC...
where gcc.exe >nul 2>&1
if errorlevel 1 goto install_gcc

echo GCC found.
goto sdl_check


REM ============================================================================
REM  AUTOMATIC GCC INSTALLER (MSYS2 UCRT64)
REM ============================================================================
:install_gcc
echo.
echo ============================================================
echo  GCC NOT FOUND - Installing MSYS2 + UCRT64 GCC automatically
echo ============================================================
echo.

set "MSYS_URL=https://github.com/msys2/msys2-installer/releases/latest/download/msys2-x86_64-latest.exe"

set "MSYS_EXE=%TEMP%\msys2-installer.exe"

echo Downloading MSYS2 installer...
%PS% "Invoke-WebRequest -Uri '%MSYS_URL%' -OutFile '%MSYS_EXE%' -UseBasicParsing" || (
    call :fail "Failed to download MSYS2 installer"
)

echo Running MSYS2 installer silently...
"%MSYS_EXE%" install --confirm-command --root C:\msys64 --default-answer=yes || (
    call :fail "MSYS2 installation failed"
)

echo Updating MSYS2 packages...
C:\msys64\usr\bin\bash -lc "pacman -Sy --noconfirm" || (
    call :fail "MSYS2 update failed"
)

echo Installing UCRT64 GCC toolchain...
C:\msys64\usr\bin\bash -lc "pacman -S --noconfirm mingw-w64-ucrt-x86_64-gcc" || (
    call :fail "GCC installation failed"
)

echo Adding MSYS2 UCRT64 to PATH...
setx PATH "%PATH%;C:\msys64\ucrt64\bin"

echo.
echo ============================================================
echo  GCC installation complete!
echo  Please restart your terminal and re-run build.bat
echo ============================================================
echo.
pause
exit /b 0


REM ============================================================================
REM  SDL2 DISCOVERY + AUTO-DOWNLOAD
REM ============================================================================
:sdl_check

if defined SDL2_DIR (
    echo Using SDL2 from %SDL2_DIR%
    set "CGO_CFLAGS=-I%SDL2_DIR%\include"
    set "CGO_LDFLAGS=-L%SDL2_DIR%\lib"
    goto build
)

set "HEADER=%SDLDIR%\%ARCH%\include\SDL2\SDL.h"
if exist "%HEADER%" goto build

echo.
echo ============================================================
echo  Downloading SDL2 development libraries
echo ============================================================

set "SDL2_VER=2.30.5"
set "IMG_VER=2.8.2"
set "TTF_VER=2.22.0"

set "SDL2_URL=https://libsdl.org/release/SDL2-devel-%SDL2_VER%-mingw.tar.gz"
set "IMG_URL=https://libsdl.org/projects/SDL_image/release/SDL2_image-devel-%IMG_VER%-mingw.tar.gz"
set "TTF_URL=https://libsdl.org/projects/SDL_ttf/release/SDL2_ttf-devel-%TTF_VER%-mingw.tar.gz"

set "SDL2_TGZ=%TEMP%\jukasdl2.tar.gz"
set "IMG_TGZ=%TEMP%\jukasdl2_image.tar.gz"
set "TTF_TGZ=%TEMP%\jukasdl2_ttf.tar.gz"

echo Downloading SDL2...
%PS% "Invoke-WebRequest -Uri '%SDL2_URL%' -OutFile '%SDL2_TGZ%' -UseBasicParsing" || call :fail "SDL2 download failed"

echo Downloading SDL2_image...
%PS% "Invoke-WebRequest -Uri '%IMG_URL%' -OutFile '%IMG_TGZ%' -UseBasicParsing" || call :fail "SDL2_image download failed"

echo Downloading SDL2_ttf...
%PS% "Invoke-WebRequest -Uri '%TTF_URL%' -OutFile '%TTF_TGZ%' -UseBasicParsing" || call :fail "SDL2_ttf download failed"

echo Extracting SDL2 packages...
rmdir /s /q "%TEMP%\jukasdl2" 2>nul
mkdir "%TEMP%\jukasdl2"

for %%F in ("%SDL2_TGZ%" "%IMG_TGZ%" "%TTF_TGZ%") do (
    tar -xzf "%%~F" -C "%TEMP%\jukasdl2" || call :fail "Extraction failed"
)

rmdir /s /q "%SDLDIR%" 2>nul
mkdir "%SDLDIR%\%ARCH%\include"
mkdir "%SDLDIR%\%ARCH%\lib"
mkdir "%SDLDIR%\%ARCH%\bin"

for %%P in (SDL2-%SDL2_VER% SDL2_image-%IMG_VER% SDL2_ttf-%TTF_VER%) do (
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\include\*" "%SDLDIR%\%ARCH%\include\" >nul
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\lib\*" "%SDLDIR%\%ARCH%\lib\" >nul
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\bin\*" "%SDLDIR%\%ARCH%\bin\" >nul
)


REM ============================================================================
REM  BUILD
REM ============================================================================
:build
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

if not defined CGO_CFLAGS (
    set "CGO_CFLAGS=-I%SDLDIR%\%ARCH%\include"
    set "CGO_LDFLAGS=-L%SDLDIR%\%ARCH%\lib"
)

if /i "%1"=="nobuild" goto after_build

echo.
echo ============================================================
echo  Building %OUT%
echo ============================================================

go build -o "%OUT%" .
if errorlevel 1 call :fail "Build failed"


REM ============================================================================
REM  POST-BUILD
REM ============================================================================
:after_build

if not exist "%OUT%" call :fail "%OUT% missing"

set DLL_COPIED=0

if exist "%~dp0SDL2.dll" (
    set DLL_COPIED=1
) else if exist "%SDLDIR%\%ARCH%\bin\SDL2.dll" (
    xcopy /y "%SDLDIR%\%ARCH%\bin\*.dll" "%~dp0" >nul
    set DLL_COPIED=1
)

if "!DLL_COPIED!"=="0" (
    echo WARNING: SDL2 runtime DLLs missing.
)

echo.
echo Build succeeded: %OUT%
echo.

if defined GITHUB_ACTIONS goto :eof
if defined CI goto :eof

echo Launching %OUT%...
start "" "%OUT%"
exit /b 0
