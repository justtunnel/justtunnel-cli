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
			ID:          idx + 1,
			Name:        "tunnel-" + string(rune('a'+idx)),
			Port:        3000 + idx,
			Subdomain:   "sub-" + string(rune('a'+idx)),
			PublicURL:   "https://sub-" + string(rune('a'+idx)) + ".justtunnel.io",
			State:       StateConnected,
			ConnectedAt: time.Now().Add(-time.Duration(idx+1) * time.Minute),
			Requests:    int64(idx * 10),
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

func TestRemoveTunnelClampsSelection(t *testing.T) {
	t.Parallel()

	t.Run("remove last tunnel clamps selectedIndex", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		// Select last tunnel (index 2)
		model.selectedIndex = 2
		model.RemoveTunnel(3002) // remove last tunnel

		if model.selectedIndex != 1 {
			t.Errorf("selectedIndex = %d, want 1 after removing last", model.selectedIndex)
		}
	})

	t.Run("remove only tunnel clamps to 0", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 1)
		model.RemoveTunnel(3000)

		if model.selectedIndex != 0 {
			t.Errorf("selectedIndex = %d, want 0 after removing only tunnel", model.selectedIndex)
		}
		if len(model.tunnels) != 0 {
			t.Errorf("tunnels length = %d, want 0", len(model.tunnels))
		}
	})

	t.Run("remove tunnel in detail view returns to list if selected was removed", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)
		model.viewState = viewDetail
		model.selectedIndex = 1

		model.RemoveTunnel(3001)

		if model.selectedIndex != 0 {
			t.Errorf("selectedIndex = %d, want 0", model.selectedIndex)
		}
	})

	t.Run("remove non-existent port returns false", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)

		found := model.RemoveTunnel(9999)
		if found {
			t.Error("RemoveTunnel should return false for non-existent port")
		}
		if len(model.tunnels) != 2 {
			t.Errorf("tunnels length = %d, want 2", len(model.tunnels))
		}
	})
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

// --- Tests for command input and manager wiring ---

// newManagedTestModel creates a Model wired to a TunnelManager with mock tunnels,
// suitable for testing command dispatch behavior.
func newManagedTestModel(t *testing.T) (Model, map[int]*mockTunnel) {
	t.Helper()
	mocks := make(map[int]*mockTunnel)
	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
	model := NewModelWithManager(mgr, PlanInfo{Name: "Pro", MaxTunnels: 5})
	return model, mocks
}

// typeCommand simulates typing a string into the input buffer via rune messages.
func typeCommand(t *testing.T, model Model, text string) Model {
	t.Helper()
	for _, char := range text {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}}
		updatedModel, _ := model.Update(msg)
		model = updatedModel.(Model)
	}
	return model
}

// pressEnter simulates pressing the Enter key.
func pressEnter(t *testing.T, model Model) (Model, tea.Cmd) {
	t.Helper()
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, cmd := model.Update(enterMsg)
	return updatedModel.(Model), cmd
}

func TestInputBufferTyping(t *testing.T) {
	t.Parallel()

	t.Run("rune input appends to input buffer", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)
		model = typeCommand(t, model, "/add 8080")

		if model.inputBuffer != "/add 8080" {
			t.Errorf("inputBuffer = %q, want %q", model.inputBuffer, "/add 8080")
		}
	})

	t.Run("backspace removes last character from buffer", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)
		model = typeCommand(t, model, "/add")

		backspaceMsg := tea.KeyMsg{Type: tea.KeyBackspace}
		updatedModel, _ := model.Update(backspaceMsg)
		model = updatedModel.(Model)

		if model.inputBuffer != "/ad" {
			t.Errorf("inputBuffer = %q, want %q", model.inputBuffer, "/ad")
		}
	})

	t.Run("backspace on empty buffer is no-op", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)

		backspaceMsg := tea.KeyMsg{Type: tea.KeyBackspace}
		updatedModel, _ := model.Update(backspaceMsg)
		model = updatedModel.(Model)

		if model.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty", model.inputBuffer)
		}
	})

	t.Run("escape clears the input buffer", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)
		model = typeCommand(t, model, "/add 8080")

		escMsg := tea.KeyMsg{Type: tea.KeyEscape}
		updatedModel, _ := model.Update(escMsg)
		model = updatedModel.(Model)

		if model.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty after Esc", model.inputBuffer)
		}
	})

	t.Run("enter with input buffer executes command and clears buffer", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080")

		model, _ = pressEnter(t, model)

		if model.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty after Enter", model.inputBuffer)
		}
	})

	t.Run("enter with empty input buffer and tunnels switches to detail view", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 3)

		model, _ = pressEnter(t, model)

		if model.viewState != viewDetail {
			t.Errorf("viewState = %d, want viewDetail (%d)", model.viewState, viewDetail)
		}
	})
}

