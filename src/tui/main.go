package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/mattn/go-runewidth"
	"regexp"
)

type stacksMsg struct {
	stacks []stack
	nodes  []node
	err    error
}
type containersMsg struct {
	generation int
	containers []container
	databases  []databaseDetail
	err        error
}
type connectedMsg struct {
	generation int
	conn       *websocket.Conn
	err        error
}
type logsMsg struct {
	generation int
	text       string
	err        error
}
type clipboardMsg struct{ err error }
type containerRestartedMsg struct {
	generation int
	err        error
}
type stackUpdatedMsg struct {
	generation int
	err        error
}
type hostKeyTrustedMsg struct {
	generation  int
	fingerprint string
	err         error
}
type backupStartedMsg struct {
	databaseID string
	record     backupRecord
	err        error
}
type restoreStartedMsg struct {
	jobID string
	err   error
}
type model struct {
	backupSelection            bool
	backupIndex                int
	restoreConfirm             int
	searching                  bool
	searchQuery                string
	searchIndex                int
	restoring                  bool
	api                        *apiClient
	logColours                 []colourRule
	stacks                     []stack
	nodes                      []node
	sidebarOffset              int
	sidebarManual              bool
	containers                 []container
	databases                  []databaseDetail
	stackIndex, containerIndex int
	databaseIndex              int
	activeDatabase             bool
	selectText                 bool
	restarting                 bool
	updating                   bool
	trusting                   bool
	width, height              int
	scroll                     int
	horizontal                 int
	generation                 int
	status                     string
	logs                       []string
	pending                    string
	conn                       *websocket.Conn
	cancel                     context.CancelFunc
}

