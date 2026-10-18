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
	"github.com/teaspotato/jules/internal/render"
)

// Tabs (cycled with tab / shift+tab). Archived sessions appear only in the
// archived tab; the others split live sessions by state.
const (
	tabWorking = iota
	tabBlocked
	tabDone
	tabArchived
	numTabs
)

var tabNames = [numTabs]string{"working", "blocked", "done", "archived"}

// tabOf places a session in exactly one tab.
func tabOf(s api.Session) int {
	switch api.Group(s) {
	case api.GroupArchived:
		return tabArchived
	case api.GroupBlocked:
		return tabBlocked
	case api.GroupDone:
		return tabDone
	}
	return tabWorking
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

	// Selection: marked session IDs, plus a visual range from anchor to
	// cursor while visual is on.
	marked   map[string]bool
	visual   bool
	anchor   int
	anchorID string        // keeps the range anchored across refreshes
	confirm  []api.Session // sessions awaiting delete confirmation

	// Archive/unarchive/delete calls: in flight per session, progress per
	// action, and finished local edits that a list fetch started before
	// they landed must not undo.
	pending map[string]string
	jobs    map[string]*job
	edits   map[string]edit

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
	// Open on what needs you.
	return listModel{loading: true, search: ti, backoff: pollList, marked: map[string]bool{}, filter: tabBlocked,
		pending: map[string]string{}, jobs: map[string]*job{}, edits: map[string]edit{}}
}

// job tracks one action's progress across its in-flight calls.
type job struct {
	total, done, failed int
	err                 error // first failure
}

// edit is a change made locally after a successful call.
type edit struct {
	action string
	at     time.Time
}

// startOp records an in-flight call; false if the session already has one.
func (l *listModel) startOp(action, id string) bool {
	if _, busy := l.pending[id]; busy {
		return false
	}
	l.pending[id] = action
	j := l.jobs[action]
	if j == nil {
		j = &job{}
		l.jobs[action] = j
	}
	j.total++
	return true
}

// finishOp applies a finished call. It returns the action's job once every
// call in it has finished, so the caller can report the outcome.
func (l *listModel) finishOp(action, id string, err error) *job {
	delete(l.pending, id)
	j := l.jobs[action]
	if j == nil {
		return nil
	}
	if err != nil {
		j.failed++
		if j.err == nil {
			j.err = err
		}
	} else {
		j.done++
		l.edits[id] = edit{action, time.Now()}
		l.applyEdit(id, action)
		l.apply()
		l.clamp()
	}
	if j.done+j.failed < j.total {
		return nil
	}
	delete(l.jobs, action)
	return j
}

func (l *listModel) applyEdit(id, action string) {
	if action == actDelete {
		delete(l.marked, id)
		kept := l.all[:0]
		for _, s := range l.all {
			if s.ID != id {
				kept = append(kept, s)
			}
		}
		l.all = kept
		return
	}
	for i := range l.all {
		if l.all[i].ID == id {
			l.all[i].Archived = action == actArchive
			l.all[i].Raw = nil // Raw would re-emit the stale flag
		}
	}
}

// progress describes in-flight calls, e.g. "archiving 3/12", or "".
func (l *listModel) progress() string {
	var parts []string
	for _, a := range []string{actArchive, actUnarchive, actDelete} {
		if j := l.jobs[a]; j != nil {
			parts = append(parts, fmt.Sprintf("%s %d/%d", opVerb[a], j.done+j.failed, j.total))
		}
	}
	return strings.Join(parts, " · ")
}

var opVerb = map[string]string{actArchive: "archiving", actUnarchive: "unarchiving", actDelete: "deleting"}

func (l *listModel) resize(w, h int) {
	l.width, l.height = w, max(1, h-3) // header, filter line, column header
	l.clamp()
}

// inVisual reports whether row i is inside the active visual range.
func (l *listModel) inVisual(i int) bool {
	if !l.visual {
		return false
	}
	lo, hi := min(l.anchor, l.cursor), max(l.anchor, l.cursor)
	return i >= lo && i <= hi
}