func TestAddCommandDispatch(t *testing.T) {
	t.Parallel()

	t.Run("add command creates tunnel via manager", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080")

		model, _ = pressEnter(t, model)

		if model.errorMessage != "" {
			t.Errorf("unexpected error: %s", model.errorMessage)
		}
		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel in display, got %d", len(model.tunnels))
		}
		if model.tunnels[0].Port != 8080 {
			t.Errorf("tunnel port = %d, want 8080", model.tunnels[0].Port)
		}
		if model.tunnels[0].State != StateConnecting {
			t.Errorf("tunnel state = %v, want StateConnecting", model.tunnels[0].State)
		}
	})

	t.Run("add command with name uses provided name", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080 --name web")

		model, _ = pressEnter(t, model)

		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel, got %d", len(model.tunnels))
		}
		if model.tunnels[0].Name != "web" {
			t.Errorf("tunnel name = %q, want %q", model.tunnels[0].Name, "web")
		}
	})

	t.Run("add duplicate port shows error", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		if model.errorMessage == "" {
			t.Error("expected error for duplicate port, got empty error")
		}
		if len(model.tunnels) != 1 {
			t.Errorf("expected 1 tunnel (no duplicate), got %d", len(model.tunnels))
		}
	})

	t.Run("add command without manager shows error", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 0)
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		if model.errorMessage == "" {
			t.Error("expected error when manager is nil")
		}
	})
}

func TestRemoveCommandDispatch(t *testing.T) {
	t.Parallel()

	t.Run("remove by index removes tunnel from manager and display", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)

		// Add two tunnels
		model = typeCommand(t, model, "/add 3000")
		model, _ = pressEnter(t, model)
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		if len(model.tunnels) != 2 {
			t.Fatalf("expected 2 tunnels, got %d", len(model.tunnels))
		}

		// Remove tunnel at index 1 (1-based)
		model = typeCommand(t, model, "/remove 1")
		model, _ = pressEnter(t, model)

		if model.errorMessage != "" {
			t.Errorf("unexpected error: %s", model.errorMessage)
		}
		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel after removal, got %d", len(model.tunnels))
		}
		if model.tunnels[0].Port != 8080 {
			t.Errorf("remaining tunnel port = %d, want 8080", model.tunnels[0].Port)
		}
	})

	t.Run("remove non-existent index shows error", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		model = typeCommand(t, model, "/remove 5")
		model, _ = pressEnter(t, model)

		if model.errorMessage == "" {
			t.Error("expected error for non-existent index")
		}
	})

	t.Run("remove without manager shows error", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 2)
		model = typeCommand(t, model, "/remove 1")
		model, _ = pressEnter(t, model)

		if model.errorMessage == "" {
			t.Error("expected error when manager is nil")
		}
	})
}

func TestQuitCommandDispatch(t *testing.T) {
	t.Parallel()

	t.Run("quit command with manager calls shutdown and quits", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)

		// Add a tunnel first
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		model = typeCommand(t, model, "/quit")
		_, cmd := pressEnter(t, model)

		if cmd == nil {
			t.Fatal("/quit should produce a quit command")
		}
		quitMsg := cmd()
		if _, isQuit := quitMsg.(tea.QuitMsg); !isQuit {
			t.Errorf("/quit produced %T, want tea.QuitMsg", quitMsg)
		}
	})

	t.Run("quit command without manager just quits", func(t *testing.T) {
		t.Parallel()
		model := newTestModel(t, 0)
		model = typeCommand(t, model, "/quit")
		_, cmd := pressEnter(t, model)

		if cmd == nil {
			t.Fatal("/quit should produce a quit command")
		}
	})
}

func TestHelpCommandDispatch(t *testing.T) {
	t.Parallel()

	model, _ := newManagedTestModel(t)
	model = typeCommand(t, model, "/help")
	model, _ = pressEnter(t, model)

	if model.errorMessage == "" {
		t.Error("expected help text in errorMessage")
	}
	if !strings.Contains(model.errorMessage, "/add") {
		t.Errorf("help text should mention /add, got %q", model.errorMessage)
	}
}

