@echo off
chcp 65001 >nul
REM Windows용 빌드 스크립트
echo 🚀 SuperFast File Copier - Windows Build Script
echo.

REM Go가 설치되어 있는지 확인
go version >nul 2>&1
if %errorlevel% neq 0 (
    echo ❌ Go가 설치되지 않았습니다.
    echo    Go를 설치하고 다시 시도하세요.
    echo    다운로드: https://golang.org/dl/
    pause
    exit /b 1
)

REM Go 버전 확인
echo ✅ Go 확인:
go version

REM build 폴더 생성
if not exist build mkdir build

echo.
echo 📦 Go 빌드 실행 중...
echo 🌍 모든 플랫폼 빌드...

REM Windows 빌드
echo 🖥️ Windows 빌드...
go build -o build\superfast-copier-windows.exe .
if %errorlevel% neq 0 (
    echo ❌ Windows 빌드 실패
    goto :error
)
echo ✅ Windows 빌드 완료: build\superfast-copier-windows.exe

REM Linux 빌드
echo 🐧 Linux 빌드...
set GOOS=linux
set GOARCH=amd64
go build -o build\superfast-copier-linux .
if %errorlevel% neq 0 (
    echo ❌ Linux 빌드 실패
) else (
    echo ✅ Linux 빌드 완료: build\superfast-copier-linux
)

REM macOS 빌드 (Intel)
echo 🍎 macOS Intel 빌드...
set GOOS=darwin
set GOARCH=amd64
go build -o build\superfast-copier-mac-intel .
if %errorlevel% neq 0 (
    echo ❌ macOS Intel 빌드 실패
) else (
    echo ✅ macOS Intel 빌드 완료: build\superfast-copier-mac-intel
)

REM macOS 빌드 (Apple Silicon)
echo 🍎 macOS Apple Silicon 빌드...
set GOOS=darwin
set GOARCH=arm64
go build -o build\superfast-copier-mac-m1 .
if %errorlevel% neq 0 (
    echo ❌ macOS Apple Silicon 빌드 실패
) else (
    echo ✅ macOS Apple Silicon 빌드 완료: build\superfast-copier-mac-m1
)

REM 환경 변수 초기화
set GOOS=
set GOARCH=

echo.
echo ✅ 빌드가 성공적으로 완료되었습니다!
echo.
echo 📁 생성된 파일들 (build 폴더):
echo    - superfast-copier-windows.exe (Windows)
if exist build\superfast-copier-linux echo    - superfast-copier-linux (Linux)
if exist build\superfast-copier-mac-intel echo    - superfast-copier-mac-intel (macOS Intel)
if exist build\superfast-copier-mac-m1 echo    - superfast-copier-mac-m1 (macOS Apple Silicon)

goto :end

:error
echo.
echo ❌ 빌드 중 오류가 발생했습니다.

:end
echo.
echo 아무 키나 누르세요...
pause >nul