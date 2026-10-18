package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/browser"
)

// stateFilters cycle with tab.
var stateFilters = []struct {
	name   string
	states map[string]bool
}{
	{"all", nil},
	{"needs attention", map[string]bool{api.StateAwaitingPlanApproval: true, api.StateAwaitingUserFeedback: true}},
	{"active", map[string]bool{api.StateQueued: true, api.StatePlanning: true, api.StateInProgress: true, api.StatePaused: true,
		api.StateAwaitingPlanApproval: true, api.StateAwaitingUserFeedback: true}},
	{"completed", map[string]bool{api.StateCompleted: true}},
	{"failed", map[string]bool{api.StateFailed: true}},
}

type listModel struct {
	all       []api.Session
	visible   []api.Session
	cursor    int
	offset    int
	height    int
	width     int
	filter    int
	search    textinput.Model
	searching bool

	loading bool
	stale   bool // showing cached data while the first fetch runs
	err     error
	gen     int
	backoff time.Duration
}

func newList() listModel {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search title, prompt, repo or id"
	return listModel{loading: true, search: ti, backoff: pollList}
}

func (l *listModel) resize(w, h int) {
	l.width, l.height = w, max(1, h-2) // header + filter line
	l.clamp()
}

func (l *listModel) selected() *api.Session {
	if l.cursor < 0 || l.cursor >= len(l.visible) {
		return nil
	}
	return &l.visible[l.cursor]
}

func (l *listModel) setSessions(ss []api.Session) {
	var keep string
	if s := l.selected(); s != nil {
		keep = s.ID
	}
	l.all = ss
	l.apply()
	if keep != "" {
		for i, s := range l.visible {
			if s.ID == keep {
				l.cursor = i
				break
			}
		}
	}
	l.clamp()
}

// upsert adds or replaces one session (after create or a session refresh).
func (l *listModel) upsert(s api.Session) {
	for i := range l.all {
		if l.all[i].ID == s.ID {
			l.all[i] = s
			l.apply()
			return
		}
	}
	l.all = append([]api.Session{s}, l.all...)
	l.apply()
}

func (l *listModel) apply() {
	f := stateFilters[l.filter].states
	q := strings.ToLower(strings.TrimSpace(l.search.Value()))
	l.visible = l.visible[:0]
	for _, s := range l.all {
		if f != nil && !f[s.State] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Title+"\n"+s.Prompt+"\n"+s.Repo()+"\n"+s.ID), q) {
			continue
		}
		l.visible = append(l.visible, s)
	}
	sort.SliceStable(l.visible, func(i, j int) bool {
		ai, aj := api.NeedsAttention(l.visible[i].State), api.NeedsAttention(l.visible[j].State)
		if ai != aj {
			return ai
		}
		return l.visible[i].UpdateTime.After(l.visible[j].UpdateTime)
	})
	l.clamp()
}

