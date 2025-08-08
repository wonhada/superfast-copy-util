package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"superfast-copy-util/copier"
	"superfast-copy-util/scanner"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Model struct {
	width       int
	height      int
	leftPanel   *PanelModel
	rightPanel  *PanelModel
	activePanel int
	status      string
	drives      []DriveInfo

	// copy workflow state
	isScanning bool
	isCopying  bool
	sourcePath string
	targetPath string
	files      []string
	totalSize  int64
	scanProg   scanner.Progress
	copyProg   copier.CopyProgress
	lastErr    string
	// runtime handles (not serialized)
	scn *scanner.Scanner
	cpr *copier.Copier

	// modal / input lock
	modalActive  bool
	modalKind    string // "confirm" or ""
	dialogCursor int    // 0: 예, 1: 아니오
	page         int    // 0: 홈, 1: 확인, 2: 진행
	fastMode     bool
}

// tea messages and cmds for scanning/copying
type scanProgressMsg struct{ p scanner.Progress }
type scanFileMsg struct {
	path string
	size int64
}
type scanErrMsg struct{ err string }
type scanDoneMsg struct{}

type copyProgressMsg struct{ p copier.CopyProgress }
type copyErrMsg struct{ err string }
type copyDoneMsg struct{}
type fastDoneMsg struct{ err error }

func watchScanProgressCmd(ch <-chan scanner.Progress) tea.Cmd {
	return func() tea.Msg {
		if p, ok := <-ch; ok {
			return scanProgressMsg{p: p}
		}
		return scanDoneMsg{}
	}
}
func watchScanFilesCmd(ch <-chan scanner.FileInfo) tea.Cmd {
	return func() tea.Msg {
		if f, ok := <-ch; ok {
			return scanFileMsg{path: f.Path, size: f.Size}
		}
		return scanDoneMsg{}
	}
}
func watchScanErrorsCmd(ch <-chan error) tea.Cmd {
	return func() tea.Msg {
		if err, ok := <-ch; ok {
			return scanErrMsg{err: err.Error()}
		}
		return nil
	}
}
func watchCopyProgressCmd(ch <-chan copier.CopyProgress) tea.Cmd {
	return func() tea.Msg {
		if p, ok := <-ch; ok {
			return copyProgressMsg{p: p}
		}
		return copyDoneMsg{}
	}
}
func watchCopyErrorsCmd(ch <-chan error) tea.Cmd {
	return func() tea.Msg {
		if err, ok := <-ch; ok {
			return copyErrMsg{err: err.Error()}
		}
		return nil
	}
}

type driveListLoaded []DriveInfo

type DriveInfo struct {
	Path      string
	Label     string
	Type      string
	Icon      string
	Available bool
}

func NewModel() Model {
	drives := []DriveInfo{}
	leftPanel := NewPanelModel("📁 소스 폴더", true, drives)
	rightPanel := NewPanelModel("📂 대상 폴더", false, drives)
	return Model{
		width:       100,
		height:      30,
		leftPanel:   &leftPanel,
		rightPanel:  &rightPanel,
		activePanel: 0,
		status:      "준비 - Space: 복사, Tab: 패널 전환, Enter: 선택, q: 종료",
		drives:      drives,
	}
}

// 간단한 확인 다이얼로그 (텍스트 입력 기반)
func confirmCreateSubfolder() bool {
	// 기본 보수적 선택: '예' 또는 '아니오'를 키로 받기보다, 안내만 하고 Enter로 '예', Esc로 '아니오' 등 키맵을 만들 수도 있으나
	// 여기서는 최소 구현: 환경변수로 강제 또는 기본 '예'
	// 실제 TUI 팝업 입력 처리는 추후 확장에서 구현
	return true
}

