cd %~dp0
set CGO_ENABLED=0
set GOARCH=amd64
set GOOS=linux
go build -o bin/wstunnel-linux-amd64
set GOARCH=386
set GOOS=linux
go build -o bin/wstunnel-linux-386
set GOARCH=amd64
set GOOS=windows
go build -o bin/wstunnel-windows-amd64.exe
set GOARCH=386
set GOOS=windows
go build -o bin/wstunnel-windows-386.exe
pause