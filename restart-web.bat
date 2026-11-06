@echo off
rem 双击入口：编好再换再起，尽量不断线。真正的逻辑在 restart-web.ps1。
rem 想带参数就在命令行里跑：restart-web.bat -Status / -Stop / -Addr :8005
setlocal
set "PS=pwsh"
where pwsh >nul 2>nul
if errorlevel 1 set "PS=powershell"
"%PS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0restart-web.ps1" %*
if errorlevel 1 (
	echo.
	echo 重启没成功，上面有原因。
	pause
)
endlocal
