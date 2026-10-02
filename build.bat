@echo off
rem Columba 构建脚本（固化旗标，防止漏 -H windowsgui 导致双击出现命令行窗口）
rem 注意：daemon 运行中时 dropw.exe 被占用，先退出托盘程序再构建它
go build -ldflags "-s -w" -o drop.exe ./cmd/drop || (echo drop.exe 构建失败 & exit /b 1)
go build -ldflags "-s -w -H windowsgui" -o dropw.exe ./cmd/drop || (echo dropw.exe 构建失败 & exit /b 1)
echo 构建完成：drop.exe = 命令行工具；dropw.exe = 无窗口后台（勿删 -H windowsgui）