func (m Model) Init() tea.Cmd {
	return func() tea.Msg { return driveListLoaded(loadDrives()) }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		panelWidth := (msg.Width - 6) / 2
		panelHeight := msg.Height - 6
		m.leftPanel.width = panelWidth
		m.leftPanel.height = panelHeight
		m.rightPanel.width = panelWidth
		m.rightPanel.height = panelHeight
	case driveListLoaded:
		if len(m.drives) == 0 {
			m.drives = []DriveInfo(msg)
			m.leftPanel.drives = m.drives
			m.rightPanel.drives = m.drives
		}
	case tea.KeyMsg:
		// 진행 페이지(2): 중지 키만 처리
		if m.page == 2 {
			switch msg.String() {
			case "ctrl+x":
				if m.isScanning && m.scn != nil {
					m.scn.Cancel()
				}
				if m.isCopying && m.cpr != nil {
					m.cpr.Cancel()
				}
				m.isScanning = false
				m.isCopying = false
				m.modalActive = false
				m.page = 0
				m.status = "중지됨"
				return m, nil
			default:
				return m, nil
			}
		}
		// 그 외 페이지에서 스캔/복사 중이면 입력 무시
		if m.isScanning || m.isCopying {
			return m, nil
		}
		// 확인 페이지 키 처리
		if m.page == 1 || (m.modalActive && m.modalKind == "confirm") {
			switch msg.String() {
			case "left", "h":
				m.dialogCursor = 0
				return m, nil
			case "right", "l":
				m.dialogCursor = 1
				return m, nil
			case "enter", "y":
				m.modalActive = false
				m.page = 2
				m.fastMode = true
				src := m.sourcePath
				dst := m.targetPath
				createSub := (m.dialogCursor == 0)
				return m, fastCopyCmd(src, dst, createSub)
			case "n", "esc", "q":
				m.modalActive = false
				m.page = 0
				m.status = "취소됨"
				return m, nil
			default:
				return m, nil
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.activePanel = (m.activePanel + 1) % 2
			m.leftPanel.focused = (m.activePanel == 0)
			m.rightPanel.focused = (m.activePanel == 1)
		case " ":
			// Space: 다음 페이지(확인)로 이동
			if !m.isScanning && !m.isCopying {
				m.sourcePath = m.leftPanel.GetCurrentPath()
				m.targetPath = m.rightPanel.GetCurrentPath()
				if m.sourcePath != "" && m.targetPath != "" && m.sourcePath != m.targetPath {
					m.modalActive = true
					m.modalKind = "confirm"
					m.page = 1
					return m, nil
				}
			}
		default:
			if m.activePanel == 0 {
				newPanel, cmd := m.leftPanel.Update(msg)
				m.leftPanel = &newPanel
				return m, cmd
			}
			newPanel, cmd := m.rightPanel.Update(msg)
			m.rightPanel = &newPanel
			return m, cmd
		}
	case scanProgressMsg:
		m.scanProg = msg.p
		m.status = fmt.Sprintf("스캔 중: %d개 (%.1f 파일/초)", m.scanProg.TotalFiles, m.scanProg.Speed)
		return m, watchScanProgressCmd(m.scn.Progress())
	case scanFileMsg:
		m.files = append(m.files, msg.path)
		m.totalSize += msg.size
		return m, watchScanFilesCmd(m.scn.Files())
	case scanDoneMsg:
		// begin copy phase
		m.isScanning = false
		m.isCopying = true
		m.status = "복사 준비 중"
		m.cpr = copier.NewCopier(m.sourcePath, m.targetPath, false)
		m.cpr.SetTotal(int64(len(m.files)), m.totalSize)
		m.cpr.CopyFilesParallel(m.files)
		return m, tea.Batch(
			watchCopyProgressCmd(m.cpr.Progress()),
			watchCopyErrorsCmd(m.cpr.Errors()),
		)
	case scanErrMsg:
		m.lastErr = msg.err
		m.status = "스캔 오류 발생"
		return m, nil
	case copyProgressMsg:
		m.copyProg = msg.p
		var percent float64
		if m.copyProg.TotalFiles > 0 {
			percent = float64(m.copyProg.CompletedFiles) * 100 / float64(m.copyProg.TotalFiles)
		}
		m.status = fmt.Sprintf("복사 중: %d/%d (%.1f%%)", m.copyProg.CompletedFiles, m.copyProg.TotalFiles, percent)
		return m, watchCopyProgressCmd(m.cpr.Progress())
	case copyDoneMsg:
		m.isCopying = false
		m.status = "복사 완료"
		return m, nil
	case copyErrMsg:
		m.lastErr = msg.err
		m.status = "복사 오류 발생"
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "로딩 중..."
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Align(lipgloss.Center).Width(m.width)
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Align(lipgloss.Center).Width(m.width)
	title := titleStyle.Render("🚀 SuperFast File Copier")

	// Page 1: 확인 페이지(전용 화면)
	if m.page == 1 {
		yesStyle := lipgloss.NewStyle().Padding(0, 2).Background(lipgloss.Color("240")).Foreground(lipgloss.Color("15"))
		active := yesStyle.Copy().Background(lipgloss.Color("205")).Bold(true)
		yes := yesStyle.Render("예")
		no := yesStyle.Render("아니오")
		if m.dialogCursor == 0 {
			yes = active.Render("예")
		} else {
			no = active.Render("아니오")
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("205")).Padding(1, 2).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("15")).Render(
			lipgloss.JoinVertical(lipgloss.Center, "📁 폴더 생성", "", "폴더를 생성하시겠습니까?", "", lipgloss.JoinHorizontal(lipgloss.Center, yes, no), "", lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("← → : 선택, Enter: 확인, Esc: 취소")),
		)
		body := lipgloss.JoinVertical(lipgloss.Left, title, "", lipgloss.Place(m.width, m.height-2, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "), lipgloss.WithWhitespaceForeground(lipgloss.Color("0"))))
		return body
	}

	// Page 2: 진행 페이지(전용 화면)
	if m.page == 2 {
		var bodyBuilder strings.Builder
		if m.fastMode {
			fmt.Fprintf(&bodyBuilder, "고속 모드 실행 중...\n별도 창에서 CLI로 복사를 수행합니다.")
		} else if m.isScanning {
			fmt.Fprintf(&bodyBuilder, "스캔 중\n파일: %d개\n속도: %.1f개/초", m.scanProg.TotalFiles, m.scanProg.Speed)
		} else {
			var percent float64
			if m.copyProg.TotalFiles > 0 {
				percent = float64(m.copyProg.CompletedFiles) * 100 / float64(m.copyProg.TotalFiles)
			}
			fmt.Fprintf(&bodyBuilder, "복사 중\n%d/%d (%.1f%%)\nCtrl+X: 중지", m.copyProg.CompletedFiles, m.copyProg.TotalFiles, percent)
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("205")).Padding(1, 2).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("15")).Render(bodyBuilder.String())
		body := lipgloss.JoinVertical(lipgloss.Left, title, "", lipgloss.Place(m.width, m.height-2, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "), lipgloss.WithWhitespaceForeground(lipgloss.Color("0"))))
		return body
	}

	// Page 0: 홈(기존 패널 레이아웃)
	panelStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1).Width((m.width - 6) / 2).Height(m.height - 6)
	activePanelStyle := panelStyle.Copy().BorderForeground(lipgloss.Color("205"))
	leftPanelView := m.leftPanel.View()
	rightPanelView := m.rightPanel.View()
	if m.activePanel == 0 {
		leftPanelView = activePanelStyle.Render(leftPanelView)
		rightPanelView = panelStyle.Render(rightPanelView)
	} else {
		leftPanelView = panelStyle.Render(leftPanelView)
		rightPanelView = activePanelStyle.Render(rightPanelView)
	}
	centerLabel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("=>\n복사")
	centerCol := lipgloss.Place(9, m.height-6, lipgloss.Center, lipgloss.Center, centerLabel)
	mainPanel := lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, centerCol, rightPanelView)
	status := statusStyle.Render(m.status)
	return lipgloss.JoinVertical(lipgloss.Left, title, "", mainPanel, "", status)
}

// startScanCopy: 모달 확정 후 스캔→복사 시작
func startScanCopy(m Model, createSub bool) (Model, tea.Cmd) {
	if createSub {
		base := filepath.Base(m.sourcePath)
		m.targetPath = filepath.Join(m.targetPath, base)
		_ = os.MkdirAll(m.targetPath, 0755)
	}
	m.isScanning = true
	m.status = "진행 중"
	m.files = nil
	m.totalSize = 0
	m.scn = scanner.NewScanner()
	m.scn.ScanDirectory(m.sourcePath)
	return m, tea.Batch(
		watchScanProgressCmd(m.scn.Progress()),
		watchScanFilesCmd(m.scn.Files()),
		watchScanErrorsCmd(m.scn.Errors()),
	)
}

// fastCopyCmd runs scan then copy without UI channel round-trips
func fastCopyCmd(sourcePath, targetPath string, createSub bool) tea.Cmd {
	return func() tea.Msg {
		exe, _ := os.Executable()
		if createSub {
			base := filepath.Base(sourcePath)
			targetPath = filepath.Join(targetPath, base)
			_ = os.MkdirAll(targetPath, 0755)
		}
		// 새 터미널 창에서 CLI 실행 후, 현재 프로세스 종료
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			// start "" <exe> --cli src dst
			args := append([]string{"/c", "start", "", exe, "--cli", sourcePath, targetPath})
			cmd = exec.Command("cmd", args...)
		} else if runtime.GOOS == "darwin" {
			// macOS: 기본 터미널에서 실행 시도
			script := "osascript"
			// open Terminal and run command
			cmd = exec.Command(script, "-e", "tell application \"Terminal\" to do script \""+exe+" --cli '"+sourcePath+"' '"+targetPath+"'\"")
		} else {
			// Linux: 백그라운드로 실행 시도 (터미널 매핑 불확실)
			cmd = exec.Command(exe, "--cli", sourcePath, targetPath)
		}
		_ = cmd.Start()
		os.Exit(0)
		return fastDoneMsg{err: nil}
	}
}

