package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"sdl/sdl_db_backup/internal/backupapp"
)

type inspectDataMsg struct {
	data *backupapp.InspectRun
	err  error
}

type clipboardMsg struct {
	text string
	err  error
}

func inspectRunCmd(envPath, runID string) tea.Cmd {
	return func() tea.Msg {
		data, err := backupapp.InspectRunData(context.Background(), envPath, runID)
		return inspectDataMsg{data: data, err: err}
	}
}

// copyToClipboard attempts xclip, then xsel, then wl-copy.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		tools := [][]string{
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
			{"wl-copy"},
		}
		for _, args := range tools {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return clipboardMsg{text: text}
			}
		}
		return clipboardMsg{err: fmt.Errorf("no clipboard tool found (install xclip or xsel)")}
	}
}
