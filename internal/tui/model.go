package tui

import (
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
	ID        int
	Name      string
	Port      int
	Subdomain string
	PublicURL string
	State     TunnelState
	Error     string
	Uptime    time.Duration
	Requests  int64
	AvgReqSec float64
	// RecentRequests would hold the request log for detail view (Phase 2).
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
}

// NewModel creates a new TUI model with the given tunnel entries and plan info.
func NewModel(tunnels []TunnelDisplayEntry, planInfo PlanInfo) Model {
	return Model{
		tunnels:   tunnels,
		planInfo:  planInfo,
		viewState: viewList,
		width:     80,
		height:    24,
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

	case TunnelRequestMsg:
		return m.handleTunnelRequest(msg)

	case TunnelErrorMsg:
		return m.handleTunnelError(msg)
	}

	return m, nil
}

// handleKeyMsg processes keyboard input based on the current view state.
func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
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
		if m.viewState == viewList && len(m.tunnels) > 0 {
			m.viewState = viewDetail
		}
		return m, nil

	case tea.KeyEscape:
		if m.viewState == viewDetail {
			m.viewState = viewList
		}
		return m, nil
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

// handleTunnelRequest updates the tunnel entry with a new request event.
func (m Model) handleTunnelRequest(msg TunnelRequestMsg) (tea.Model, tea.Cmd) {
	for idx := range m.tunnels {
		if m.tunnels[idx].Port == msg.Port {
			m.tunnels[idx].Requests++
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

// View renders the current state of the model as a string.
func (m Model) View() string {
	switch m.viewState {
	case viewDetail:
		return renderDetailView(m)
	default:
		return renderListView(m)
	}
}