type PanelModel struct {
	title               string
	isSource            bool
	focused             bool
	width               int
	height              int
	drives              []DriveInfo
	currentDrive        int
	currentPath         string
	selectedPath        string
	folders             []string
	cursor              int
	scrollOffset        int
	viewMode            int
	startedFromShortcut bool
}

func NewPanelModel(title string, isSource bool, drives []DriveInfo) PanelModel {
	return PanelModel{title: title, isSource: isSource, focused: isSource, drives: drives, currentDrive: -1, viewMode: 0, folders: []string{}, cursor: 0, scrollOffset: 0}
}

func (p PanelModel) Update(msg tea.Msg) (PanelModel, tea.Cmd) {
	if !p.focused {
		return p, nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
			if p.viewMode == 1 {
				visible := p.visibleFolderRows()
				if p.cursor < p.scrollOffset {
					p.scrollOffset = p.cursor
				} else if p.cursor >= p.scrollOffset+visible {
					p.scrollOffset = p.cursor - visible + 1
				}
			}
		case "down", "j":
			maxItems := 0
			if p.viewMode == 0 {
				maxItems = len(p.drives)
			} else {
				maxItems = len(p.getFolderItems())
			}
			if p.cursor < maxItems-1 {
				p.cursor++
			}
			if p.viewMode == 1 {
				visible := p.visibleFolderRows()
				if p.cursor < p.scrollOffset {
					p.scrollOffset = p.cursor
				} else if p.cursor >= p.scrollOffset+visible {
					p.scrollOffset = p.cursor - visible + 1
				}
			}
		case "enter":
			if p.viewMode == 0 {
				if p.cursor < len(p.drives) {
					drive := p.drives[p.cursor]
					if drive.Available {
						p.currentDrive = p.cursor
						p.currentPath = drive.Path
						p.startedFromShortcut = (drive.Type == "바로가기")
						p.loadFolders()
						p.viewMode = 1
						p.cursor = 0
					}
				}
			} else {
				items := p.getFolderItems()
				if p.cursor < len(items) {
					selectedFolder := items[p.cursor]
					if selectedFolder == ".." {
						if p.startedFromShortcut {
							if p.currentDrive >= 0 && p.currentDrive < len(p.drives) {
								originalPath := p.drives[p.currentDrive].Path
								if p.currentPath == originalPath {
									p.viewMode = 0
									p.cursor = p.currentDrive
									p.startedFromShortcut = false
									return p, nil
								}
							}
						}
						parent := filepath.Dir(p.currentPath)
						if parent != p.currentPath {
							p.currentPath = parent
							p.loadFolders()
							p.cursor = 0
						} else if p.startedFromShortcut {
							p.viewMode = 0
							p.cursor = p.currentDrive
							p.startedFromShortcut = false
						}
					} else {
						newPath := filepath.Join(p.currentPath, selectedFolder)
						if info, err := os.Stat(newPath); err == nil && info.IsDir() {
							p.currentPath = newPath
							p.selectedPath = newPath
							p.loadFolders()
							p.cursor = 0
							p.scrollOffset = 0
						}
					}
				}
			}
		case "backspace", "h":
			if p.viewMode == 1 {
				p.viewMode = 0
				p.cursor = p.currentDrive
				p.scrollOffset = 0
			}
		}
	}
	return p, nil
}

