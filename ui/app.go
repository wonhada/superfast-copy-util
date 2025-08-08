package ui

import (
	"bufio"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	runewidth "github.com/mattn/go-runewidth"
)

// RunTUI TUI 애플리케이션 실행 (원본 _uiorigin 동작과 동일)
func RunTUI() error {
	// 터미널 환경이 아닌 경우 안내 후 종료 (더블클릭 실행 대비)
	if !(isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())) {
		fmt.Println("TUI는 터미널에서만 표시됩니다. PowerShell 또는 Windows Terminal에서 실행해주세요.")
		fmt.Print("계속하려면 Enter 키를 누르세요...")
		_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')
		return nil
	}

	// Windows/한글 환경에서 이모지/동아시아 폭 보정 (경계선 깨짐 방지)
	runewidth.EastAsianWidth = true
	runewidth.DefaultCondition.EastAsianWidth = true

	p := tea.NewProgram(
		NewModel(),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
