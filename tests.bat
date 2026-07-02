@echo off
setlocal enabledelayedexpansion
 

set ROOT=%~dp0
set BORAN_DIR=%ROOT%boran
set SAMPLES_DIR=%ROOT%samples
set EXE=%BORAN_DIR%\boran.exe
 
echo ================================================
echo  Building Boran parser
echo ================================================
 
if not exist "%BORAN_DIR%" (
    echo [ERROR] Could not find boran folder at "%BORAN_DIR%"
    exit /b 1
)
 
pushd "%BORAN_DIR%"
go build -o boran.exe .
if errorlevel 1 (
    echo [ERROR] Build failed.
    popd
    exit /b 1
)
popd
 
echo [OK] Build succeeded: %EXE%
echo.
 
if not exist "%SAMPLES_DIR%" (
    echo [ERROR] Could not find samples folder at "%SAMPLES_DIR%"
    exit /b 1
)
 
echo ================================================
echo  Running tests
echo ================================================
 
set /a TOTAL=0
set /a PASS=0
set /a FAIL=0
 
for %%F in ("%SAMPLES_DIR%\*.bn") do (
    set /a TOTAL+=1
    echo.
    echo ---- %%~nxF ----
    "%EXE%" "%%F"
    if errorlevel 1 (
        echo [FAIL] %%~nxF
        set /a FAIL+=1
    ) else (
        echo [PASS] %%~nxF
        set /a PASS+=1
    )
)
 
echo.
echo ================================================
echo  Summary: !PASS!/!TOTAL! passed, !FAIL!/!TOTAL! failed
echo ================================================
 
if !FAIL! GTR 0 (
    exit /b 1
)
exit /b 0