func (p PanelModel) View() string {
	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true)
	if p.focused {
		titleStyle = titleStyle.Foreground(lipgloss.Color("205"))
	}
	b.WriteString(titleStyle.Render(p.title))
	b.WriteString("\n\n")
	if p.viewMode == 0 {
		b.WriteString("위치 선택:\n")
		var shortcuts, drives []DriveInfo
		for _, d := range p.drives {
			if d.Type == "바로가기" {
				shortcuts = append(shortcuts, d)
			} else {
				drives = append(drives, d)
			}
		}
		if len(shortcuts) > 0 {
			sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Bold(true)
			b.WriteString(sectionStyle.Render("📌 바로가기"))
			b.WriteString("\n")
			for i, sc := range shortcuts {
				cursor := " "
				if i == p.cursor && p.focused {
					cursor = ">"
				}
				style := lipgloss.NewStyle()
				if i == p.cursor && p.focused {
					style = style.Background(lipgloss.Color("205")).Foreground(lipgloss.Color("15"))
				} else if !sc.Available {
					style = style.Foreground(lipgloss.Color("241"))
				}
				b.WriteString(style.Render(fmt.Sprintf("%s %s", cursor, sc.Label)))
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(drives) > 0 {
			sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Bold(true)
			b.WriteString(sectionStyle.Render("💾 드라이브"))
			b.WriteString("\n")
			shortcutCount := len(shortcuts)
			for i, dv := range drives {
				cursor := " "
				if (i+shortcutCount) == p.cursor && p.focused {
					cursor = ">"
				}
				style := lipgloss.NewStyle()
				if (i+shortcutCount) == p.cursor && p.focused {
					style = style.Background(lipgloss.Color("205")).Foreground(lipgloss.Color("15"))
				} else if !dv.Available {
					style = style.Foreground(lipgloss.Color("241"))
				}
				b.WriteString(style.Render(fmt.Sprintf("%s %s %s", cursor, dv.Icon, dv.Label)))
				b.WriteString("\n")
			}
		}
	} else {
		b.WriteString(fmt.Sprintf("현재 위치: %s\n", p.currentPath))
		b.WriteString("폴더 선택:\n")
		items := p.getFolderItems()
		start, end := p.visibleFolderRange(len(items))
		for i := start; i < end; i++ {
			folder := items[i]
			cursor := " "
			if i == p.cursor && p.focused {
				cursor = ">"
			}
			style := lipgloss.NewStyle()
			if i == p.cursor && p.focused {
				style = style.Background(lipgloss.Color("205")).Foreground(lipgloss.Color("15"))
			}
			icon := "📁"
			if folder == ".." {
				icon = "⬆️"
			}
			b.WriteString(style.Render(fmt.Sprintf("%s %s %s", cursor, icon, folder)))
			b.WriteString("\n")
		}
		if end < len(items) {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("..."))
			b.WriteString("\n")
		}
	}
	if p.selectedPath != "" {
		b.WriteString("\n")
		sel := lipgloss.NewStyle().Foreground(lipgloss.Color("34")).Bold(true)
		b.WriteString(sel.Render("선택됨: " + p.selectedPath))
	}
	b.WriteString("\n\n")
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	if p.viewMode == 0 {
		b.WriteString(help.Render("Space: 복사 시작  |  Enter: 위치 선택"))
	} else {
		helpText := "Space: 복사 시작  |  Enter: 폴더 이동, '..' 선택: 상위 폴더, Backspace: 위치 목록"
		if p.startedFromShortcut {
			helpText = "Space: 복사 시작  |  Enter: 폴더 이동, '..' 선택: 첫 화면으로, Backspace: 위치 목록"
		}
		b.WriteString(help.Render(helpText))
	}
	return b.String()
}

