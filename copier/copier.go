package copier

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// CopyProgress represents the copy progress
type CopyProgress struct {
	CompletedFiles int64
	CompletedSize  int64
	CurrentFile    string
	TotalFiles     int64
	TotalSize      int64
	FailedFiles    int64
	SkippedFiles   int64
	Speed          float64 // files per second
	ElapsedTime    time.Duration
	RemainingTime  time.Duration
}

// CopyResult represents the result of a file copy operation
type CopyResult struct {
	FilePath string
	Success  bool
	Error    error
	Size     int64
}

// Copier handles file copying operations
type Copier struct {
	sourceDir    string
	targetDir    string
	progress     CopyProgress
	progressCh   chan CopyProgress
	resultCh     chan CopyResult
	errCh        chan error
	progressMux  sync.Mutex
	useAPFSClone bool
	workerCount  int
	startTime    time.Time
	tickInterval time.Duration
	canceled     int32
	bufferSize   int // per-worker buffer size in bytes
}

// NewCopier creates a new Copier instance
func NewCopier(sourceDir, targetDir string, useAPFSClone bool) *Copier {
	workerCount := runtime.NumCPU()
	if workerCount > 8 {
		workerCount = 8
	}

	tickMs := 500
	if tickMs < 100 {
		tickMs = 100
	}

	return &Copier{
		sourceDir:    sourceDir,
		targetDir:    targetDir,
		progressCh:   make(chan CopyProgress, 100),
		resultCh:     make(chan CopyResult, 1000),
		errCh:        make(chan error, 100),
		useAPFSClone: useAPFSClone,
		workerCount:  workerCount,
		startTime:    time.Now(),
		tickInterval: time.Duration(tickMs) * time.Millisecond,
		bufferSize:   1 * 1024 * 1024, // default 1MB
	}
}

// Close closes all channels
func (c *Copier) Close() {
	close(c.progressCh)
	close(c.resultCh)
	close(c.errCh)
}

// CopyFilesParallel copies multiple files in parallel
func (c *Copier) CopyFilesParallel(files []string) {
	go func() {
		defer c.Close()

		if len(files) == 0 {
			return
		}

		// 비어있는 폴더 포함 모든 디렉터리 미리 생성
		c.ensureAllDirectories()

		// 총 파일 수와 크기 계산
		var totalSize int64
		for _, file := range files {
			if info, err := os.Stat(file); err == nil {
				totalSize += info.Size()
			}
		}

		c.progressMux.Lock()
		c.progress.TotalFiles = int64(len(files))
		c.progress.TotalSize = totalSize
		c.progressMux.Unlock()

		// 파일 채널 생성
		fileChan := make(chan string, len(files))
		var wg sync.WaitGroup

		// 워커들 시작
		for i := 0; i < c.workerCount; i++ {
			wg.Add(1)
			go c.copyWorker(fileChan, &wg)
		}

		// 진행 상황 모니터링
		done := make(chan bool)
		go c.monitorProgress(done)

		// 파일들을 채널에 전송
		for _, file := range files {
			fileChan <- file
		}
		close(fileChan)

		// 모든 워커 완료 대기
		wg.Wait()
		close(done)

		// 최종 진행 상황 전송
		c.sendFinalProgress()
	}()
}

// ensureAllDirectories walks the source tree and creates corresponding directories in the target,
// so that empty directories are preserved.
func (c *Copier) ensureAllDirectories() {
	// 소스 루트가 없으면 스킵
	srcInfo, err := os.Stat(c.sourceDir)
	if err != nil || !srcInfo.IsDir() {
		return
	}

	// 루트도 포함해 순회하며 디렉터리만 생성
	_ = filepath.WalkDir(c.sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 읽기 에러는 전체 중단보다는 스킵
			return nil
		}
		if d.IsDir() {
			rel, rErr := filepath.Rel(c.sourceDir, path)
			if rErr != nil {
				return nil
			}
			dst := filepath.Join(c.targetDir, rel)
			// 빈 문자열(rel==".")이면 타겟 루트 자체
			if rel == "." {
				dst = c.targetDir
			}
			_ = os.MkdirAll(dst, 0755)
		}
		return nil
	})
}

// copyWorker is a worker goroutine that copies files
func (c *Copier) copyWorker(fileChan <-chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	bufSize := c.bufferSize
	if bufSize <= 0 {
		bufSize = 1 * 1024 * 1024
	}
	buffer := make([]byte, bufSize)

	for srcPath := range fileChan {
		if atomic.LoadInt32(&c.canceled) == 1 {
			return
		}
		result := c.copySingleFile(srcPath, buffer)
		c.resultCh <- result

		if result.Success {
			c.progressMux.Lock()
			c.progress.CompletedFiles++
			c.progress.CompletedSize += result.Size
			c.progressMux.Unlock()
		} else {
			c.progressMux.Lock()
			c.progress.FailedFiles++
			c.progressMux.Unlock()
		}
	}
}