func (l *listModel) clamp() {
	if l.cursor >= len(l.visible) {
		l.cursor = len(l.visible) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.height > 0 && l.cursor >= l.offset+l.height {
		l.offset = l.cursor - l.height + 1
	}
	l.offset = max(0, l.offset)
}

func (l *listModel) move(d int) {
	l.cursor += d
	l.clamp()
}

func (m *model) updateList(msg tea.Msg) tea.Cmd {
	l := &m.list
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if l.searching {
			var cmd tea.Cmd
			l.search, cmd = l.search.Update(msg)
			return cmd
		}
		return nil
	}

	if l.searching {
		switch key.String() {
		case "esc":
			l.searching = false
			l.search.Blur()
			l.search.SetValue("")
			l.apply()
			return nil
		case "enter":
			l.searching = false
			l.search.Blur()
			return nil
		case "up", "ctrl+p":
			l.move(-1)
			return nil
		case "down", "ctrl+n":
			l.move(1)
			return nil
		}
		var cmd tea.Cmd
		l.search, cmd = l.search.Update(msg)
		l.cursor = 0
		l.apply()
		return cmd
	}

	switch key.String() {
	case "q", "esc":
		if key.String() == "esc" && l.search.Value() != "" {
			l.search.SetValue("")
			l.apply()
			return nil
		}
		return tea.Quit
	case "?":
		m.showHelp = true
	case "j", "down":
		l.move(1)
	case "k", "up":
		l.move(-1)
	case "ctrl+d", "pgdown":
		l.move(l.height / 2)
	case "ctrl+u", "pgup":
		l.move(-l.height / 2)
	case "g", "home":
		l.cursor = 0
		l.clamp()
	case "G", "end":
		l.cursor = len(l.visible) - 1
		l.clamp()
	case "/":
		l.searching = true
		return l.search.Focus()
	case "tab":
		l.filter = (l.filter + 1) % len(stateFilters)
		l.cursor = 0
		l.apply()
	case "shift+tab":
		l.filter = (l.filter + len(stateFilters) - 1) % len(stateFilters)
		l.cursor = 0
		l.apply()
	case "r":
		l.loading = true
		return m.fetchSessions()
	case "n":
		return m.openForm()
	case "enter", "l", "right":
		if s := l.selected(); s != nil {
			return m.openSession(*s)
		}
	case "o":
		if s := l.selected(); s != nil {
			return openURL(s.URL)
		}
	case "p":
		if s := l.selected(); s != nil {
			return openPR(*s)
		}
	case "a":
		if s := l.selected(); s != nil {
			if s.State != api.StateAwaitingPlanApproval {
				m.flash("This session isn't waiting for plan approval", true)
				return nil
			}
			return m.approvePlan(s.ID)
		}
	}
	return nil
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		if url == "" {
			return toastMsg{"No URL for this session", true}
		}
		if err := browser.Open(url); err != nil {
			return toastMsg{err.Error(), true}
		}
		return toastMsg{"Opened in browser", false}
	}
}

func openPR(s api.Session) tea.Cmd {
	prs := s.PullRequests()
	if len(prs) == 0 {
		return func() tea.Msg { return toastMsg{"This session has no pull request", true} }
	}
	return openURL(prs[len(prs)-1].URL)
}

func (m *model) listView() string {
	l := &m.list
	t := m.theme

	// Header: brand, counts, refresh state.
	attn := 0
	for _, s := range l.all {
		if api.NeedsAttention(s.State) {
			attn++
		}
	}
	left := m.st.brand.Render("◆ Jules") + t.Faint.Render(fmt.Sprintf("  %d sessions", len(l.all)))
	if attn > 0 {
		left += "  " + t.Warn.Render(fmt.Sprintf("%d need you", attn))
	}
	right := ""
	switch {
	case l.loading && l.stale:
		right = t.Faint.Render("cached · refreshing…")
	case l.loading:
		right = t.Faint.Render("loading…")
	case l.err != nil:
		right = t.Bad.Render("offline")
	}
	header := m.st.header.Render(spread(left, right, m.width-2))

	// Filter line.
	var tabs []string
	for i, f := range stateFilters {
		if i == l.filter {
			tabs = append(tabs, t.Accent.Bold(true).Render("["+f.name+"]"))
		} else {
			tabs = append(tabs, t.Faint.Render(f.name))
		}
	}
	filterLine := " " + strings.Join(tabs, " ")
	if l.searching || l.search.Value() != "" {
		filterLine += "   " + l.search.View()
	}

	// Rows, with a divider between attention and the rest.
	var rows []string
	rowW := m.width - 2
	for i := l.offset; i < len(l.visible) && len(rows) < l.height; i++ {
		s := l.visible[i]
		row := t.SessionRow(s, rowW)
		if i == l.cursor {
			row = m.st.selected.Width(m.width).Render("▌" + row)
		} else {
			row = " " + row
		}
		rows = append(rows, row)
	}
	if len(l.visible) == 0 {
		msg := "No sessions."
		switch {
		case l.loading:
			msg = "Loading sessions…"
		case l.search.Value() != "" || l.filter != 0:
			msg = "No sessions match. Press esc to clear the search or tab to change the filter."
		case l.err != nil:
			msg = "Couldn't load sessions: " + errText(l.err)
		default:
			msg = "No sessions yet. Press n to start one."
		}
		rows = append(rows, "", "  "+t.Faint.Render(msg))
	}

	hints := "enter open · n new · / search · tab filter · a approve · o web · r refresh · ? help · q quit"
	return header + "\n" + filterLine + "\n" + fit(strings.Join(rows, "\n"), l.height) + "\n" + m.footer(hints)
}

// spread places left and right on one line of width w.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}