func (p *PanelModel) loadFolders() {
	p.folders = []string{}
	done := make(chan struct{})
	go func() {
		entries, err := os.ReadDir(p.currentPath)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					p.folders = append(p.folders, e.Name())
				}
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}
}

func (p PanelModel) GetSelectedPath() string { return p.selectedPath }
func (p PanelModel) GetCurrentPath() string  { return p.currentPath }

func (p PanelModel) getFolderItems() []string {
	items := []string{}
	if p.currentPath != "/" && !strings.HasSuffix(p.currentPath, ":\\") || p.startedFromShortcut {
		items = append(items, "..")
	}
	items = append(items, p.folders...)
	return items
}

func (p PanelModel) visibleFolderRows() int {
	rows := p.height - 6
	if rows < 1 {
		rows = 1
	}
	return rows
}
func (p PanelModel) visibleFolderRange(total int) (int, int) {
	if p.viewMode != 1 {
		return 0, total
	}
	visible := p.visibleFolderRows()
	start := p.scrollOffset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + visible
	if end > total {
		end = total
	}
	return start, end
}

// helpers
func loadDrives() []DriveInfo {
	var drives []DriveInfo
	drives = append(drives, getSystemFolders()...)
	if runtime.GOOS == "windows" {
		type result struct{ info DriveInfo }
		results := make(chan result, 26)
		for drive := 'A'; drive <= 'Z'; drive++ {
			d := drive
			go func() {
				drivePath := string(d) + ":\\"
				if _, err := os.Stat(drivePath); err == nil {
					results <- result{info: DriveInfo{Path: drivePath, Label: getDriveLabel(drivePath), Type: getDriveType(drivePath), Icon: getDriveIcon(drivePath), Available: true}}
				}
			}()
		}
		timeout := time.After(300 * time.Millisecond)
	collect:
		for {
			select {
			case r := <-results:
				drives = append(drives, r.info)
				if len(drives) > 40 {
					break collect
				}
			case <-timeout:
				break collect
			}
		}
	} else {
		drives = append(drives, DriveInfo{Path: "/", Label: "/ (루트)", Type: "로컬", Icon: "🖥️", Available: true})
		if runtime.GOOS == "darwin" {
			if entries, err := os.ReadDir("/Volumes"); err == nil {
				for _, e := range entries {
					if e.IsDir() {
						v := "/Volumes/" + e.Name()
						drives = append(drives, DriveInfo{Path: v, Label: e.Name(), Type: "볼륨", Icon: "💾", Available: true})
					}
				}
			}
		}
	}
	return drives
}

