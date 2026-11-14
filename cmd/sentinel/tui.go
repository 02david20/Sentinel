package main

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/02david20/Sentinel/internal/tui"
)

// isTerminal is true when both input and output are a terminal, so `sentinel` alone opens the view for
// a person and still prints help into a pipe or a script.
func isTerminal() bool {
	in, err1 := os.Stdin.Stat()
	out, err2 := os.Stdout.Stat()
	return err1 == nil && err2 == nil && in.Mode()&os.ModeCharDevice != 0 && out.Mode()&os.ModeCharDevice != 0
}

func cmdTUI() error {
	if !isTerminal() {
		return errors.New("the interactive view needs a terminal; use `sentinel status` in scripts")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	self, _ := os.Executable()
	be := &tui.StoreBackend{Store: st, Self: self, Timer: timerState}
	p := tea.NewProgram(tui.New(be), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("terminal view: %w", err)
	}
	return nil
}