// selection returns the marked and visually selected sessions in list order.
func (l *listModel) selection() []api.Session {
	var out []api.Session
	for i, s := range l.visible {
		if l.marked[s.ID] || l.inVisual(i) {
			out = append(out, s)
		}
	}
	return out
}

// targets is what an action applies to: the selection, or else the
// session under the cursor.
func (l *listModel) targets() []api.Session {
	if sel := l.selection(); len(sel) > 0 {
		return sel
	}
	if s := l.selected(); s != nil {
		return []api.Session{*s}
	}
	return nil
}

func (l *listModel) clearSelection() {
	l.marked = map[string]bool{}
	l.visual = false
}

func (l *listModel) selected() *api.Session {
	if l.cursor < 0 || l.cursor >= len(l.visible) {
		return nil
	}
	return &l.visible[l.cursor]
}

// setSessions replaces the list with a fetch that began at started. Local
// edits that landed after that are re-applied so a slow fetch can't bring
// back a session just archived or deleted.
func (l *listModel) setSessions(ss []api.Session, started time.Time) {
	var keep string
	if s := l.selected(); s != nil {
		keep = s.ID
	}
	l.all = ss
	for id, e := range l.edits {
		if e.at.Before(started) {
			delete(l.edits, id) // the fetch already reflects it
			continue
		}
		l.applyEdit(id, e.action)
	}
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
	q := strings.ToLower(strings.TrimSpace(l.search.Value()))
	l.visible = l.visible[:0]
	for _, s := range l.all {
		if tabOf(s) != l.filter {
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
	if l.visual {
		l.visual = false
		for i, s := range l.visible {
			if s.ID == l.anchorID {
				l.visual, l.anchor = true, i
				break
			}
		}
	}
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

	if l.confirm != nil {
		targets := l.confirm
		l.confirm = nil
		if key.String() == "y" || key.String() == "Y" {
			l.clearSelection()
			return m.batch(actDelete, targets)
		}
		m.flash("Delete cancelled", false)
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
		if key.String() == "esc" {
			switch {
			case l.visual || len(l.marked) > 0:
				l.clearSelection()
				return nil
			case l.search.Value() != "":
				l.search.SetValue("")
				l.apply()
				return nil
			}
		}
		return tea.Quit
	case "v":
		if l.visual {
			// Leaving visual mode keeps the range as marks.
			for _, s := range l.selection() {
				l.marked[s.ID] = true
			}
			l.visual = false
		} else if len(l.visible) > 0 {
			l.visual, l.anchor, l.anchorID = true, l.cursor, l.visible[l.cursor].ID
		}
	case "m", " ":
		if s := l.selected(); s != nil {
			if l.marked[s.ID] {
				delete(l.marked, s.ID)
			} else {
				l.marked[s.ID] = true
			}
			l.move(1)
		}
	case "d", "D", "delete", "u":
		targets := l.targets()
		if len(targets) == 0 {
			return nil
		}
		// Archived tab: u unarchives, d deletes. Elsewhere: d archives,
		// D deletes. Only delete asks for confirmation.
		archived := l.filter == tabArchived
		switch k := key.String(); {
		case k == "u" && archived:
			l.clearSelection()
			return m.batch(actUnarchive, targets)
		case k == "d" && !archived:
			l.clearSelection()
			return m.batch(actArchive, targets)
		case k == "D" && !archived, k == "d" && archived, k == "delete":
			l.confirm = targets
		}
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
		l.filter = (l.filter + 1) % numTabs
		l.cursor = 0
		l.apply()
	case "shift+tab":
		l.filter = (l.filter + numTabs - 1) % numTabs
		l.cursor = 0
		l.apply()
	case "1", "2", "3", "4":
		l.filter = int(key.String()[0] - '1')
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
		if !s.Archived && api.NeedsAttention(s.State) {
			attn++
		}
	}
	left := m.st.brand.Render("◆ Jules")
	if attn > 0 {
		left += "  " + t.Warn.Render(fmt.Sprintf("%d need you", attn))
	}
	right := ""
	switch {
	case l.progress() != "":
		right = t.Warn.Render(m.spin.View() + " " + l.progress() + "…")
	case m.activity() != "":
		right = t.Faint.Render(m.activity())
		if l.stale {
			right = t.Faint.Render("cached · ") + right
		}
	case l.err != nil:
		right = t.Bad.Render("offline")
	}
	header := m.st.header.Render(spread(left, right, m.width-2))

	// Filter line.
	var tabs []string
	var counts [numTabs]int
	for _, s := range l.all {
		counts[tabOf(s)]++
	}
	for i, name := range tabNames {
		label := fmt.Sprintf("%s %d", name, counts[i])
		if i == l.filter {
			tabs = append(tabs, t.Accent.Bold(true).Render("["+label+"]"))
		} else {
			tabs = append(tabs, t.Faint.Render(" "+label+" "))
		}
	}
	filterLine := " " + strings.Join(tabs, " ")
	if l.searching || l.search.Value() != "" {
		filterLine += "   " + l.search.View()
	}

	// Rows: a gutter shows the cursor (▌) and marks (●).
	var rows []string
	rowW := m.width - 3
	repoW := render.RepoWidth(l.visible)
	query := l.search.Value()
	for i := l.offset; i < len(l.visible) && len(rows) < l.height; i++ {
		s := l.visible[i]
		gutter := " "
		if l.marked[s.ID] || l.inVisual(i) {
			gutter = t.Warn.Render("●")
		}
		status := ""
		if a, ok := l.pending[s.ID]; ok {
			status = opVerb[a] + "…"
		}
		row := t.SessionRowStatus(s, rowW, repoW, query, status)
		switch {
		case i == l.cursor:
			row = m.st.selected.Width(m.width).Render("▌" + gutter + row)
		case l.inVisual(i):
			row = m.st.selected.Width(m.width).Render(" " + gutter + row)
		default:
			row = " " + gutter + row
		}
		rows = append(rows, row)
	}
	if len(l.visible) == 0 {
		msg := "No sessions."
		switch {
		case l.loading:
			msg = "Loading sessions…"
		case l.search.Value() != "":
			msg = "No sessions match. Press esc to clear the search or tab to switch tabs."
		case len(l.all) > 0:
			msg = "Nothing " + tabNames[l.filter] + ". Press tab to switch tabs."
		case l.err != nil:
			msg = "Couldn't load sessions: " + errText(l.err)
		default:
			msg = "No sessions yet. Press n to start one."
		}
		rows = append(rows, "", "  "+t.Faint.Render(msg))
	}

	columns := "  " + t.SessionColumns(rowW, repoW)

	actions := "d archive · D delete"
	if l.filter == tabArchived {
		actions = "u unarchive · d delete"
	}
	hints := "enter open · n new · / search · 1-4/tab switch · v range · m mark · " + actions + " · ? help · q quit"
	if sel := len(l.selection()); sel > 0 || l.visual {
		hints = fmt.Sprintf("%d selected · v %s · m mark · "+actions+" · esc clear", sel,
			map[bool]string{true: "end range", false: "range"}[l.visual])
	}
	footer := m.footer(hints)
	if l.confirm != nil {
		what := "this session"
		if len(l.confirm) > 1 {
			what = fmt.Sprintf("%d sessions", len(l.confirm))
		} else {
			what = fmt.Sprintf("%q", truncate(l.confirm[0].DisplayTitle(), 50))
		}
		footer = t.Bad.Render("Delete "+what+"? This can't be undone.") + t.Faint.Render("  y to delete · any other key cancels")
	}
	return header + "\n" + filterLine + "\n" + columns + "\n" + fit(strings.Join(rows, "\n"), l.height) + "\n" + footer
}

// spread places left and right on one line of width w.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}
