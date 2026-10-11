@echo off
rem Columba build script (keep -H windowsgui for dropw.exe, or double-click shows a console window)
go build -ldflags "-s -w" -o drop.exe ./cmd/drop || (echo drop.exe build failed & exit /b 1)
go build -ldflags "-s -w -H windowsgui" -o dropw.exe ./cmd/drop || (echo dropw.exe build failed & exit /b 1)
echo build done: drop.exe = CLI, dropw.exe = background daemon
