package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel creates a Model with mock tunnel data for testing.
func newTestModel(t *testing.T, tunnelCount int) Model {
	t.Helper()

	tunnels := make([]TunnelDisplayEntry, 0, tunnelCount)
	for idx := range tunnelCount {
		tunnels = append(tunnels, TunnelDisplayEntry{
			ID:        idx + 1,
			Name:      "tunnel-" + string(rune('a'+idx)),
			Port:      3000 + idx,
			Subdomain: "sub-" + string(rune('a'+idx)),
			PublicURL: "https://sub-" + string(rune('a'+idx)) + ".justtunnel.io",
			State:     StateConnected,
			Uptime:    time.Duration(idx+1) * time.Minute,
			Requests:  int64(idx * 10),
		})
	}

	return NewModel(tunnels, PlanInfo{Name: "Pro", MaxTunnels: 5})
}

func TestKeyNavigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tunnelCount   int
		keys          []tea.KeyType
		wantSelected  int
	}{
		{
			name:         "down arrow moves selection from 0 to 1",
			tunnelCount:  3,
			keys:         []tea.KeyType{tea.KeyDown},
			wantSelected: 1,
		},
		{
			name:         "up arrow at index 0 stays at 0",
			tunnelCount:  3,
			keys:         []tea.KeyType{tea.KeyUp},
			wantSelected: 0,
		},
		{
			name:         "down then up returns to 0",
			tunnelCount:  3,
			keys:         []tea.KeyType{tea.KeyDown, tea.KeyUp},
			wantSelected: 0,
		},
		{
			name:         "down arrow at last index stays at last",
			tunnelCount:  3,
			keys:         []tea.KeyType{tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown},
			wantSelected: 2,
		},
		{
			name:         "multiple downs with 1 tunnel stays at 0",
			tunnelCount:  1,
			keys:         []tea.KeyType{tea.KeyDown, tea.KeyDown},
			wantSelected: 0,
		},
		{
			name:         "empty tunnel list stays at 0",
			tunnelCount:  0,
			keys:         []tea.KeyType{tea.KeyDown, tea.KeyUp},
			wantSelected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel(t, tt.tunnelCount)

			for _, key := range tt.keys {
				msg := tea.KeyMsg{Type: key}
				updatedModel, _ := model.Update(msg)
				model = updatedModel.(Model)
			}

			if model.selectedIndex != tt.wantSelected {
				t.Errorf("selectedIndex = %d, want %d", model.selectedIndex, tt.wantSelected)
			}
		})
	}
}

func TestViewTransitions(t *testing.T) {
	t.Parallel()

	t.Run("enter switches from list to detail view", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		if model.viewState != viewList {
			t.Fatalf("initial viewState = %d, want viewList (%d)", model.viewState, viewList)
		}

		msg := tea.KeyMsg{Type: tea.KeyEnter}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		if model.viewState != viewDetail {
			t.Errorf("viewState after Enter = %d, want viewDetail (%d)", model.viewState, viewDetail)
		}
	})

	t.Run("esc switches from detail to list view", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		// Navigate down then enter detail view
		downMsg := tea.KeyMsg{Type: tea.KeyDown}
		updatedModel, _ := model.Update(downMsg)
		model = updatedModel.(Model)

		enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
		updatedModel, _ = model.Update(enterMsg)
		model = updatedModel.(Model)

		if model.viewState != viewDetail {
			t.Fatalf("viewState = %d, want viewDetail", model.viewState)
		}

		// Esc should return to list
		escMsg := tea.KeyMsg{Type: tea.KeyEscape}
		updatedModel, _ = model.Update(escMsg)
		model = updatedModel.(Model)

		if model.viewState != viewList {
			t.Errorf("viewState after Esc = %d, want viewList (%d)", model.viewState, viewList)
		}
	})

	t.Run("esc preserves selected index when returning to list", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		// Select index 2
		downMsg := tea.KeyMsg{Type: tea.KeyDown}
		updatedModel, _ := model.Update(downMsg)
		model = updatedModel.(Model)
		updatedModel, _ = model.Update(downMsg)
		model = updatedModel.(Model)

		// Enter detail
		enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
		updatedModel, _ = model.Update(enterMsg)
		model = updatedModel.(Model)

		// Return to list
		escMsg := tea.KeyMsg{Type: tea.KeyEscape}
		updatedModel, _ = model.Update(escMsg)
		model = updatedModel.(Model)

		if model.selectedIndex != 2 {
			t.Errorf("selectedIndex after Esc = %d, want 2", model.selectedIndex)
		}
	})

	t.Run("enter on empty list stays in list view", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 0)

		msg := tea.KeyMsg{Type: tea.KeyEnter}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		if model.viewState != viewList {
			t.Errorf("viewState = %d, want viewList (%d) on empty list", model.viewState, viewList)
		}
	})

	t.Run("esc in list view is no-op", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		msg := tea.KeyMsg{Type: tea.KeyEscape}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)

		if model.viewState != viewList {
			t.Errorf("viewState = %d, want viewList (%d)", model.viewState, viewList)
		}
	})
}

func TestTickMessage(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)
	initialTick := model.tickCount

	tickMsg := TickMsg(time.Now())
	updatedModel, _ := model.Update(tickMsg)
	model = updatedModel.(Model)

	if model.tickCount != initialTick+1 {
		t.Errorf("tickCount = %d, want %d", model.tickCount, initialTick+1)
	}
}

