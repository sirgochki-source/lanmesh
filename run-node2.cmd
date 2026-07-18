@echo off
rem Второй экземпляр lanmesh на этой же машине — для тестов без второго ПК.
rem Своя APPDATA => свой identity => свой PeerID и виртуальный IP.
rem Своё имя адаптера, иначе конфликт с основным клиентом.
rem Запуск из папки со скриптом; подставь своё имя сети и пароль.
setlocal
set APPDATA=%~dp0node2
if not exist "%APPDATA%" mkdir "%APPDATA%"
cd /d "%~dp0"
echo cwd=%CD% > "%APPDATA%\node2.log"
"%~dp0lanmesh.exe" -network YOURNET -password YOURPASS -iface lanmesh2 >> "%APPDATA%\node2.log" 2>&1
