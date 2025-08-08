package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"superfast-copy-util/copier"
	"superfast-copy-util/scanner"
	"superfast-copy-util/ui"

	"golang.org/x/term"
)

// CopyManager manages the entire copy process
type CopyManager struct {
	scanner      *scanner.Scanner
	copier       *copier.Copier
	sourceDir    string
	targetDir    string
	scanProgress scanner.Progress
	copyProgress copier.CopyProgress
	mu           sync.Mutex
	wg           sync.WaitGroup
	startTime    time.Time
}

// NewCopyManager creates a new copy manager
func NewCopyManager(sourceDir, targetDir string) *CopyManager {
	return &CopyManager{
		scanner:   scanner.NewScanner(),
		copier:    tuneCopierForSystem(sourceDir, targetDir),
		sourceDir: sourceDir,
		targetDir: targetDir,
		startTime: time.Now(),
	}
}

// StartCopy starts the copy process
func (cm *CopyManager) StartCopy() {
	// 스캔 진행 상황 모니터링
	cm.wg.Add(1)
	go cm.monitorScanProgress()

	// 복사 진행 상황 모니터링
	cm.wg.Add(1)
	go cm.monitorCopyProgress()

	// 에러 처리
	cm.wg.Add(1)
	go cm.handleErrors()

	// 파일 복사 작업
	cm.wg.Add(1)
	go cm.copyFiles()

	// 스캔 시작
	cm.scanner.ScanDirectory(cm.sourceDir)

	// 모든 작업 완료 대기
	cm.wg.Wait()
}

// monitorScanProgress monitors scan progress
func (cm *CopyManager) monitorScanProgress() {
	defer cm.wg.Done()
	for progress := range cm.scanner.Progress() {
		cm.mu.Lock()
		cm.scanProgress = progress
		cm.mu.Unlock()

		// GUI 업데이트를 위한 콜백 호출 가능
		cm.onScanProgress(progress)
	}
}

// monitorCopyProgress monitors copy progress
func (cm *CopyManager) monitorCopyProgress() {
	defer cm.wg.Done()
	lastUpdate := time.Now()
	for progress := range cm.copier.Progress() {
		cm.mu.Lock()
		cm.copyProgress = progress
		cm.mu.Unlock()

		// 1초마다 업데이트
		if time.Since(lastUpdate) >= time.Second {
			cm.onCopyProgress(progress)
			lastUpdate = time.Now()
		}
	}
}

// handleErrors handles errors from scanner and copier
func (cm *CopyManager) handleErrors() {
	defer cm.wg.Done()

	// 스캔 에러 처리
	for err := range cm.scanner.Errors() {
		cm.onError("스캔", err)
	}

	// 복사 에러 처리
	for err := range cm.copier.Errors() {
		cm.onError("복사", err)
	}
}

// copyFiles copies files from scanner to copier using parallel processing
func (cm *CopyManager) copyFiles() {
	defer cm.wg.Done()

	var files []string
	var totalSize int64

	// 먼저 모든 파일을 수집
	for file := range cm.scanner.Files() {
		files = append(files, file.Path)
		totalSize += file.Size
	}

	// 총 파일 수와 크기를 copier에 설정
	cm.copier.SetTotal(int64(len(files)), totalSize)

	// 병렬 복사 시작
	cm.copier.CopyFilesParallel(files)

	// 복사 결과 처리
	for result := range cm.copier.Results() {
		if !result.Success {
			cm.onError("복사", fmt.Errorf("파일 복사 실패 %s: %v", result.FilePath, result.Error))
		}
	}
}

// onScanProgress is called when scan progress updates
func (cm *CopyManager) onScanProgress(progress scanner.Progress) {
	elapsedSeconds := int(progress.ElapsedTime.Seconds())
	fmt.Printf("\r스캔 중: %d개 파일 (경과: %d초, 속도: %.1f 파일/초)",
		progress.TotalFiles,
		elapsedSeconds,
		progress.Speed)
}

// onCopyProgress is called when copy progress updates
func (cm *CopyManager) onCopyProgress(progress copier.CopyProgress) {
	var percent float64
	if progress.TotalFiles > 0 {
		percent = float64(progress.CompletedFiles) * 100 / float64(progress.TotalFiles)
	}

	elapsedSeconds := int(progress.ElapsedTime.Seconds())
	remainingSeconds := int(progress.RemainingTime.Seconds())

	fmt.Printf("\r복사 중: %d/%d개 파일, %.1f%% 완료 (경과: %d초, 남은시간: %d초, 속도: %.1f 파일/초)",
		progress.CompletedFiles,
		progress.TotalFiles,
		percent,
		elapsedSeconds,
		remainingSeconds,
		progress.Speed)
}