// copySingleFile copies a single file
func (c *Copier) copySingleFile(srcPath string, buffer []byte) CopyResult {
	// 상대 경로 계산
	relPath, err := filepath.Rel(c.sourceDir, srcPath)
	if err != nil {
		return CopyResult{
			FilePath: srcPath,
			Success:  false,
			Error:    fmt.Errorf("상대 경로 계산 실패: %v", err),
		}
	}

	dstPath := filepath.Join(c.targetDir, relPath)
	dstDir := filepath.Dir(dstPath)

	// 대상 디렉토리 생성
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return CopyResult{
			FilePath: srcPath,
			Success:  false,
			Error:    fmt.Errorf("디렉토리 생성 실패: %v", err),
		}
	}

	// 파일 정보 가져오기
	info, err := os.Stat(srcPath)
	if err != nil {
		return CopyResult{
			FilePath: srcPath,
			Success:  false,
			Error:    fmt.Errorf("파일 정보 읽기 실패: %v", err),
		}
	}

	// 파일 복사
	if err := c.copyFileContent(srcPath, dstPath, buffer); err != nil {
		return CopyResult{
			FilePath: srcPath,
			Success:  false,
			Error:    err,
			Size:     info.Size(),
		}
	}

	return CopyResult{
		FilePath: srcPath,
		Success:  true,
		Size:     info.Size(),
	}
}

// copyFileContent copies the content of a file
func (c *Copier) copyFileContent(srcPath, dstPath string, buffer []byte) error {
	sourceFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("소스 파일 열기 실패: %v", err)
	}
	defer sourceFile.Close()

	targetFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("대상 파일 생성 실패: %v", err)
	}
	defer targetFile.Close()

	for {
		if atomic.LoadInt32(&c.canceled) == 1 {
			return fmt.Errorf("사용자 취소")
		}
		n, rerr := sourceFile.Read(buffer)
		if n > 0 {
			if _, werr := targetFile.Write(buffer[:n]); werr != nil {
				return fmt.Errorf("쓰기 실패: %v", werr)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("읽기 실패: %v", rerr)
		}
	}

	return nil
}

// monitorProgress monitors and reports copy progress
func (c *Copier) monitorProgress(done <-chan bool) {
	interval := c.tickInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.progressMux.Lock()
			progress := c.progress
			c.progressMux.Unlock()

			// 시간 정보 업데이트
			elapsed := time.Since(c.startTime)
			progress.ElapsedTime = elapsed

			// 속도 계산
			if elapsed.Seconds() > 0 {
				progress.Speed = float64(progress.CompletedFiles) / elapsed.Seconds()
			}

			// 남은 시간 계산
			if progress.Speed > 0 && progress.TotalFiles > progress.CompletedFiles {
				remainingFiles := progress.TotalFiles - progress.CompletedFiles
				remainingSeconds := float64(remainingFiles) / progress.Speed
				progress.RemainingTime = time.Duration(remainingSeconds) * time.Second
			}

			// 진행 상황 전송
			select {
			case c.progressCh <- progress:
			default:
			}
		}
	}
}

// sendFinalProgress sends the final progress update
func (c *Copier) sendFinalProgress() {
	c.progressMux.Lock()
	progress := c.progress
	c.progressMux.Unlock()

	elapsed := time.Since(c.startTime)
	progress.ElapsedTime = elapsed

	if elapsed.Seconds() > 0 {
		progress.Speed = float64(progress.CompletedFiles) / elapsed.Seconds()
	}

	select {
	case c.progressCh <- progress:
	default:
	}
}

// SetTotal sets the total files and size for progress calculation
func (c *Copier) SetTotal(totalFiles, totalSize int64) {
	c.progressMux.Lock()
	c.progress.TotalFiles = totalFiles
	c.progress.TotalSize = totalSize
	c.progressMux.Unlock()
}

// CopyFile copies a single file (legacy method for compatibility)
func (c *Copier) CopyFile(sourcePath string, fileSize int64) error {
	result := c.copySingleFile(sourcePath, make([]byte, 32*1024))
	return result.Error
}

// Progress returns the progress channel
func (c *Copier) Progress() <-chan CopyProgress {
	return c.progressCh
}

// Results returns the result channel
func (c *Copier) Results() <-chan CopyResult {
	return c.resultCh
}

// Errors returns the error channel
func (c *Copier) Errors() <-chan error {
	return c.errCh
}

// Cancel stops ongoing copy as soon as possible
func (c *Copier) Cancel() { atomic.StoreInt32(&c.canceled, 1) }

// SetWorkerCount tunes parallelism (call before CopyFilesParallel)
func (c *Copier) SetWorkerCount(n int) {
	if n < 1 {
		return
	}
	c.workerCount = n
}

// SetBufferSizeMB sets per-worker buffer size (MB)
func (c *Copier) SetBufferSizeMB(mb int) {
	if mb <= 0 {
		return
	}
	c.bufferSize = mb * 1024 * 1024
}
