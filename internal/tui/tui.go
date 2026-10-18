// Package tui is the interactive Bubble Tea interface: a session list, a
// session timeline with a message composer, and a new-session form.
package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/cache"
	"github.com/teaspotato/jules/internal/render"
)

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, c *api.Client, ch cache.Cache) error {
	m := newModel(ctx, c, ch)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}

type view int

const (
	viewList view = iota
	viewSession
	viewNew
)

// Polling intervals (R6): the open session polls fast while active, the
// list slowly; terminal sessions stop; errors back off.
const (
	pollActive   = 4 * time.Second
	pollWaiting  = 15 * time.Second
	pollFinished = 30 * time.Second
	pollList     = 30 * time.Second
	pollMaxDelay = 2 * time.Minute
)

type model struct {
	ctx    context.Context
	client *api.Client
	cache  cache.Cache
	theme  *render.Theme
	st     styles

	width, height int
	view          view
	showHelp      bool

	list    listModel
	session *sessionModel
	form    *formModel

	sources []api.Source

	toast      string
	toastBad   bool
	toastUntil time.Time
}

func newModel(ctx context.Context, c *api.Client, ch cache.Cache) *model {
	th := render.NewTheme(os.Stdout)
	m := &model{ctx: ctx, client: c, cache: ch, theme: th, st: newStyles(th)}
	m.list = newList()
	if ss, ok := ch.Sessions(); ok {
		m.list.setSessions(ss)
		m.list.loading = false
		m.list.stale = true
	}
	if src, ok := ch.Sources(); ok {
		m.sources = src
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetchSessions(), m.fetchSources(), tea.SetWindowTitle("Jules"))
}

// ---- messages --------------------------------------------------------------

type sessionsMsg struct {
	sessions []api.Session
	err      error
}

type sourcesMsg struct {
	sources []api.Source
	err     error
}

type activitiesMsg struct {
	id      string
	acts    []api.Activity
	session *api.Session
	err     error
}

type sentMsg struct {
	id, text string
	err      error
}

type approvedMsg struct {
	id  string
	err error
}

type createdMsg struct {
	session *api.Session
	err     error
}

type listTickMsg struct{ gen int }

type sessionTickMsg struct {
	id  string
	gen int
}

type toastMsg struct {
	text string
	bad  bool
}

// ---- commands --------------------------------------------------------------

func (m *model) fetchSessions() tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		ss, err := c.ListSessions(ctx, false)
		return sessionsMsg{ss, err}
	}
}

func (m *model) fetchSources() tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		src, err := c.ListSources(ctx)
		return sourcesMsg{src, err}
	}
}

func (m *model) fetchActivities(id string, since time.Time) tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		acts, err := c.ListActivitiesSince(ctx, id, since)
		if err != nil {
			return activitiesMsg{id: id, err: err}
		}
		s, err := c.GetSession(ctx, id)
		return activitiesMsg{id: id, acts: acts, session: s, err: err}
	}
}

func (m *model) sendMessage(id, text string) tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		return sentMsg{id, text, c.SendMessage(ctx, id, text)}
	}
}

func (m *model) approvePlan(id string) tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		return approvedMsg{id, c.ApprovePlan(ctx, id)}
	}
}

func (m *model) createSession(req api.CreateSessionRequest) tea.Cmd {
	c, ctx := m.client, m.ctx
	return func() tea.Msg {
		s, err := c.CreateSession(ctx, req)
		return createdMsg{s, err}
	}
}

func tickAfter(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

func (m *model) flash(text string, bad bool) {
	m.toast, m.toastBad = text, bad
	m.toastUntil = time.Now().Add(4 * time.Second)
}

func errText(err error) string {
	if e, ok := err.(*api.Error); ok {
		if h := e.Hint(); h != "" {
			return h
		}
	}
	s := err.Error()
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// ---- update ----------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.resize(m.width, m.height-2)
		if m.session != nil {
			m.session.resize(m, m.width, m.height)
		}
		if m.form != nil {
			m.form.resize(m.width, m.height)
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}

	case toastMsg:
		m.flash(msg.text, msg.bad)
		return m, nil

	case sessionsMsg:
		m.list.loading = false
		if msg.err != nil {
			m.list.err = msg.err
			m.flash(errText(msg.err), true)
			m.list.backoff = min(max(m.list.backoff*2, pollList), pollMaxDelay)
		} else {
			m.list.err = nil
			m.list.stale = false
			m.list.backoff = pollList
			m.list.setSessions(msg.sessions)
			m.cache.PutSessions(msg.sessions)
		}
		m.list.gen++
		return m, tickAfter(m.list.backoff, listTickMsg{m.list.gen})

	case listTickMsg:
		if msg.gen != m.list.gen {
			return m, nil
		}
		return m, m.fetchSessions()

	case sourcesMsg:
		if msg.err == nil {
			m.sources = msg.sources
			m.cache.PutSources(msg.sources)
			if m.form != nil {
				m.form.setSources(msg.sources)
			}
		}
		return m, nil

	case createdMsg:
		if msg.err != nil {
			if m.form != nil {
				m.form.submitting = false
			}
			m.flash("Couldn't start session: "+errText(msg.err), true)
			return m, nil
		}
		m.form = nil
		m.list.upsert(*msg.session)
		m.flash("Session started", false)
		return m, m.openSession(*msg.session)
	}

	switch m.view {
	case viewSession:
		return m, m.updateSession(msg)
	case viewNew:
		return m, m.updateForm(msg)
	default:
		return m, m.updateList(msg)
	}
}

func (m *model) openSession(s api.Session) tea.Cmd {
	cached, _ := m.cache.Activities(s.ID)
	m.session = newSessionModel(s, cached)
	m.session.resize(m, m.width, m.height)
	m.view = viewSession
	return m.session.poll(m, 0)
}

func (m *model) openForm() tea.Cmd {
	m.form = newForm(m.sources, m.cache.Prompts())
	m.form.resize(m.width, m.height)
	m.view = viewNew
	return m.form.init()
}

// ---- view ------------------------------------------------------------------

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	var body string
	switch m.view {
	case viewSession:
		body = m.session.view(m)
	case viewNew:
		body = m.form.view(m)
	default:
		body = m.listView()
	}
	if m.showHelp {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.helpView(),
			lipgloss.WithWhitespaceChars(" "))
	}
	return body
}

// footer renders the toast (if any) or the key hints, one line.
func (m *model) footer(hints string) string {
	if m.toast != "" && time.Now().Before(m.toastUntil) {
		st := m.theme.OK
		if m.toastBad {
			st = m.theme.Bad
		}
		return st.Render(truncate(m.toast, m.width))
	}
	return m.theme.Faint.Render(truncate(hints, m.width))
}
