package tui

import (
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// viewState represents which view is currently displayed.
type viewState int

const (
	viewList   viewState = iota
	viewDetail
)

// TunnelDisplayEntry holds the display data for a single tunnel in the TUI.
// This is a view-layer struct — no real tunnel connections, just display state.
type TunnelDisplayEntry struct {
	ID                int
	Name              string
	Port              int
	Subdomain         string
	PublicURL         string
	State             TunnelState
	Error             string
	ConnectedAt       time.Time
	Requests          int64
	AvgReqSec         float64
	PasswordProtected bool
	RecentRequests    []RequestEntry
}

// PlanInfo holds the user's plan name and tunnel limit.
type PlanInfo struct {
	Name       string
	MaxTunnels int
}

// Model is the main Bubble Tea model for the TUI application.
type Model struct {
	tunnels       []TunnelDisplayEntry
	selectedIndex int
	viewState     viewState
	planInfo      PlanInfo
	errorMessage  string
	inputBuffer   string
	tickCount     int
	width         int
	height        int

	// manager is the tunnel lifecycle manager. When nil, the model operates
	// in display-only mode (used by existing tests). When set, slash commands
	// are dispatched to the manager for real tunnel operations.
	manager *TunnelManager
}

// NewModel creates a new TUI model with the given tunnel entries and plan info.
// This constructor creates a display-only model without a manager, preserving
// backward compatibility with existing tests.
func NewModel(tunnels []TunnelDisplayEntry, planInfo PlanInfo) Model {
	return Model{
		tunnels:   tunnels,
		planInfo:  planInfo,
		viewState: viewList,
		width:     80,
		height:    24,
	}
}

// NewModelWithManager creates a TUI model wired to a TunnelManager for
// real tunnel operations. Slash commands typed in the input bar are
// dispatched to the manager.
func NewModelWithManager(manager *TunnelManager, planInfo PlanInfo) Model {
	return Model{
		tunnels:   make([]TunnelDisplayEntry, 0),
		planInfo:  planInfo,
		viewState: viewList,
		width:     80,
		height:    24,
		manager:   manager,
	}
}

// Init returns the initial command for the Bubble Tea program.
// It starts a 1-second ticker for uptime refresh.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// tickCmd returns a command that sends a TickMsg after 1 second.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(tickTime time.Time) tea.Msg {
		return TickMsg(tickTime)
	})
}

// Update handles all incoming messages and returns the updated model and any commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case TickMsg:
		m.tickCount++
		return m, tickCmd()

	case TunnelConnectedMsg:
		return m.handleTunnelConnected(msg)

	case TunnelDisconnectedMsg:
		return m.handleTunnelDisconnected(msg)

	case TunnelReconnectingMsg:
		return m.handleTunnelReconnecting(msg)

	case TunnelReconnectedMsg:
		return m.handleTunnelReconnected(msg)

	case TunnelRequestMsg:
		return m.handleTunnelRequest(msg)

	case TunnelErrorMsg:
		return m.handleTunnelError(msg)

	case ConfigChangedMsg:
		return m.handleConfigChanged(msg)

	case ConfigReloadErrorMsg:
		m.errorMessage = msg.Error
		return m, nil
	}

	return m, nil
}

// handleKeyMsg processes keyboard input based on the current view state.
func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.manager != nil {
			m.manager.Shutdown()
		}
		return m, tea.Quit

	case tea.KeyUp:
		if m.viewState == viewList && m.selectedIndex > 0 {
			m.selectedIndex--
		}
		return m, nil

	case tea.KeyDown:
		if m.viewState == viewList && m.selectedIndex < len(m.tunnels)-1 {
			m.selectedIndex++
		}
		return m, nil

	case tea.KeyEnter:
		return m.handleEnter()

	case tea.KeyEscape:
		if m.viewState == viewDetail {
			m.viewState = viewList
		}
		m.inputBuffer = ""
		m.errorMessage = ""
		return m, nil

	case tea.KeyBackspace:
		if len(m.inputBuffer) > 0 {
			m.inputBuffer = m.inputBuffer[:len(m.inputBuffer)-1]
		}
		return m, nil

	case tea.KeySpace:
		m.inputBuffer += " "
		m.errorMessage = ""
		return m, nil

	case tea.KeyRunes:
		m.inputBuffer += string(msg.Runes)
		// Clear error on new input
		m.errorMessage = ""
		return m, nil
	}

	return m, nil
}

