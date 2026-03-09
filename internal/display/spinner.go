package display

import (
	"fmt"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Spinner struct {
	mu      sync.Mutex
	message string
	stop    chan struct{}
	done    chan struct{}
}

func NewSpinner(message string) *Spinner {
	return &Spinner{
		message: message,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (spin *Spinner) Start() {
	if !IsTerminal() {
		fmt.Fprintf(output, "  %s\n", spin.message)
		close(spin.done)
		return
	}

	go func() {
		defer close(spin.done)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		frame := 0
		for {
			select {
			case <-spin.stop:
				fmt.Fprintf(output, "\r\033[K")
				return
			case <-ticker.C:
				spin.mu.Lock()
				msg := spin.message
				spin.mu.Unlock()
				fmt.Fprintf(output, "\r  %s %s", spinnerFrames[frame%len(spinnerFrames)], msg)
				frame++
			}
		}
	}()
}

func (spin *Spinner) Update(message string) {
	spin.mu.Lock()
	spin.message = message
	spin.mu.Unlock()
}

func (spin *Spinner) Stop() {
	select {
	case <-spin.stop:
	default:
		close(spin.stop)
	}
	<-spin.done
}

func (spin *Spinner) StopWithMessage(message string) {
	spin.Stop()
	fmt.Fprintf(output, "  %s\n", message)
}
