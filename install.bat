@echo off
setlocal
rem "go install ./src/cli" would produce cli.exe, because the binary name
rem comes from the package directory. Keep agent-dev.exe by using build -o.
for /f "delims=" %%i in ('go env GOPATH') do set "GOPATH_DIR=%%i"
if not exist "%GOPATH_DIR%\bin" mkdir "%GOPATH_DIR%\bin"
go build -o "%GOPATH_DIR%\bin\agent-dev.exe" ./src/cli
if errorlevel 1 (
	echo build failed
	exit /b 1
)
echo installed: %GOPATH_DIR%\bin\agent-dev.exe