// handleEnter processes the Enter key. When the input buffer has content,
// it parses and executes the command. When empty, it switches to detail view.
func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	if m.inputBuffer != "" {
		return m.executeInputBuffer()
	}

	// No input — switch to detail view if in list view with tunnels
	if m.viewState == viewList && len(m.tunnels) > 0 {
		m.viewState = viewDetail
	}
	return m, nil
}

// executeInputBuffer parses the input buffer as a command and dispatches it.
func (m Model) executeInputBuffer() (tea.Model, tea.Cmd) {
	input := m.inputBuffer
	m.inputBuffer = ""
	m.errorMessage = ""

	parsedCmd, parseErr := ParseCommand(input)
	if parseErr != nil {
		m.errorMessage = parseErr.Error()
		return m, nil
	}

	// ParseCommand returns nil, nil for non-command input
	if parsedCmd == nil {
		return m, nil
	}

	switch cmd := parsedCmd.(type) {
	case AddCommand:
		return m.handleAddCommand(cmd)
	case RemoveCommand:
		return m.handleRemoveCommand(cmd)
	case ListCommand:
		m.viewState = viewList
		return m, nil
	case QuitCommand:
		if m.manager != nil {
			m.manager.Shutdown()
		}
		return m, tea.Quit
	case HelpCommand:
		m.errorMessage = "Commands: /add <port>, /remove <index>, /list, /quit, /help"
		return m, nil
	}

	return m, nil
}

// handleAddCommand dispatches an /add command to the manager.
func (m Model) handleAddCommand(cmd AddCommand) (tea.Model, tea.Cmd) {
	if m.manager == nil {
		m.errorMessage = "tunnel manager not available"
		return m, nil
	}

	addErr := m.manager.Add(cmd.Port, cmd.Name, cmd.Subdomain, cmd.Password)
	if addErr != nil {
		m.errorMessage = addErr.Error()
		return m, nil
	}

	// Add a display entry for the new tunnel
	nextID := len(m.tunnels) + 1
	displayName := cmd.Name
	if displayName == "" {
		displayName = fmt.Sprintf(":%d", cmd.Port)
	}

	m.tunnels = append(m.tunnels, TunnelDisplayEntry{
		ID:    nextID,
		Name:  displayName,
		Port:  cmd.Port,
		State: StateConnecting,
	})

	return m, nil
}

// handleRemoveCommand dispatches a /remove command to the manager.
// The target can be a 1-based index or a port number.
func (m Model) handleRemoveCommand(cmd RemoveCommand) (tea.Model, tea.Cmd) {
	if m.manager == nil {
		m.errorMessage = "tunnel manager not available"
		return m, nil
	}

	target, parseErr := strconv.Atoi(cmd.Target)
	if parseErr != nil {
		m.errorMessage = fmt.Sprintf("invalid target: %s (must be an index or port number)", cmd.Target)
		return m, nil
	}

	// Try as 1-based index first, then as port number
	var removeErr error
	if target >= 1 && target <= len(m.tunnels) {
		// Looks like a valid index — get the port before removing
		port := m.tunnels[target-1].Port
		removeErr = m.manager.RemoveByIndex(target)
		if removeErr == nil {
			m.RemoveTunnel(port)
			// If we were in detail view of the removed tunnel, go back to list
			if m.viewState == viewDetail && m.selectedIndex >= len(m.tunnels) {
				m.viewState = viewList
			}
		}
	} else {
		// Try as port number
		removeErr = m.manager.RemoveByPort(target)
		if removeErr == nil {
			m.RemoveTunnel(target)
			if m.viewState == viewDetail && m.selectedIndex >= len(m.tunnels) {
				m.viewState = viewList
			}
		}
	}

	if removeErr != nil {
		m.errorMessage = removeErr.Error()
	}

	return m, nil
}

// handleTunnelConnected updates the tunnel entry when a connection is established.
func (m Model) handleTunnelConnected(msg TunnelConnectedMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].State = StateConnected
			m.tunnels[idx].Subdomain = msg.Subdomain
			m.tunnels[idx].PublicURL = msg.PublicURL
			m.tunnels[idx].ConnectedAt = time.Now()
			m.tunnels[idx].PasswordProtected = msg.PasswordProtected
			break
		}
	}
	return m, nil
}

// handleTunnelDisconnected updates the tunnel entry when connection is lost.
func (m Model) handleTunnelDisconnected(msg TunnelDisconnectedMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].State = StateDisconnected
			break
		}
	}
	return m, nil
}