func (m *model) Init() tea.Cmd { return m.loadStacks() }
func (m *model) loadStacks() tea.Cmd {
	return func() tea.Msg {
		stacks, err := m.api.stacks(context.Background())
		if err != nil {
			return stacksMsg{err: err}
		}
		nodes, err := m.api.nodes(context.Background())
		return stacksMsg{stacks: stacks, nodes: nodes, err: err}
	}
}
func (m *model) stopStream() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
	}
}
func (m *model) selectStack() tea.Cmd {
	m.stopStream()
	m.generation++
	generation := m.generation
	m.containers = nil
	m.databases = nil
	m.containerIndex = 0
	m.databaseIndex = 0
	m.activeDatabase = false
	m.logs = nil
	m.pending = ""
	m.scroll = 0
	m.horizontal = 0
	m.status = "Loading containers..."
	id := m.stacks[m.stackIndex].ID
	return func() tea.Msg {
		v, e := m.api.stackDetails(context.Background(), id)
		if e != nil {
			return containersMsg{generation: generation, err: e}
		}
		databases := make([]databaseDetail, 0, len(v.Databases))
		for _, record := range v.Databases {
			detail, err := m.api.databaseDetails(context.Background(), record.ID)
			if err != nil {
				return containersMsg{generation: generation, err: fmt.Errorf("load database %s: %w", record.Name, err)}
			}
			databases = append(databases, detail)
		}
		for _, service := range v.Services {
			found := false
			for _, c := range v.Containers {
				if c.Service == service.Name {
					found = true
					break
				}
			}
			if !found {
				v.Containers = append(v.Containers, container{Service: service.Name})
			}
		}
		return containersMsg{generation, v.Containers, databases, nil}
	}
}
func (m *model) selectContainer() tea.Cmd {
	m.stopStream()
	m.generation++
	generation := m.generation
	m.logs = nil
	m.pending = ""
	m.scroll = 0
	m.horizontal = 0
	m.status = "Connecting to logs..."
	m.activeDatabase = false
	c := m.containers[m.containerIndex]
	if c.ID == "" && c.Name == "" {
		m.status = "No containers running"
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	id := m.stacks[m.stackIndex].ID
	name := c.ID
	if name == "" {
		name = c.Name
	}
	return func() tea.Msg {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return connectedMsg{generation: generation, err: ctx.Err()}
		case <-timer.C:
		}
		conn, err := m.api.logs(ctx, id, name)
		return connectedMsg{generation, conn, err}
	}
}
func readLogs(conn *websocket.Conn, generation int) tea.Cmd {
	return func() tea.Msg { _, data, err := conn.ReadMessage(); return logsMsg{generation, string(data), err} }
}
func clipboardCommand() (*exec.Cmd, error) {
	commands := [][]string{}
	switch runtime.GOOS {
	case "darwin":
		commands = append(commands, []string{"pbcopy"})
	case "windows":
		commands = append(commands, []string{"clip.exe"})
	default:
		commands = append(commands, []string{"wl-copy"}, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	for _, args := range commands {
		path, err := exec.LookPath(args[0])
		if err == nil {
			return exec.Command(path, args[1:]...), nil
		}
	}
	return nil, errors.New("no clipboard command found")
}
func copyToClipboard(value string) error {
	cmd, err := clipboardCommand()
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(value)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clipboard command: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
func (m *model) copyConnectionString() tea.Cmd {
	value := m.databases[m.databaseIndex].ConnectionString
	return func() tea.Msg { return clipboardMsg{err: copyToClipboard(value)} }
}
func (m *model) startDatabaseBackup() tea.Cmd {
	databaseID := m.databases[m.databaseIndex].ID
	return func() tea.Msg {
		record, err := m.api.createBackup(context.Background(), databaseID)
		return backupStartedMsg{databaseID: databaseID, record: record, err: err}
	}
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}
func (m *model) appendLogs(s string) {
	s = m.pending + safeText(s)
	lines := strings.Split(s, "\n")
	m.pending = lines[len(lines)-1]
	m.logs = append(m.logs, lines[:len(lines)-1]...)
	if m.scroll > 0 {
		m.scroll += len(lines) - 1
	}
	if len(m.logs) > 5000 {
		m.logs = append([]string(nil), m.logs[len(m.logs)-5000:]...)
	}
	m.clampScroll()
	m.clampHorizontal()
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.clampScroll()
		m.clampHorizontal()
	case tea.MouseMsg:
		if m.searching || m.restoreConfirm > 0 {
			if m.searching {
				switch v.Button {
				case tea.MouseButtonWheelUp:
					m.searchIndex = max(0, m.searchIndex-1)
				case tea.MouseButtonWheelDown:
					m.searchIndex = min(max(0, len(m.searchMatches())-1), m.searchIndex+1)
				}
			}
			break
		}
		left := m.leftWidth()
		if v.X > 0 && v.X < left && v.Y >= 2 && v.Y < m.height-2 {
			if v.Button == tea.MouseButtonWheelUp {
				m.sidebarOffset -= 3
				m.sidebarManual = true
			}
			if v.Button == tea.MouseButtonWheelDown {
				m.sidebarOffset += 3
				m.sidebarManual = true
			}
			break
		}
		if v.X <= left+1 || v.X >= m.width-1 || v.Y < 2 || v.Y >= m.height-2 {
			break
		}
		switch {
		case v.Button == tea.MouseButtonWheelLeft || (v.Shift && v.Button == tea.MouseButtonWheelUp):
			m.horizontal -= 4
		case v.Button == tea.MouseButtonWheelRight || (v.Shift && v.Button == tea.MouseButtonWheelDown):
			m.horizontal += 4
		case v.Button == tea.MouseButtonWheelUp:
			m.scroll += 3
			m.clampScroll()
		case v.Button == tea.MouseButtonWheelDown:
			m.scroll -= 3
			if m.scroll < 0 {
				m.scroll = 0
			}
		}
		m.clampHorizontal()
	case tea.KeyMsg:
		if m.searching {
			switch v.String() {
			case "esc":
				m.searching = false
			case "up":
				m.searchIndex = max(0, m.searchIndex-1)
			case "down":
				m.searchIndex = min(max(0, len(m.searchMatches())-1), m.searchIndex+1)
			case "enter":
				matches := m.searchMatches()
				if len(matches) > 0 {
					m.stackIndex = matches[m.searchIndex]
					m.sidebarManual = false
					m.searching = false
					return m, m.selectStack()
				}
			case "backspace", "ctrl+h":
				runes := []rune(m.searchQuery)
				if len(runes) > 0 {
					m.searchQuery = string(runes[:len(runes)-1])
					m.searchIndex = 0
				}
			default:
				if v.Type == tea.KeyRunes {
					m.searchQuery += string(v.Runes)
					m.searchIndex = 0
				}
			}
			return m, nil
		}
		if m.restoreConfirm > 0 {
			switch v.String() {
			case "esc", "n":
				m.restoreConfirm = 0
			case "y":
				if m.restoreConfirm == 1 {
					m.restoreConfirm = 2
				} else {
					d := m.databases[m.databaseIndex]
					b := d.Backups[m.backupIndex]
					m.restoreConfirm = 0
					m.backupSelection = false
					m.restoring = true
					m.status = "Starting restore..."
					return m, func() tea.Msg {
						id, err := m.api.restoreBackup(context.Background(), d.ID, b.ID)
						return restoreStartedMsg{id, err}
					}
				}
			}
			return m, nil
		}
		if m.backupSelection {
			switch v.String() {
			case "esc":
				m.backupSelection = false
			case "up":
				if m.backupIndex > 0 {
					m.backupIndex--
				}
			case "down":
				if m.backupIndex+1 < len(m.databases[m.databaseIndex].Backups) {
					m.backupIndex++
				}
			case "enter":
				if m.databases[m.databaseIndex].Backups[m.backupIndex].Status == "succeeded" {
					m.restoreConfirm = 1
				} else {
					m.status = "Only completed backups can be restored"
				}
			}
			return m, nil
		}
		switch v.String() {
		case "q", "ctrl+c":
			m.stopStream()
			return m, tea.Quit
		case "s":
			m.searching = true
			m.searchQuery = ""
			m.searchIndex = 0
		case "m":
			m.selectText = !m.selectText
			m.clampScroll()
			if m.selectText {
				m.status = "Text selection enabled; select and copy using your terminal"
				return m, tea.DisableMouse
			}
			m.status = "Mouse scrolling enabled"
			return m, tea.EnableMouseCellMotion
		case "r":
			m.stopStream()
			m.generation++
			m.stacks = nil
			m.containers = nil
			m.databases = nil
			m.activeDatabase = false
			m.logs = nil
			m.pending = ""
			m.scroll = 0
			m.horizontal = 0
			m.status = "Loading stacks..."
			return m, m.loadStacks()
		case "T":
			if !m.trusting && m.stackIndex < len(m.stacks) {
				stackID, generation := m.stacks[m.stackIndex].ID, m.generation
				m.trusting = true
				m.status = "Trusting new SSH key for " + m.stacks[m.stackIndex].Node + "..."
				return m, func() tea.Msg {
					fingerprint, err := m.api.trustStackHostKey(context.Background(), stackID)
					return hostKeyTrustedMsg{generation: generation, fingerprint: fingerprint, err: err}
				}
			}
		case "U":
			if !m.updating && m.stackIndex < len(m.stacks) {
				stackID, generation := m.stacks[m.stackIndex].ID, m.generation
				m.updating = true
				m.status = "Updating " + m.stacks[m.stackIndex].Name + "..."
				return m, func() tea.Msg {
					return stackUpdatedMsg{generation: generation, err: m.api.updateStack(context.Background(), stackID)}
				}
			}
		case "R":
			if !m.restarting && !m.activeDatabase && m.stackIndex < len(m.stacks) && m.containerIndex < len(m.containers) && m.containers[m.containerIndex].Name != "" {
				stackID, name, generation := m.stacks[m.stackIndex].ID, m.containers[m.containerIndex].Name, m.generation
				m.restarting = true
				m.status = "Restarting " + name + "..."
				return m, func() tea.Msg {
					return containerRestartedMsg{generation: generation, err: m.api.restartContainer(context.Background(), stackID, name)}
				}
			}
		case "S":
			if !m.restoring && m.activeDatabase && m.databaseIndex < len(m.databases) && len(m.databases[m.databaseIndex].Backups) > 0 {
				m.backupSelection = true
				m.backupIndex = 0
			}
		case "c":
			if m.activeDatabase && m.databaseIndex < len(m.databases) {
				return m, m.copyConnectionString()
			}
		case "b":
			if m.activeDatabase && m.databaseIndex < len(m.databases) {
				m.status = "Starting backup..."
				return m, m.startDatabaseBackup()
			}
		case "up":
			m.sidebarManual = false
			if len(m.stacks) > 0 && m.stackIndex > 0 {
				m.stackIndex--
				return m, m.selectStack()
			}
		case "down":
			m.sidebarManual = false
			if m.stackIndex+1 < len(m.stacks) {
				m.stackIndex++
				return m, m.selectStack()
			}
		case "left":
			if m.activeDatabase {
				if m.databaseIndex > 0 {
					m.databaseIndex--
					m.status = "Database details"
				} else if len(m.containers) > 0 {
					m.activeDatabase = false
					m.containerIndex = len(m.containers) - 1
					return m, m.selectContainer()
				}
			} else if len(m.containers) > 0 && m.containerIndex > 0 {
				m.containerIndex--
				return m, m.selectContainer()
			}
		case "right":
			if m.activeDatabase {
				if m.databaseIndex+1 < len(m.databases) {
					m.databaseIndex++
					m.status = "Database details"
				}
			} else if m.containerIndex+1 < len(m.containers) {
				m.containerIndex++
				return m, m.selectContainer()
			} else if len(m.databases) > 0 {
				m.stopStream()
				m.generation++
				m.activeDatabase = true
				m.databaseIndex = 0
				m.status = "Database details"
			}
		case "pgup":
			step := m.height - 6
			if step < 1 {
				step = 1
			}
			m.scroll += step
			m.clampScroll()
		case "pgdown":
			step := m.height - 6
			if step < 1 {
				step = 1
			}
			m.scroll -= step
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "h":
			m.horizontal -= 4
			m.clampHorizontal()
		case "l":
			m.horizontal += 4
			m.clampHorizontal()
		}
	case stacksMsg:
		if v.err != nil {
			m.status = "Could not load stacks: " + v.err.Error()
			return m, nil
		}
		m.stacks = v.stacks
		m.nodes = v.nodes
		m.sidebarManual = false
		m.sidebarOffset = 0
		sort.SliceStable(m.stacks, func(i, j int) bool { return m.stacks[i].Node < m.stacks[j].Node })
		m.stackIndex = 0
		if len(m.stacks) == 0 {
			m.status = "No stacks found. Press r to refresh."
			return m, nil
		}
		return m, m.selectStack()
	case containersMsg:
		if v.generation != m.generation {
			return m, nil
		}
		if v.err != nil {
			m.status = "Could not load containers: " + v.err.Error()
			return m, nil
		}
		m.containers = v.containers
		m.databases = v.databases
		if len(m.containers) == 0 {
			if len(m.databases) > 0 {
				m.activeDatabase = true
				m.databaseIndex = 0
				m.status = "Database details"
				return m, nil
			}
			m.status = "No containers or databases in this stack."
			return m, nil
		}
		return m, m.selectContainer()
	case connectedMsg:
		if v.generation != m.generation {
			if v.conn != nil {
				v.conn.Close()
			}
			return m, nil
		}
		if v.err != nil {
			m.status = "Could not open logs: " + v.err.Error()
			return m, nil
		}
		m.conn = v.conn
		m.status = "Streaming"
		return m, readLogs(m.conn, m.generation)
	case logsMsg:
		if v.generation != m.generation {
			return m, nil
		}
		if v.text != "" {
			m.appendLogs(v.text)
		}
		if v.err != nil {
			m.stopStream()
			m.status = "Log stream ended. Press r to reconnect."
			return m, nil
		}
		return m, readLogs(m.conn, m.generation)
	case hostKeyTrustedMsg:
		m.trusting = false
		if v.generation != m.generation {
			return m, nil
		}
		if v.err != nil {
			m.status = "Could not trust SSH key: " + v.err.Error()
			return m, nil
		}
		m.status = "Trusted SSH key " + v.fingerprint + "; press r to reconnect"
		return m, nil
	case stackUpdatedMsg:
		m.updating = false
		if v.generation != m.generation {
			return m, nil
		}
		if v.err != nil {
			m.status = "Could not update stack: " + v.err.Error()
			return m, nil
		}
		m.status = "Stack updated"
		return m, m.selectStack()
	case containerRestartedMsg:
		m.restarting = false
		if v.generation != m.generation {
			return m, nil
		}
		if v.err != nil {
			m.status = "Could not restart container: " + v.err.Error()
			return m, nil
		}
		return m, m.selectContainer()
	case clipboardMsg:
		if v.err != nil {
			m.status = "Could not copy connection string: " + v.err.Error()
		} else {
			m.status = "Connection string copied to clipboard"
		}
	case restoreStartedMsg:
		m.restoring = false
		if v.err != nil {
			m.status = "Could not start restore: " + v.err.Error()
		} else {
			m.status = "Restore started (job " + v.jobID + "); check job status in the API"
		}
	case backupStartedMsg:
		if v.err != nil {
			m.status = "Could not start backup: " + v.err.Error()
			return m, nil
		}
		for i := range m.databases {
			if m.databases[i].ID == v.databaseID {
				m.databases[i].Backups = append([]backupRecord{v.record}, m.databases[i].Backups...)
				break
			}
		}
		m.status = "Backup started (job " + v.record.JobID + ")"
	}
	return m, nil
}
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > width {
		if width == 1 {
			return "…"
		}
		return string(r[:width-1]) + "…"
	}
	return s
}
func (m *model) logHeight() int {
	if m.selectText {
		if m.height > 0 {
			return m.height
		}
		return 1
	}
	height := m.height - 7
	height -= m.detailHeight()
	if height < 1 {
		return 1
	}
	return height
}
func (m *model) detailHeight() int {
	if len(m.containers) == 0 || len(m.stacks) == 0 || m.activeDatabase {
		return 0
	}
	n := m.height - 8
	if n < 0 {
		return 0
	}
	if n > 6 {
		return 6
	}
	return n
}
func (m *model) clampScroll() {
	count := len(m.logs)
	if m.pending != "" {
		count++
	}
	max := count - m.logHeight()
	if max < 0 {
		max = 0
	}
	if m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}
func (m *model) leftWidth() int {
	left := m.width / 4
	if left < 12 {
		left = 12
	}
	if left > 32 {
		left = 32
	}
	return left
}
func (m *model) clampHorizontal() {
	if m.horizontal <= 0 {
		m.horizontal = 0
		return
	}
	viewport := m.width - m.leftWidth() - 3
	if viewport < 1 {
		viewport = 1
	}
	longest := runewidth.StringWidth(strings.ReplaceAll(m.pending, "\t", "    "))
	for _, line := range m.logs {
		if width := runewidth.StringWidth(strings.ReplaceAll(line, "\t", "    ")); width > longest {
			longest = width
		}
	}
	max := longest - viewport
	if max < 0 {
		max = 0
	}
	if m.horizontal < 0 {
		m.horizontal = 0
	}
	if m.horizontal > max {
		m.horizontal = max
	}
}
func box(title string, content []string, width, height int) string {
	inner := width - 2
	title = truncate("─ "+title+" ", inner)
	top := "┌" + title + strings.Repeat("─", inner-lipgloss.Width(title)) + "┐"
	lines := make([]string, 0, height)
	lines = append(lines, top)
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(content) {
			line = lipgloss.NewStyle().MaxWidth(inner).Render(content[i])
		}
		padding := inner - lipgloss.Width(line)
		if padding < 0 {
			padding = 0
		}
		lines = append(lines, "│"+line+strings.Repeat(" ", padding)+"│")
	}
	lines = append(lines, "└"+strings.Repeat("─", inner)+"┘")
	return strings.Join(lines, "\n")
}