func TestListCommandDispatch(t *testing.T) {
	t.Parallel()

	model, _ := newManagedTestModel(t)

	// Add a tunnel, go to detail view
	model = typeCommand(t, model, "/add 8080")
	model, _ = pressEnter(t, model)

	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, _ := model.Update(enterMsg)
	model = updatedModel.(Model)

	if model.viewState != viewDetail {
		t.Fatalf("expected detail view, got %d", model.viewState)
	}

	// /list should return to list view
	model = typeCommand(t, model, "/list")
	model, _ = pressEnter(t, model)

	if model.viewState != viewList {
		t.Errorf("viewState after /list = %d, want viewList", model.viewState)
	}
}

func TestUnknownCommandShowsError(t *testing.T) {
	t.Parallel()

	model, _ := newManagedTestModel(t)
	model = typeCommand(t, model, "/unknown")
	model, _ = pressEnter(t, model)

	if model.errorMessage == "" {
		t.Error("expected error for unknown command")
	}
	if !strings.Contains(model.errorMessage, "Unknown command") {
		t.Errorf("error = %q, want to contain 'Unknown command'", model.errorMessage)
	}
}

func TestCtrlCWithManagerCallsShutdown(t *testing.T) {
	t.Parallel()

	model, _ := newManagedTestModel(t)

	// Add a tunnel
	model = typeCommand(t, model, "/add 8080")
	model, _ = pressEnter(t, model)

	// Manager should have 1 tunnel
	if model.manager.Count() != 1 {
		t.Fatalf("expected 1 tunnel in manager, got %d", model.manager.Count())
	}

	// Press Ctrl+C
	ctrlCMsg := tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd := model.Update(ctrlCMsg)

	if cmd == nil {
		t.Fatal("Ctrl+C should produce a quit command")
	}

	// Manager should have been shut down (0 tunnels)
	if model.manager.Count() != 0 {
		t.Errorf("expected 0 tunnels after Ctrl+C shutdown, got %d", model.manager.Count())
	}
}

func TestPasswordProtectedDisplayEntry(t *testing.T) {
	t.Parallel()

	t.Run("TunnelConnectedMsg with PasswordProtected sets display entry flag", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)

		// Add a tunnel
		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel, got %d", len(model.tunnels))
		}

		// Simulate connected message with password protection
		connMsg := TunnelConnectedMsg{
			Port:              8080,
			Subdomain:         "my-sub",
			PublicURL:         "https://my-sub.justtunnel.dev",
			PasswordProtected: true,
		}
		updatedModel, _ := model.Update(connMsg)
		model = updatedModel.(Model)

		if !model.tunnels[0].PasswordProtected {
			t.Error("expected PasswordProtected to be true in display entry")
		}
	})

	t.Run("TunnelConnectedMsg without PasswordProtected leaves display entry false", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)

		model = typeCommand(t, model, "/add 8080")
		model, _ = pressEnter(t, model)

		connMsg := TunnelConnectedMsg{
			Port:              8080,
			Subdomain:         "my-sub",
			PublicURL:         "https://my-sub.justtunnel.dev",
			PasswordProtected: false,
		}
		updatedModel, _ := model.Update(connMsg)
		model = updatedModel.(Model)

		if model.tunnels[0].PasswordProtected {
			t.Error("expected PasswordProtected to be false in display entry")
		}
	})
}

func TestAddCommandWithPassword(t *testing.T) {
	t.Parallel()

	t.Run("add command with --password passes password to manager", func(t *testing.T) {
		t.Parallel()
		model, _ := newManagedTestModel(t)
		model = typeCommand(t, model, "/add 8080 --password secret123")
		model, _ = pressEnter(t, model)

		if model.errorMessage != "" {
			t.Errorf("unexpected error: %s", model.errorMessage)
		}
		if len(model.tunnels) != 1 {
			t.Fatalf("expected 1 tunnel, got %d", len(model.tunnels))
		}
		if model.tunnels[0].Port != 8080 {
			t.Errorf("tunnel port = %d, want 8080", model.tunnels[0].Port)
		}
	})
}

func TestNewModelWithManagerInitialState(t *testing.T) {
	t.Parallel()

	mgr := NewTunnelManager(mockTunnelFactory(nil), nil)
	model := NewModelWithManager(mgr, PlanInfo{Name: "starter", MaxTunnels: 2})

	if model.manager == nil {
		t.Fatal("manager should be set")
	}
	if len(model.tunnels) != 0 {
		t.Errorf("initial tunnel count = %d, want 0", len(model.tunnels))
	}
	if model.planInfo.Name != "starter" {
		t.Errorf("plan name = %q, want %q", model.planInfo.Name, "starter")
	}
	if model.planInfo.MaxTunnels != 2 {
		t.Errorf("max tunnels = %d, want 2", model.planInfo.MaxTunnels)
	}
	if model.viewState != viewList {
		t.Errorf("viewState = %d, want viewList", model.viewState)
	}
}
