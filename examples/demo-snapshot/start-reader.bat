@echo off
setlocal
pushd "%~dp0" || exit /b 1
"bin\goforconf-reader.exe" --config "config.demo.json" serve
set "reader_exit=%errorlevel%"
popd
exit /b %reader_exit%