func formatDetails(details [][2]string, width int) []string {
	keyWidth := width / 6
	if keyWidth < 4 {
		keyWidth = 4
	}
	if keyWidth > width-3 {
		keyWidth = width - 3
	}
	valueWidth := width - keyWidth - 2
	lines := make([]string, 0, len(details))
	for _, pair := range details {
		key := truncate(pair[0], keyWidth)
		padding := keyWidth - lipgloss.Width(key)
		if padding < 0 {
			padding = 0
		}
		lines = append(lines, key+strings.Repeat(" ", padding+2)+truncate(safeText(pair[1]), valueWidth))
	}
	return lines
}

func formatDetailBanks(left, right [][2]string, width int) []string {
	if width < 56 {
		return formatDetails(left, width)
	}
	leftWidth := (width - 3) / 2
	rightWidth := width - leftWidth - 3
	formatBank := func(details [][2]string, bankWidth, keyWidth int) []string {
		lines := make([]string, len(details))
		for i, pair := range details {
			key := truncate(pair[0], keyWidth)
			lines[i] = key + strings.Repeat(" ", keyWidth-lipgloss.Width(key)+1) + truncate(safeText(pair[1]), bankWidth-keyWidth-1)
		}
		return lines
	}
	keyWidth := width / 6
	if keyWidth < 9 {
		keyWidth = 9
	}
	if keyWidth > leftWidth-3 {
		keyWidth = leftWidth - 3
	}
	keyWidth /= 2
	a, b := formatBank(left, leftWidth, keyWidth), formatBank(right, rightWidth, keyWidth)
	lines := make([]string, len(a))
	for i := range a {
		lines[i] = a[i] + strings.Repeat(" ", leftWidth-lipgloss.Width(a[i])+3)
		if i < len(b) {
			lines[i] += b[i]
		}
	}
	return lines
}

