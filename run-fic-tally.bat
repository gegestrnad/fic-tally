@echo off
rem Fic Tally launcher — run the prebuilt binary next to this script.
rem Build it first with: CGO_ENABLED=0 go build -o fic-tally.exe .
cd /d "%~dp0"
fic-tally.exe -addr 0.0.0.0:4242
pause
