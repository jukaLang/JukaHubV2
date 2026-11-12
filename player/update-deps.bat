@echo off
setlocal EnableExtensions
cd /d "%~dp0"
title JukaHub - update Go dependencies

REM ============================================================================
REM  Update every Go module in player/ to its latest stable release, then
REM  re-vendor.  Usage:  update-deps.bat
REM
REM   1. go get -u -t ./...   for windows/amd64 AND linux/arm64 (TrimUI), so
REM                           modules only one platform imports are covered
REM   2. go get toolchain@latest   newest stable Go for the toolchain line
REM                           (the "go 1.26.0" minimum is left alone)
REM   3. go mod tidy + go mod vendor   player/vendor/ is what CI builds from
REM   4. go list both platforms from vendor/ to prove the tree resolves
REM
REM  Pre-release tags are never picked: @latest / -u only take stable semver.
REM  Afterwards run  build.bat nolaunch  and commit go.mod, go.sum, vendor/.
REM ============================================================================

set "GO_EXE="
for /f "delims=" %%G in ('where go.exe 2^>nul') do if not defined GO_EXE set "GO_EXE=%%G"
if not defined GO_EXE if exist "%ProgramFiles%\Go\bin\go.exe" set "GO_EXE=%ProgramFiles%\Go\bin\go.exe"
if not defined GO_EXE if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "GO_EXE=%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GO_EXE for /d %%D in ("%LOCALAPPDATA%\JukaHub\tools\go1.*") do if not defined GO_EXE if exist "%%~fD\go\bin\go.exe" set "GO_EXE=%%~fD\go\bin\go.exe"
if not defined GO_EXE (
    echo [x] No Go found. Run  build.bat nobuild  once to install it, then retry.
    exit /b 1
)
for %%I in ("%GO_EXE%") do set "PATH=%%~dpI;%PATH%"
echo [ok] using %GO_EXE%
go version

REM Module mode for the upgrade itself (vendor/ is rebuilt at the end).
set "GOFLAGS=-mod=mod"
REM cgo on so the cgo-only files (go-sdl2 etc.) count when listing imports;
REM nothing is compiled, so no cross gcc is needed.
set "CGO_ENABLED=1"
if not defined GOPROXY set "GOPROXY=https://proxy.golang.org,direct"

copy /y go.mod "%TEMP%\jukahub-go.mod.bak" >nul
copy /y go.sum "%TEMP%\jukahub-go.sum.bak" >nul

echo.
echo === [1/4] go get -u -t ./...  (windows/amd64) ===
set "GOOS=windows" & set "GOARCH=amd64"
go get -u -t ./... || goto :fail
echo === [1/4] go get -u -t ./...  (linux/arm64) ===
set "GOOS=linux" & set "GOARCH=arm64"
go get -u -t ./... || goto :fail
set "GOOS=" & set "GOARCH="

echo.
echo === [2/4] go get toolchain@latest ===
go get toolchain@latest || echo [!] toolchain bump skipped ^(offline proxy?^) - continuing

echo.
echo === [3/4] go mod tidy + go mod vendor ===
go mod tidy || goto :fail
go mod vendor || goto :fail

echo.
echo === [4/4] resolve from vendor/ on both platforms ===
set "GOFLAGS=-mod=vendor"
set "GOOS=windows" & set "GOARCH=amd64"
go list -deps -test ./... >nul || goto :fail
set "GOOS=linux" & set "GOARCH=arm64"
go list -deps -test ./... >nul || goto :fail
set "GOOS=" & set "GOARCH="

echo.
echo === changes ===
git --no-optional-locks diff --stat -- go.mod go.sum vendor/modules.txt
git --no-optional-locks diff -U0 -- go.mod
echo.
echo [ok] Dependencies updated. Next:  build.bat nolaunch   then commit go.mod, go.sum and vendor/.
exit /b 0

:fail
echo.
echo [x] Update failed - restoring go.mod / go.sum (vendor/ untouched unless step 3 ran).
copy /y "%TEMP%\jukahub-go.mod.bak" go.mod >nul
copy /y "%TEMP%\jukahub-go.sum.bak" go.sum >nul
exit /b 1