// onError is called when an error occurs
func (cm *CopyManager) onError(operation string, err error) {
	fmt.Printf("\n%s 오류: %v\n", operation, err)
}

// GetScanProgress returns current scan progress
func (cm *CopyManager) GetScanProgress() scanner.Progress {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.scanProgress
}

// GetCopyProgress returns current copy progress
func (cm *CopyManager) GetCopyProgress() copier.CopyProgress {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.copyProgress
}

func main() {
	// 플래그 파싱: 기본은 GUI, --cli 시 CLI 실행
	cliMode := flag.Bool("cli", false, "CLI 모드로 실행")
	uiMode := flag.Bool("ui", false, "UI(TUI)로 실행")
	flag.Parse()

	if *uiMode || !*cliMode {
		_ = ui.RunTUI()
		return
	}

	fmt.Println("🚀 SuperFast File Copier")
	fmt.Println("==========================")
	fmt.Println()

	var sourceDir, targetDir string
	args := flag.Args()
	if len(args) >= 2 {
		sourceDir = strings.TrimSpace(args[0])
		targetDir = strings.TrimSpace(args[1])
	} else {
		// 소스 디렉토리 입력 받기
		fmt.Print("📁 복사할 소스 디렉토리 경로를 입력하세요: ")
		inputScanner := bufio.NewScanner(os.Stdin)
		inputScanner.Scan()
		sourceDir = strings.TrimSpace(inputScanner.Text())

		if sourceDir == "" {
			fmt.Println("❌ 소스 디렉토리 경로가 입력되지 않았습니다.")
			return
		}

		// 타겟 디렉토리 입력 받기
		fmt.Print("📁 복사할 타겟 디렉토리 경로를 입력하세요: ")
		inputScanner.Scan()
		targetDir = strings.TrimSpace(inputScanner.Text())

		if targetDir == "" {
			fmt.Println("❌ 타겟 디렉토리 경로가 입력되지 않았습니다.")
			return
		}
	}

	fmt.Println()
	fmt.Printf("📂 소스: %s\n", sourceDir)
	fmt.Printf("📂 타겟: %s\n", targetDir)
	fmt.Println()

	// 소스 디렉토리 존재 확인
	if _, err := os.Stat(sourceDir); os.IsNotExist(err) {
		fmt.Printf("❌ 소스 디렉토리가 존재하지 않습니다: %s\n", sourceDir)
		return
	}

	// 복사 매니저 생성 및 시작
	manager := NewCopyManager(sourceDir, targetDir)
	manager.StartCopy()

	fmt.Println()
	fmt.Printf("✅ 복사가 완료되었습니다.\n   - 소스: %s\n   - 타겟: %s\n", sourceDir, targetDir)
	fmt.Print("계속하려면 아무 키나 누르세요...")
	if runtime.GOOS == "windows" {
		// Windows에서는 cmd의 pause를 이용해 아무 키 입력을 즉시 감지
		cmd := exec.Command("cmd", "/C", "pause>nul")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	} else {
		// macOS/Linux: 터미널을 raw 모드로 전환하여 단일 키 입력을 읽음
		fd := int(os.Stdin.Fd())
		if term.IsTerminal(fd) {
			if oldState, err := term.MakeRaw(fd); err == nil {
				defer term.Restore(fd, oldState)
				var b [1]byte
				_, _ = os.Stdin.Read(b[:])
			} else {
				reader := bufio.NewReader(os.Stdin)
				_, _ = reader.ReadBytes('\n')
			}
		} else {
			reader := bufio.NewReader(os.Stdin)
			_, _ = reader.ReadBytes('\n')
		}
	}
}

// tuneCopierForSystem configures copier based on simple system heuristics
func tuneCopierForSystem(sourceDir, targetDir string) *copier.Copier {
	c := copier.NewCopier(sourceDir, targetDir, false)
	// Heuristic: more workers for high CPU count, larger buffer on likely SSD
	cpu := runtime.NumCPU()
	workers := cpu * 2
	if workers > 16 {
		workers = 16
	}
	if workers < 4 {
		workers = 4
	}
	c.SetWorkerCount(workers)

	// Buffer: default 1MB → bump to 4MB for better throughput
	c.SetBufferSizeMB(4)
	return c
}

// UI(TUI) 전용이므로 브라우저 기반 GUI 코드는 제거되었습니다.