func (m *model) View() string {
	if m.selectText {
		lines := append([]string(nil), m.logs...)
		if m.pending != "" {
			lines = append(lines, m.pending)
		}
		end := len(lines) - m.scroll
		if end < 0 {
			end = 0
		}
		start := end - m.height
		if start < 0 {
			start = 0
		}
		lines = lines[start:end]
		for i := range lines {
			line := strings.ReplaceAll(lines[i], "\t", "    ")
			lines[i] = runewidth.Truncate(runewidth.TruncateLeft(line, m.horizontal, ""), m.width, "")
		}
		return strings.Join(lines, "\n")
	}
	if m.width < 25 || m.height < 7 {
		return "Terminal too small (minimum 25×7). Press q to quit."
	}
	left := m.leftWidth()
	right := m.width - left - 1
	boxHeight := m.height - 3
	leftInner, rightInner := left-2, right-2
	sidebar := make([]string, 0, len(m.stacks)+len(m.nodes))
	selectedRow := 0
	names := map[string]bool{}
	for _, n := range m.nodes {
		if n.Type == "workload" {
			names[n.Name] = true
		}
	}
	for _, st := range m.stacks {
		names[st.Node] = true
	}
	nodeNames := make([]string, 0, len(names))
	for name := range names {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	for _, name := range nodeNames {
		header := name
		if header == "" {
			header = "Unknown node"
		}
		sidebar = append(sidebar, lipgloss.NewStyle().Bold(true).Render(truncate(safeText(header), leftInner)))
		for i, st := range m.stacks {
			if st.Node != name {
				continue
			}
			if i == m.stackIndex {
				selectedRow = len(sidebar)
			}
			prefix := "  "
			if i == m.stackIndex {
				prefix = "▸ "
			}
			line := truncate(prefix+strings.ReplaceAll(safeText(st.Name), "\n", " "), leftInner)
			if i == m.stackIndex {
				line = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4A1FF")).Bold(true).Render(line)
			}
			sidebar = append(sidebar, line)
		}
	}
	visible := boxHeight - 2
	if len(sidebar) > visible {
		first := m.sidebarOffset
		if !m.sidebarManual {
			first = selectedRow - visible + 1
		}
		if first < 0 {
			first = 0
		}
		if first+visible > len(sidebar) {
			first = len(sidebar) - visible
		}
		m.sidebarOffset = first
		sidebar = sidebar[first : first+visible]
	} else {
		m.sidebarOffset = 0
	}

	tabNames := make([]string, 0, len(m.containers)+len(m.databases))
	activeTab := m.containerIndex
	for _, c := range m.containers {
		name := c.Service
		if name == "" {
			name = c.Name
		}
		tabNames = append(tabNames, strings.ReplaceAll(safeText(name), "\n", " "))
	}
	for _, d := range m.databases {
		tabNames = append(tabNames, "DB:"+d.Name)
	}
	if m.activeDatabase {
		activeTab = len(m.containers) + m.databaseIndex
	}
	startTab := activeTab
	usedWidth := 0
	for i := startTab; i >= 0 && i < len(tabNames); i-- {
		labelWidth := lipgloss.Width(tabNames[i]) + 2
		if usedWidth > 0 {
			labelWidth++
		}
		if usedWidth+labelWidth > rightInner {
			break
		}
		usedWidth += labelWidth
		startTab = i
	}
	tabs := []string{}
	for i := startTab; i < len(tabNames); i++ {
		name := truncate(tabNames[i], rightInner-2)
		label := " " + name + " "
		if i == activeTab {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4A1FF")).Render("[" + name + "]")
		}
		tabs = append(tabs, label)
	}
	tabLine := lipgloss.NewStyle().MaxWidth(rightInner).Render(strings.Join(tabs, " "))
	if len(tabs) == 0 {
		tabLine = truncate(m.status, rightInner)
	}
	rightContent := []string{tabLine, strings.Repeat("─", rightInner)}
	boxTitle := "Logs"
	if m.activeDatabase && m.databaseIndex < len(m.databases) {
		d := m.databases[m.databaseIndex]
		boxTitle = "Database: " + d.Name
		details := [][2]string{
			{"Database name", d.Name}, {"Database user", d.User}, {"Node", d.Node},
			{"Hostname", d.Hostname}, {"Port", fmt.Sprint(d.Port)}, {"Password", d.Password},
			{"Connection string", d.ConnectionString},
		}
		schedule := "Not scheduled"
		if d.BackupSchedule != nil {
			schedule = d.BackupSchedule.Frequency
			if d.BackupSchedule.Day != "" {
				schedule += " " + d.BackupSchedule.Day
			}
			if d.BackupSchedule.Time != "" {
				schedule += " " + d.BackupSchedule.Time
			}
			schedule += fmt.Sprintf(" (keep %d)", d.BackupSchedule.KeepLast)
		}
		details = append(details, [2]string{"Backup schedule", schedule})
		rightContent = append(rightContent, formatDetails(details, rightInner)...)
		rightContent = append(rightContent, strings.Repeat("─", rightInner), "Backups")
		if len(d.Backups) == 0 {
			rightContent = append(rightContent, "No backups recorded.")
		} else {
			for i, backup := range d.Backups {
				line := fmt.Sprintf("%s  %s  %.9f GB", brisbaneBackupTime(backup.StartedAt), backup.Status, float64(backup.Size)/1e9)
				line = truncate(safeText(line), rightInner-2)
				if m.backupSelection && i == m.backupIndex {
					line = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4A1FF")).Render("[" + line + "]")
				} else {
					line = " " + line + " "
				}
				rightContent = append(rightContent, line)
				if backup.Error != "" {
					rightContent = append(rightContent, "Error: "+safeText(backup.Error))
				}
			}
		}
	} else {
		if m.containerIndex < len(m.containers) && m.stackIndex < len(m.stacks) {
			c, st := m.containers[m.containerIndex], m.stacks[m.stackIndex]
			containerName := c.Name
			if c.ID == "" && c.Name == "" {
				containerName = "no containers running"
			}
			details := [][2]string{{"Stack", st.Name}, {"Node", st.Node}, {"Container", containerName}, {"Service", c.Service}, {"State", c.State}}
			nodeDetails := [][2]string{{"Node", st.Node}, {"Host", "Unknown"}, {"SSH port", "Unknown"}, {"Type", "Unknown"}, {"Reachable", "Unknown"}}
			for _, n := range m.nodes {
				if n.Name == st.Node {
					status := "No"
					if n.Reachable {
						status = "Yes"
					}
					nodeDetails = [][2]string{{"Node", n.Name}, {"Host", n.Hostname}, {"SSH port", fmt.Sprint(n.SSHPort)}, {"Type", n.Type}, {"Reachable", status}}
					break
				}
			}
			rows := append(formatDetailBanks(details, nodeDetails, rightInner), strings.Repeat("─", rightInner))
			rightContent = append(rightContent, rows[:m.detailHeight()]...)
		}
		lines := append([]string(nil), m.logs...)
		if m.pending != "" {
			lines = append(lines, m.pending)
		}
		available := boxHeight - 4
		available -= m.detailHeight()
		if available < 0 {
			available = 0
		}
		end := len(lines) - m.scroll
		if end < 0 {
			end = 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		start := end - available
		if start < 0 {
			start = 0
		}
		lines = lines[start:end]
		for i := range lines {
			line := strings.ReplaceAll(lines[i], "\t", "    ")
			visible := runewidth.Truncate(runewidth.TruncateLeft(line, m.horizontal, ""), rightInner, "")
			lines[i] = colourLogLine(m.logColours, line, visible)
		}
		rightContent = append(rightContent, lines...)
	}
	content := lipgloss.JoinHorizontal(lipgloss.Top, box("Stacks", sidebar, left, boxHeight), " ", box(boxTitle, rightContent, right, boxHeight))
	help := "↑/↓ stacks  s search  ←/→ tabs  h/l logs  R restart  U update  T trust SSH  m log selection mode  r refresh  q quit"
	if m.activeDatabase {
		help = "↑/↓ stacks  s search  ←/→ tabs  c copy URI  b backup  S select backup  U update  T trust SSH  m log selection mode  r refresh  q quit"
	}
	footer := truncate(m.status+"  |  "+help, m.width)
	view := lipgloss.NewStyle().Bold(true).Render("LAB CONTROLLER") + "\n" + content + "\n" + footer
	if m.restoreConfirm > 0 {
		return m.modal(view, m.restoreModal())
	}
	if m.searching {
		return m.modal(view, m.searchModal())
	}
	return view
}

func brisbaneBackupTime(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		if value == "" {
			return "Unknown"
		}
		return value
	}
	location, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		return value
	}
	t = t.In(location)
	day := t.Day()
	suffix := "th"
	if day%100 < 11 || day%100 > 13 {
		switch day % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strings.ToLower(t.Format("3:04PM")) + fmt.Sprintf(" %d%s ", day, suffix) + t.Format("Jan 2006")
}

func (m *model) searchMatches() []int {
	var matches []int
	if m.searchQuery == "" {
		return matches
	}
	query := strings.ToLower(m.searchQuery)
	for i, st := range m.stacks {
		if strings.Contains(strings.ToLower(st.Name), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func (m *model) searchModal() string {
	lines := []string{"Search stacks", "> " + safeText(m.searchQuery), ""}
	matches := m.searchMatches()
	available := max(1, m.height-10)
	start := max(0, m.searchIndex-available+1)
	end := min(len(matches), start+available)
	for i := start; i < end; i++ {
		name := strings.ReplaceAll(safeText(m.stacks[matches[i]].Name), "\n", " ")
		line := "  " + name
		if i == m.searchIndex {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4A1FF")).Bold(true).Render("▸ " + name)
		}
		lines = append(lines, truncate(line, max(1, min(m.width-12, 52))))
	}
	if m.searchQuery != "" && len(matches) == 0 {
		lines = append(lines, "No stacks found")
	}
	lines = append(lines, "", "↑/↓ select  Enter open  Esc cancel")
	return strings.Join(lines, "\n")
}

func (m *model) restoreModal() string {
	d := m.databases[m.databaseIndex]
	b := d.Backups[m.backupIndex]
	prompt := "Restore backup to " + d.Name + "?"
	if m.restoreConfirm == 2 {
		prompt = "FINAL: Replace all data in " + d.Name + "?"
	}
	name := b.Path
	if name == "" {
		name = b.ID
	}
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	return prompt + "\nBackup: " + safeText(name) + "\nBackup time: " + brisbaneBackupTime(b.StartedAt) + "\n\nPress y to confirm; n or Esc to cancel"
}

func (m *model) modal(view, content string) string {
	dialog := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).Width(max(20, min(m.width-6, 56))).Render(content)
	rows := strings.Split(dialog, "\n")
	background := strings.Split(view, "\n")
	top := max(0, (m.height-len(rows))/2)
	left := max(0, (m.width-lipgloss.Width(dialog))/2)
	result := make([]string, max(m.height, len(background)))
	for i := range result {
		line := ""
		if i < len(background) {
			line = regexp.MustCompile(`\[[0-9;]*m`).ReplaceAllString(background[i], "")
		}
		line = runewidth.FillRight(runewidth.Truncate(line, m.width, ""), m.width)
		if i >= top && i < top+len(rows) {
			prefix := runewidth.Truncate(line, left, "")
			suffix := runewidth.TruncateLeft(line, left+lipgloss.Width(dialog), "")
			result[i] = "\x1b[2m" + prefix + "\x1b[0m" + rows[i-top] + "\x1b[2m" + suffix + "\x1b[0m"
		} else {
			result[i] = "\x1b[2m" + line + "\x1b[0m"
		}
	}
	return strings.Join(result, "\n")
}

func main() {
	apiURL := flag.String("api", "http://10.10.10.2:8080", "controller API URL")
	colourFile := flag.String("log-colours", "", "optional JSON log colour configuration")
	flag.Parse()
	client, err := newAPI(*apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	colourData := defaultLogColours
	if *colourFile != "" {
		colourData, err = os.ReadFile(*colourFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	colours, err := parseLogColours(colourData)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := &model{api: client, logColours: colours, status: "Loading stacks..."}
	if _, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