func TestEmptyListRendering(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 0)
	output := model.View()

	if output == "" {
		t.Fatal("View() returned empty string for empty tunnel list")
	}

	// Should contain the header
	if !strings.Contains(output, "justtunnel") {
		t.Error("View() output missing 'justtunnel' header")
	}

	// Should contain the plan info
	if !strings.Contains(output, "0/5") {
		t.Error("View() output missing plan quota '0/5'")
	}

	// Should contain the input bar prompt
	if !strings.Contains(output, ">") {
		t.Error("View() output missing '>' input prompt")
	}
}

func TestListViewRendering(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)
	output := model.View()

	// Should contain header
	if !strings.Contains(output, "justtunnel") {
		t.Error("View() missing 'justtunnel' header")
	}

	// Should contain plan quota
	if !strings.Contains(output, "2/5") {
		t.Error("View() missing plan quota '2/5'")
	}

	// Should contain plan name
	if !strings.Contains(output, "Pro") {
		t.Error("View() missing plan name 'Pro'")
	}

	// Should contain tunnel ports
	if !strings.Contains(output, "3000") {
		t.Error("View() missing port 3000")
	}
	if !strings.Contains(output, "3001") {
		t.Error("View() missing port 3001")
	}

	// Should contain status labels for accessibility
	if !strings.Contains(output, "[connected]") {
		t.Error("View() missing [connected] status label")
	}

	// First row should have the selection marker
	if !strings.Contains(output, ">") {
		t.Error("View() missing '>' selection marker")
	}
}

func TestDetailViewRendering(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)

	// Enter detail view for the first tunnel
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, _ := model.Update(enterMsg)
	model = updatedModel.(Model)

	output := model.View()

	// Detail view should show the tunnel's public URL
	if !strings.Contains(output, "https://sub-a.justtunnel.io") {
		t.Error("Detail view missing public URL")
	}

	// Detail view should show the local target
	if !strings.Contains(output, "3000") {
		t.Error("Detail view missing local port")
	}

	// Detail view should show the subdomain
	if !strings.Contains(output, "sub-a") {
		t.Error("Detail view missing subdomain")
	}

	// Detail view should show the status
	if !strings.Contains(output, "[connected]") {
		t.Error("Detail view missing status label")
	}
}

func TestCtrlCQuitsProgram(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd := model.Update(msg)

	// cmd should be a quit command
	if cmd == nil {
		t.Fatal("Ctrl+C should produce a quit command")
	}

	// Execute the command and check it produces a tea.QuitMsg
	quitMsg := cmd()
	if _, isQuit := quitMsg.(tea.QuitMsg); !isQuit {
		t.Errorf("Ctrl+C command produced %T, want tea.QuitMsg", quitMsg)
	}
}

func TestWindowSizeMsg(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)

	sizeMsg := tea.WindowSizeMsg{Width: 120, Height: 40}
	updatedModel, _ := model.Update(sizeMsg)
	model = updatedModel.(Model)

	if model.width != 120 {
		t.Errorf("width = %d, want 120", model.width)
	}
	if model.height != 40 {
		t.Errorf("height = %d, want 40", model.height)
	}
}

func TestTunnelStateStyles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state TunnelState
		label string
	}{
		{"connecting", StateConnecting, "[connecting]"},
		{"connected", StateConnected, "[connected]"},
		{"reconnecting", StateReconnecting, "[reconnecting]"},
		{"disconnected", StateDisconnected, "[disconnected]"},
		{"error", StateError, "[error]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stateLabel(tt.state)
			if got != tt.label {
				t.Errorf("stateLabel(%v) = %q, want %q", tt.state, got, tt.label)
			}
		})
	}
}

func TestTunnelStateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state TunnelState
		want  string
	}{
		{"connecting", StateConnecting, "connecting"},
		{"connected", StateConnected, "connected"},
		{"reconnecting", StateReconnecting, "reconnecting"},
		{"disconnected", StateDisconnected, "disconnected"},
		{"error", StateError, "error"},
		{"unknown", TunnelState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.state.String()
			if got != tt.want {
				t.Errorf("TunnelState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestErrorMessageDisplay(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)
	model.errorMessage = "Port 3000 is already being tunneled"

	output := model.View()

	if !strings.Contains(output, "Port 3000 is already being tunneled") {
		t.Error("View() should display the error message")
	}
}

func TestListViewSelectionMarker(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 3)

	// Move selection to index 1
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	updatedModel, _ := model.Update(downMsg)
	model = updatedModel.(Model)

	output := model.View()
	lines := strings.Split(output, "\n")

	// Find lines containing tunnel ports and check marker placement
	foundSelected := false
	for _, line := range lines {
		if strings.Contains(line, "3001") && strings.Contains(line, ">") {
			foundSelected = true
		}
	}

	if !foundSelected {
		t.Error("Selection marker '>' should be on the line for port 3001 (index 1)")
	}
}

func TestDetailViewArrowKeysIgnored(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 3)

	// Go to detail view for tunnel at index 0
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, _ := model.Update(enterMsg)
	model = updatedModel.(Model)

	selectedBefore := model.selectedIndex

	// Press down arrow in detail view - should not change selection
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	updatedModel, _ = model.Update(downMsg)
	model = updatedModel.(Model)

	if model.selectedIndex != selectedBefore {
		t.Errorf("selectedIndex changed in detail view: got %d, want %d", model.selectedIndex, selectedBefore)
	}

	// Should still be in detail view
	if model.viewState != viewDetail {
		t.Errorf("viewState changed after arrow key in detail view: got %d, want %d", model.viewState, viewDetail)
	}
}
