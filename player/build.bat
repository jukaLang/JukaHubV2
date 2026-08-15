@echo off
REM ============================================================================
REM  JukaHub / JukaHub - Windows build & run script
REM
REM  What this does:
REM    1. Installs the SDL2 development libraries (MinGW-w64) if missing, so the
REM       "SDL2/SDL.h: No such file or directory" CGO errors go away.
REM    2. Builds the player (JukaHub.exe) with CGO enabled.
REM    3. Runs it.
REM
REM  REQUIREMENTS:
REM    - Go (https://go.dev/dl) on PATH.
REM    - A MinGW-w64 C compiler on PATH (gcc.exe required by CGO). e.g. from
REM      https://www.mingw-w64.org or MSYS2. If you already have an SDL2 dev
REM      install, set SDL2_DIR to its folder and the download is skipped.
REM ============================================================================

setlocal
cd /d "%~dp0"

set OUT=JukaHub.exe
set ARCH=x86_64-w64-mingw32

if defined SDL2_DIR (
    set "CGO_CFLAGS=-I%SDL2_DIR%\include"
    set "CGO_LDFLAGS=-L%SDL2_DIR%\lib"
    goto :build
)

set SDLDIR=%cd%\.sdl2
set HEADER=%SDLDIR%\%ARCH%\include\SDL2\SDL.h
if exist "%HEADER%" goto :build

echo.
echo ============================================================
echo  Downloading SDL2 development libraries for MinGW-w64
echo ============================================================
echo.

set SDL2_VER=2.30.5
set IMG_VER=2.8.2
set TTF_VER=2.22.0

set SDL2_URL=https://libsdl.org/release/SDL2-devel-%SDL2_VER%-mingw.tar.gz
set IMG_URL=https://libsdl.org/projects/SDL_image/release/SDL2_image-devel-%IMG_VER%-mingw.tar.gz
set TTF_URL=https://libsdl.org/projects/SDL_ttf/release/SDL2_ttf-devel-%TTF_VER%-mingw.tar.gz

set SDL2_TGZ=%TEMP%\jukasdl2.tar.gz
set IMG_TGZ=%TEMP%\jukasdl2_image.tar.gz
set TTF_TGZ=%TEMP%\jukasdl2_ttf.tar.gz

powershell -NoProfile -Command "Invoke-WebRequest -Uri '%SDL2_URL%' -OutFile '%SDL2_TGZ%'"
if errorlevel 1 goto :dl_fail
powershell -NoProfile -Command "Invoke-WebRequest -Uri '%IMG_URL%' -OutFile '%IMG_TGZ%'"
if errorlevel 1 goto :dl_fail
powershell -NoProfile -Command "Invoke-WebRequest -Uri '%TTF_URL%' -OutFile '%TTF_TGZ%'"
if errorlevel 1 goto :dl_fail

echo Extracting SDL2 dev packages...
if exist "%TEMP%\jukasdl2" rmdir /s /q "%TEMP%\jukasdl2"
mkdir "%TEMP%\jukasdl2"
tar -xzf "%SDL2_TGZ%" -C "%TEMP%\jukasdl2"
if errorlevel 1 goto :tar_fail
tar -xzf "%IMG_TGZ%" -C "%TEMP%\jukasdl2"
if errorlevel 1 goto :tar_fail
tar -xzf "%TTF_TGZ%" -C "%TEMP%\jukasdl2"
if errorlevel 1 goto :tar_fail

if exist "%SDLDIR%" rmdir /s /q "%SDLDIR%"
mkdir "%SDLDIR%\%ARCH%\include"
mkdir "%SDLDIR%\%ARCH%\lib"

for %%P in (SDL2-%SDL2_VER% SDL2_image-%IMG_VER% SDL2_ttf-%TTF_VER%) do (
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\include\*" "%SDLDIR%\%ARCH%\include\"
    xcopy /s /e /y "%TEMP%\jukasdl2\%%P\%ARCH%\lib\*" "%SDLDIR%\%ARCH%\lib\"
)

:build
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

if not defined CGO_CFLAGS (
    set "CGO_CFLAGS=-I%SDLDIR%\%ARCH%\include"
    set "CGO_LDFLAGS=-L%SDLDIR%\%ARCH%\lib"
)

echo.
echo ============================================================
echo  Building %OUT%
echo ============================================================
echo.

go build -o "%OUT%" .
if errorlevel 1 (
    echo.
    echo BUILD FAILED
    echo  Ensure a MinGW-w64 gcc.exe is on PATH and SDL2 headers are available.
    echo  Tip: set SDL2_DIR to your SDL2 dev folder and re-run.
    pause
    exit /b 1
)

echo.
echo Build succeeded: %OUT%
echo.

if "%1"=="nobuild" goto :eof

echo Launching %OUT%...
echo.
start "" "%OUT%"
goto :eof

:dl_fail
echo.
echo DOWNLOAD FAILED - could not download SDL2 dev libraries.
echo  Set SDL2_DIR to an existing SDL2 MinGW dev install and re-run.
pause
exit /b 1

:tar_fail
echo.
echo EXTRACT FAILED - tar.exe could not extract the SDL2 packages.
echo  Ensure tar.exe is available or set SDL2_DIR manually.
pause
exit /b 1