// handleTunnelReconnected updates the tunnel entry when a reconnect succeeds.
func (m Model) handleTunnelReconnected(msg TunnelReconnectedMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].State = StateConnected
			if msg.SubdomainChanged {
				m.tunnels[idx].Subdomain = msg.NewSubdomain
			}
			m.tunnels[idx].ConnectedAt = time.Now()
			break
		}
	}
	return m, nil
}

// handleTunnelReconnecting updates the tunnel entry when a reconnect is attempted.
func (m Model) handleTunnelReconnecting(msg TunnelReconnectingMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].State = StateReconnecting
			break
		}
	}
	return m, nil
}

// maxDisplayRequests is the maximum number of recent requests shown in the detail view.
const maxDisplayRequests = 10

// handleTunnelRequest updates the tunnel entry with a new request event.
func (m Model) handleTunnelRequest(msg TunnelRequestMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].Requests++
			entry := RequestEntry{
				Method:     msg.Method,
				Path:       msg.Path,
				StatusCode: msg.Status,
				Duration:   msg.Latency,
				Timestamp:  time.Now(),
			}
			recent := m.tunnels[idx].RecentRequests
			if len(recent) >= maxDisplayRequests {
				copy(recent, recent[1:])
				recent[len(recent)-1] = entry
			} else {
				recent = append(recent, entry)
			}
			m.tunnels[idx].RecentRequests = recent
			break
		}
	}
	return m, nil
}

// handleTunnelError sets the tunnel to error state and records the error message.
func (m Model) handleTunnelError(msg TunnelErrorMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].State = StateError
			m.tunnels[idx].Error = msg.Message
			break
		}
	}
	return m, nil
}

// handleConfigChanged processes a config file hot-reload event. It adds and
// removes tunnels based on the diff between the desired config and current state.
func (m Model) handleConfigChanged(msg ConfigChangedMsg) (tea.Model, tea.Cmd) {
	if m.manager == nil {
		m.errorMessage = "tunnel manager not available"
		return m, nil
	}

	// Remove tunnels that are no longer in the config
	for _, port := range msg.ToRemove {
		removeErr := m.manager.RemoveByPort(port)
		if removeErr == nil {
			m.RemoveTunnel(port)
		}
	}

	// Add tunnels that are new in the config
	for _, preset := range msg.ToAdd {
		addErr := m.manager.Add(preset.Port, preset.Name, preset.Subdomain, preset.Password)
		if addErr != nil {
			continue
		}

		nextID := len(m.tunnels) + 1
		displayName := preset.Name
		if displayName == "" {
			displayName = fmt.Sprintf(":%d", preset.Port)
		}
		m.tunnels = append(m.tunnels, TunnelDisplayEntry{
			ID:    nextID,
			Name:  displayName,
			Port:  preset.Port,
			State: StateConnecting,
		})
	}

	m.clampSelectedIndex()
	return m, nil
}

// clampSelectedIndex ensures selectedIndex is within valid bounds after tunnel list changes.
func (m *Model) clampSelectedIndex() {
	if len(m.tunnels) == 0 {
		m.selectedIndex = 0
		return
	}
	if m.selectedIndex >= len(m.tunnels) {
		m.selectedIndex = len(m.tunnels) - 1
	}
}

// AddDisplayEntry adds a display entry for a tunnel that was started outside the
// command dispatch flow (e.g., the initial tunnel from CLI args). This is used
// to populate the model before tea.Program.Run() starts.
func (m *Model) AddDisplayEntry(port int, name string) {
	nextID := len(m.tunnels) + 1
	displayName := name
	if displayName == "" {
		displayName = fmt.Sprintf(":%d", port)
	}
	m.tunnels = append(m.tunnels, TunnelDisplayEntry{
		ID:    nextID,
		Name:  displayName,
		Port:  port,
		State: StateConnecting,
	})
}

// RemoveTunnel removes a tunnel by port and clamps the selection. Returns true if found.
func (m *Model) RemoveTunnel(port int) bool {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == port {
			m.tunnels = append(m.tunnels[:idx], m.tunnels[idx+1:]...)
			m.clampSelectedIndex()
			// If in detail view and the removed tunnel was selected, return to list
			if m.viewState == viewDetail && m.selectedIndex >= len(m.tunnels) {
				m.viewState = viewList
			}
			return true
		}
	}
	return false
}

// View renders the current state of the model as a string.
func (m Model) View() string {
	switch m.viewState {
	case viewDetail:
		return renderDetailView(m)
	default:
		return renderListView(m)
	}
}
