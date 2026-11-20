@echo off
setlocal
rem JukaPad receiver for Windows: finds Python 3, installs vgamepad once
rem (it brings the ViGEmBus driver), then runs jukapad_receiver.py.
rem Extra arguments go to the receiver, e.g.  jukapad-receiver.bat --tsp 192.168.1.42
set "PY="
py -3 -c "import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)" >nul 2>nul && set "PY=py -3"
if not defined PY python -c "import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)" >nul 2>nul && set "PY=python"
if not defined PY goto nopython
%PY% -c "import vgamepad" >nul 2>nul
if not errorlevel 1 goto run
echo Installing vgamepad - accept the ViGEmBus driver installer if it opens...
%PY% -m pip install --user vgamepad
if errorlevel 1 goto nopip
:run
%PY% "%~dp0jukapad_receiver.py" %*
if errorlevel 1 pause
endlocal
exit /b 0
:nopython
echo Python 3.8 or newer is not installed.
echo Get it from https://www.python.org/downloads/ and tick "Add python.exe to PATH", then run this again.
pause
endlocal
exit /b 1
:nopip
echo Could not install vgamepad. Check the internet connection and try again,
echo or run:  %PY% -m pip install --user vgamepad
pause
endlocal
exit /b 1