func getSystemFolders() []DriveInfo {
	var folders []DriveInfo
	if homeDir, err := os.UserHomeDir(); err == nil {
		folders = append(folders, DriveInfo{Path: homeDir, Label: "🏠 홈 폴더", Type: "바로가기", Icon: "🏠", Available: true})
		if runtime.GOOS == "windows" {
			systemFolders := []struct{ name, path, icon string }{{"바탕화면", filepath.Join(homeDir, "Desktop"), "🖥️"}, {"문서", filepath.Join(homeDir, "Documents"), "📄"}, {"다운로드", filepath.Join(homeDir, "Downloads"), "⬇️"}, {"사진", filepath.Join(homeDir, "Pictures"), "🖼️"}, {"음악", filepath.Join(homeDir, "Music"), "🎵"}, {"동영상", filepath.Join(homeDir, "Videos"), "🎬"}}
			for _, f := range systemFolders {
				if _, err := os.Stat(f.path); err == nil {
					folders = append(folders, DriveInfo{Path: f.path, Label: f.icon + " " + f.name, Type: "바로가기", Icon: f.icon, Available: true})
				}
			}
		} else {
			systemFolders := []struct{ name, path, icon string }{{"바탕화면", filepath.Join(homeDir, "Desktop"), "🖥️"}, {"문서", filepath.Join(homeDir, "Documents"), "📄"}, {"다운로드", filepath.Join(homeDir, "Downloads"), "⬇️"}, {"사진", filepath.Join(homeDir, "Pictures"), "🖼️"}, {"음악", filepath.Join(homeDir, "Music"), "🎵"}, {"동영상", filepath.Join(homeDir, "Movies"), "🎬"}}
			if runtime.GOOS == "darwin" {
				systemFolders = append(systemFolders, []struct{ name, path, icon string }{{"응용 프로그램", "/Applications", "📱"}, {"유틸리티", "/Applications/Utilities", "🔧"}}...)
			}
			for _, f := range systemFolders {
				if _, err := os.Stat(f.path); err == nil {
					folders = append(folders, DriveInfo{Path: f.path, Label: f.icon + " " + f.name, Type: "바로가기", Icon: f.icon, Available: true})
				}
			}
		}
	}
	return folders
}

func getDriveLabel(drivePath string) string { return drivePath + " (" + getDriveType(drivePath) + ")" }
func getDriveType(drivePath string) string {
	if strings.HasPrefix(drivePath, "\\\\") {
		return "네트워크"
	}
	if runtime.GOOS == "windows" {
		drive := strings.ToUpper(drivePath[:1])
		switch drive {
		case "A", "B":
			return "플로피"
		case "C":
			return "로컬"
		default:
			return "이동식"
		}
	}
	return "로컬"
}
func getDriveIcon(drivePath string) string {
	switch getDriveType(drivePath) {
	case "네트워크":
		return "🌐"
	case "이동식":
		return "🔌"
	case "플로피":
		return "💾"
	default:
		return "🖥️"
	}
}

// terminal detection (simple replacement for isatty usage)
func isTerminal(fd uintptr) bool { return true }
