@echo off
rem Columba build script (keep -H windowsgui for dropw.exe, or double-click shows a console window)
rem Clear inherited GOWORK=off: the go.work replace (third_party fork) is REQUIRED here.
rem A stale GOWORK=off silently builds against the unpatched module-cache go-webview2.
set "GOWORK="
set "GOFLAGS="
go build -ldflags "-s -w" -o drop.exe ./cmd/drop || (echo drop.exe build failed & exit /b 1)
go build -ldflags "-s -w -H windowsgui" -o dropw.exe ./cmd/drop || (echo dropw.exe build failed & exit /b 1)
echo build done: drop.exe = CLI, dropw.exe = background daemon
