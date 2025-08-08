package scanner

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Progress represents the scanning progress
type Progress struct {
	TotalFiles  int64
	TotalSize   int64
	Speed       float64 // files per second
	ElapsedTime time.Duration
}

// FileInfo represents information about a file
type FileInfo struct {
	Path string
	Size int64
	Dir  string
}

// Scanner handles file scanning operations
type Scanner struct {
	progress     Progress
	progressCh   chan Progress
	filesCh      chan FileInfo
	errCh        chan error
	progressMux  sync.Mutex
	startTime    time.Time
	concurrency  int
	tickInterval time.Duration
	totalFiles   int64 // atomic
	totalSize    int64 // atomic
	canceled     int32 // atomic flag
}

// NewScanner creates a new Scanner instance
func NewScanner() *Scanner {
	progressBuf := getEnvInt("SCANNER_PROGRESS_BUF", 100)
	filesBuf := getEnvInt("SCANNER_FILES_BUF", 1000)
	errBuf := getEnvInt("SCANNER_ERR_BUF", 100)
	conc := getEnvInt("SCANNER_CONCURRENCY", max(8, runtime.NumCPU()*4))
	if conc < 1 {
		conc = 1
	}
	tickMs := getEnvInt("SCANNER_TICK_MS", 500)
	if tickMs < 10 {
		tickMs = 10
	}
	return &Scanner{
		progressCh:   make(chan Progress, progressBuf),
		filesCh:      make(chan FileInfo, filesBuf),
		errCh:        make(chan error, errBuf),
		startTime:    time.Now(),
		concurrency:  conc,
		tickInterval: time.Duration(tickMs) * time.Millisecond,
	}
}

// ScanDirectory starts scanning a directory with parallel workers
func (s *Scanner) ScanDirectory(path string) {
	go func() {
		defer s.Close()

		// 시작 시간 초기화
		s.startTime = time.Now()

		// 진행상황 모니터링
		done := make(chan bool)
		go s.monitorProgress(done)

		// 병렬 디렉터리 탐색을 위한 워커 풀
		dirBuf := getEnvInt("SCANNER_DIRBUF", 1024)
		if dirBuf < 1 {
			dirBuf = 1
		}
		dirCh := make(chan string, dirBuf)

		// 디렉터리 대기열 카운팅용 WaitGroup
		var dirWG sync.WaitGroup
		dirWG.Add(1) // 루트 디렉터리

		// 모든 디렉터리 처리가 끝나면 안전하게 채널 종료
		go func() {
			dirWG.Wait()
			close(dirCh)
		}()

		// 워커 시작
		workerCount := s.concurrency
		var workers sync.WaitGroup
		workers.Add(workerCount)
		// 명시적 크기 수집 옵션: 기본 false (스캔 가속)
		collectSize := getEnvBool("SCANNER_COLLECT_SIZE", false)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer workers.Done()
				for dir := range dirCh {
					if atomic.LoadInt32(&s.canceled) == 1 {
						// 소비만 하고 스킵
						dirWG.Done()
						continue
					}
					entries, err := os.ReadDir(dir)
					if err != nil {
						select {
						case s.errCh <- err:
						default:
						}
						dirWG.Done()
						continue
					}

					for _, entry := range entries {
						if atomic.LoadInt32(&s.canceled) == 1 {
							break
						}
						entryPath := filepath.Join(dir, entry.Name())
						if entry.IsDir() {
							// 하위 디렉터리 큐잉
							dirWG.Add(1)
							dirCh <- entryPath
							continue
						}
						// 파일 처리 (필요 시에만 크기 조회)
						var size int64
						if collectSize {
							info, err := os.Lstat(entryPath)
							if err != nil {
								select {
								case s.errCh <- err:
								default:
								}
								continue
							}
							size = info.Size()
						}
						fileInfo := FileInfo{Path: entryPath, Size: size, Dir: dir}

						// 진행 상태 O(1) 누적 (atomic)
						atomic.AddInt64(&s.totalFiles, 1)
						if collectSize {
							atomic.AddInt64(&s.totalSize, fileInfo.Size)
						}

						// 파일 정보 전송
						if atomic.LoadInt32(&s.canceled) == 0 {
							s.filesCh <- fileInfo
						}
					}

					// 이 디렉터리 처리 완료
					dirWG.Done()
				}
			}()
		}

		// 루트 디렉터리 투입
		dirCh <- path

		// 워커 종료 대기
		workers.Wait()

		// 스캔 완료 후 모니터링 중단
		close(done)

		// 최종 진행 상황 전송
		s.sendFinalProgress()
	}()
}

// Cancel signals the scanner to stop as soon as possible
func (s *Scanner) Cancel() { atomic.StoreInt32(&s.canceled, 1) }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// monitorProgress monitors and reports scan progress
func (s *Scanner) monitorProgress(done <-chan bool) {
	interval := s.tickInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			var progress Progress
			progress.TotalFiles = atomic.LoadInt64(&s.totalFiles)
			progress.TotalSize = atomic.LoadInt64(&s.totalSize)
			elapsed := time.Since(s.startTime)
			progress.ElapsedTime = elapsed
			if elapsed.Seconds() > 0 {
				progress.Speed = float64(progress.TotalFiles) / elapsed.Seconds()
			}

			// 진행 상황 전송
			select {
			case s.progressCh <- progress:
			default:
			}
		}
	}
}

// sendFinalProgress sends the final progress update
func (s *Scanner) sendFinalProgress() {
	var progress Progress
	progress.TotalFiles = atomic.LoadInt64(&s.totalFiles)
	progress.TotalSize = atomic.LoadInt64(&s.totalSize)
	elapsed := time.Since(s.startTime)
	progress.ElapsedTime = elapsed
	if elapsed.Seconds() > 0 {
		progress.Speed = float64(progress.TotalFiles) / elapsed.Seconds()
	}

	select {
	case s.progressCh <- progress:
	default:
	}
}

// Close closes all channels
func (s *Scanner) Close() {
	close(s.progressCh)
	close(s.filesCh)
	close(s.errCh)
}

// getEnvInt returns integer environment variable or default if not present/invalid
func getEnvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// getEnvBool returns boolean environment variable or default if not present/invalid
func getEnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		switch v {
		case "1", "true", "TRUE", "True", "yes", "Y", "y":
			return true
		case "0", "false", "FALSE", "False", "no", "N", "n":
			return false
		}
	}
	return def
}

// Progress returns the progress channel
func (s *Scanner) Progress() <-chan Progress {
	return s.progressCh
}

// Files returns the files channel
func (s *Scanner) Files() <-chan FileInfo {
	return s.filesCh
}

// Errors returns the error channel
func (s *Scanner) Errors() <-chan error {
	return s.errCh
}
