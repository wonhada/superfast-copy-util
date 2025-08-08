#!/bin/bash
# macOS/Linux용 빌드 스크립트

echo "🚀 SuperFast File Copier - Unix Build Script"
echo

# Go가 설치되어 있는지 확인
if ! command -v go &> /dev/null; then
    echo "❌ Go가 설치되지 않았습니다."
    echo "   Go를 설치하고 다시 시도하세요."
    echo "   다운로드: https://golang.org/dl/"
    exit 1
fi

# Go 버전 확인
echo "✅ Go 확인: $(go version)"
echo

# build 폴더 생성
mkdir -p build

echo "📦 Go 빌드 실행 중..."

# 현재 플랫폼 확인 및 빌드
OS=$(uname -s)
case $OS in
    Linux*)
        PLATFORM="linux"
        BINARY_NAME="superfast-copier-linux"
        ;;
    Darwin*)
        ARCH=$(uname -m)
        if [ "$ARCH" = "arm64" ]; then
            PLATFORM="darwin-arm64"
            BINARY_NAME="superfast-copier-mac-m1"
        else
            PLATFORM="darwin-amd64"
            BINARY_NAME="superfast-copier-mac-intel"
        fi
        ;;
    *)
        PLATFORM="unknown"
        BINARY_NAME="superfast-copier"
        ;;
esac

echo "🖥️  현재 플랫폼 빌드 ($PLATFORM)..."
go build -o "build/$BINARY_NAME" .
if [ $? -ne 0 ]; then
    echo "❌ 현재 플랫폼 빌드 실패"
    exit 1
fi
echo "✅ 현재 플랫폼 빌드 완료: build/$BINARY_NAME"

# 모든 플랫폼 빌드
echo
echo "🌍 모든 플랫폼 빌드..."

# Linux 빌드
echo "🐧 Linux 빌드..."
GOOS=linux GOARCH=amd64 go build -o build/superfast-copier-linux .
if [ $? -eq 0 ]; then
    echo "✅ Linux 빌드 완료: build/superfast-copier-linux"
else
    echo "❌ Linux 빌드 실패"
fi

# Windows 빌드
echo "🖥️  Windows 빌드..."
GOOS=windows GOARCH=amd64 go build -o build/superfast-copier-windows.exe .
if [ $? -eq 0 ]; then
    echo "✅ Windows 빌드 완료: build/superfast-copier-windows.exe"
else
    echo "❌ Windows 빌드 실패"
fi

# macOS 빌드 (Intel)
echo "🍎 macOS Intel 빌드..."
GOOS=darwin GOARCH=amd64 go build -o build/superfast-copier-mac-intel .
if [ $? -eq 0 ]; then
    echo "✅ macOS Intel 빌드 완료: build/superfast-copier-mac-intel"
else
    echo "❌ macOS Intel 빌드 실패"
fi

# macOS 빌드 (Apple Silicon)
echo "🍎 macOS Apple Silicon 빌드..."
GOOS=darwin GOARCH=arm64 go build -o build/superfast-copier-mac-m1 .
if [ $? -eq 0 ]; then
    echo "✅ macOS Apple Silicon 빌드 완료: build/superfast-copier-mac-m1"
else
    echo "❌ macOS Apple Silicon 빌드 실패"
fi

echo
echo "✅ 빌드가 성공적으로 완료되었습니다!"
echo
echo "📁 생성된 파일들 (build 폴더):"
echo "   - $BINARY_NAME (현재 플랫폼)"
[ -f build/superfast-copier-linux ] && echo "   - superfast-copier-linux (Linux)"
[ -f build/superfast-copier-windows.exe ] && echo "   - superfast-copier-windows.exe (Windows)"
[ -f build/superfast-copier-mac-intel ] && echo "   - superfast-copier-mac-intel (macOS Intel)"
[ -f build/superfast-copier-mac-m1 ] && echo "   - superfast-copier-mac-m1 (macOS Apple Silicon)"