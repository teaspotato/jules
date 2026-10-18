package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/browser"
	"github.com/teaspotato/jules/internal/render"
)

// pending is a message sent from this client that hasn't shown up in the
// timeline yet (sendMessage returns nothing, so we show it optimistically).
type pending struct {
	text   string
	sentAt time.Time
	failed bool
}

type sessionModel struct {
	s         api.Session
	tl        *api.Timeline
	vp        viewport.Model
	composer  textarea.Model
	spin      spinner.Model
	composing bool

	expandDiffs bool
	expand      bool
	cache       map[string]string
	pending     []pending
	waiting     bool // waiting for the agent's reply to a sent message

	loading bool
	err     error
	gen     int
	backoff time.Duration
	width   int
	height  int
}

func newSessionModel(s api.Session, cached []api.Activity) *sessionModel {
	ta := textarea.New()
	ta.Placeholder = "Message Jules… (enter to send, alt+enter for a newline)"
	ta.ShowLineNumbers = false
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sm := &sessionModel{
		s:        s,
		tl:       api.NewTimeline(s.ID, cached),
		vp:       viewport.New(0, 0),
		composer: ta,
		spin:     sp,
		cache:    map[string]string{},
		loading:  true,
		backoff:  pollActive,
	}
	return sm
}

func (sm *sessionModel) headerHeight(m *model) int {
	return lipgloss.Height(sm.header(m))
}

func (sm *sessionModel) header(m *model) string {
	return m.st.header.Render(m.theme.SessionHeader(&sm.s, m.width-2))
}

func (sm *sessionModel) composerHeight() int {
	return sm.composer.Height() + 2 // border
}

func (sm *sessionModel) resize(m *model, w, h int) {
	sm.width, sm.height = w, h
	sm.composer.SetWidth(max(10, w-4))
	vh := h - sm.headerHeight(m) - sm.composerHeight() - 2 // divider + footer
	atBottom := sm.vp.AtBottom()
	sm.vp.Width, sm.vp.Height = w, max(1, vh)
	sm.refresh(m, atBottom)
}

// interval picks the next poll delay from the session's state.
func (sm *sessionModel) interval() time.Duration {
	switch {
	case sm.waiting:
		return 3 * time.Second
	case api.NeedsAttention(sm.s.State) || sm.s.State == api.StatePaused:
		return pollWaiting
	case api.IsTerminal(sm.s.State):
		// Finished sessions can still be resumed (from here or the web),
		// so keep checking, slowly.
		return pollFinished
	}
	return pollActive
}

// poll schedules the next fetch (immediately when after is 0).
func (sm *sessionModel) poll(m *model, after time.Duration) tea.Cmd {
	sm.gen++
	gen, id := sm.gen, sm.s.ID
	if after == 0 {
		return tea.Batch(m.fetchActivities(id, sm.tl.Since()), sm.spin.Tick)
	}
	return tickAfter(after, sessionTickMsg{id, gen})
}

// refresh re-renders the timeline into the viewport.
func (sm *sessionModel) refresh(m *model, stickToBottom bool) {
	o := render.Options{
		Width:       sm.vp.Width - 2,
		ExpandDiffs: sm.expandDiffs,
		ExpandBash:  sm.expand,
		FullPrompt:  sm.expand,
		Cache:       sm.cache,
	}
	t := m.theme
	content := t.Timeline(&sm.s, sm.tl.Activities, o)
	for _, p := range sm.pending {
		label := t.Faint.Render("sending…")
		if p.failed {
			label = t.Bad.Render("not sent")
		}
		content += "\n\n" + t.User.Render("● You") + " " + label + "\n" + render.Indent(t.Markdown(p.text, o.Width-2), "  ")
	}
	if sm.loading && len(sm.tl.Activities) == 0 {
		if content != "" {
			content += "\n\n"
		}
		content += t.Faint.Render("Loading timeline…")
	} else if !api.IsTerminal(sm.s.State) && !api.NeedsAttention(sm.s.State) || sm.waiting {
		content += "\n\n" + t.Accent.Render(sm.spin.View()+" Jules is working…")
	}
	if api.NeedsAttention(sm.s.State) && !sm.waiting && !sm.loading {
		hint := "Jules is waiting for your reply. Press i to write one."
		if sm.s.State == api.StateAwaitingPlanApproval {
			hint = "Jules is waiting for plan approval. Press a to approve, or i to request changes."
		}
		content += "\n\n" + t.Warn.Render("▸ "+hint)
	}
	sm.vp.SetContent(render.Indent(content, " ") + "\n")
	if stickToBottom {
		sm.vp.GotoBottom()
	}
}

// reconcile drops optimistic messages once the timeline contains them.
func (sm *sessionModel) reconcile(fresh []api.Activity) {
	for _, a := range fresh {
		if a.UserMessaged == nil {
			if a.Originator != "user" && a.PlanApproved == nil {
				sm.waiting = false
			}
			continue
		}
		got := strings.TrimSpace(a.UserMessaged.UserMessage)
		for i, p := range sm.pending {
			if strings.TrimSpace(p.text) == got {
				sm.pending = append(sm.pending[:i], sm.pending[i+1:]...)
				break
			}
		}
	}
	// If the echo never arrives (or is reworded), stop showing it once
	// the agent has responded.
	if !sm.waiting {
		kept := sm.pending[:0]
		for _, p := range sm.pending {
			if p.failed {
				kept = append(kept, p)
			}
		}
		sm.pending = kept
	}
}

func (m *model) updateSession(msg tea.Msg) tea.Cmd {
	sm := m.session
	switch msg := msg.(type) {
	case activitiesMsg:
		if msg.id != sm.s.ID {
			return nil
		}
		sm.loading = false
		if msg.err != nil {
			sm.err = msg.err
			sm.backoff = min(max(sm.backoff*2, pollActive), pollMaxDelay)
			m.flash(errText(msg.err), true)
			return sm.poll(m, sm.backoff)
		}
		sm.err = nil
		sm.backoff = pollActive
		atBottom := sm.vp.AtBottom()
		first := len(sm.tl.Activities) == 0
		fresh := sm.tl.Merge(msg.acts)
		hdrBefore := sm.headerHeight(m)
		if msg.session != nil {
			sm.s = *msg.session
			m.list.upsert(*msg.session)
		}
		if len(fresh) > 0 {
			m.cache.PutActivities(sm.s.ID, sm.tl.Activities)
			sm.reconcile(fresh)
		}
		if sm.headerHeight(m) != hdrBefore {
			sm.resize(m, sm.width, sm.height)
		}
		sm.refresh(m, atBottom || first)
		return sm.poll(m, sm.interval())

	case toggleDiffsMsg:
		if msg.id != sm.s.ID {
			return nil
		}
		sm.expandDiffs = !sm.expandDiffs
		sm.refresh(m, false)
		if sm.expandDiffs && sm.changeSet() == nil {
			m.flash("No changes in this session yet", false)
		}
		return nil

	case sessionTickMsg:
		if msg.id != sm.s.ID || msg.gen != sm.gen {
			return nil
		}
		return sm.poll(m, 0)

	case spinner.TickMsg:
		if api.IsTerminal(sm.s.State) && !sm.waiting {
			return nil
		}
		var cmd tea.Cmd
		sm.spin, cmd = sm.spin.Update(msg)
		sm.refresh(m, sm.vp.AtBottom())
		return cmd

	case sentMsg:
		if msg.id != sm.s.ID {
			return nil
		}
		if msg.err != nil {
			for i := range sm.pending {
				if sm.pending[i].text == msg.text {
					sm.pending[i].failed = true
				}
			}
			sm.waiting = false
			m.flash("Message not sent: "+errText(msg.err), true)
			sm.refresh(m, true)
			return nil
		}
		m.flash("Sent", false)
		return sm.poll(m, 2*time.Second)

	case approvedMsg:
		if msg.err != nil {
			m.flash("Couldn't approve: "+errText(msg.err), true)
			return nil
		}
		m.flash("Plan approved", false)
		sm.waiting = true
		return sm.poll(m, time.Second)

	case tea.KeyMsg:
		if sm.composing {
			return sm.updateComposer(m, msg)
		}
		return sm.updateKeys(m, msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		sm.vp, cmd = sm.vp.Update(msg)
		return cmd
	}
	return nil
}

func (sm *sessionModel) updateComposer(m *model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		sm.composing = false
		sm.composer.Blur()
		return nil
	case "enter":
		text := strings.TrimSpace(sm.composer.Value())
		if text == "" {
			return nil
		}
		sm.composer.Reset()
		sm.composing = false
		sm.composer.Blur()
		sm.pending = append(sm.pending, pending{text: text, sentAt: time.Now()})
		sm.waiting = true
		sm.refresh(m, true)
		return tea.Batch(m.sendMessage(sm.s.ID, text), sm.spin.Tick)
	}
	var cmd tea.Cmd
	sm.composer, cmd = sm.composer.Update(k)
	return cmd
}

func (sm *sessionModel) updateKeys(m *model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "h", "left", "backspace":
		m.view = viewList
		m.session = nil
		return nil
	case "?":
		m.showHelp = true
	case "i", "m":
		sm.composing = true
		return sm.composer.Focus()
	case "a":
		if sm.s.State != api.StateAwaitingPlanApproval {
			m.flash("No plan is waiting for approval", true)
			return nil
		}
		return m.approvePlan(sm.s.ID)
	case "d":
		// Big diffs take a moment to render: show the indicator for a
		// frame first, then render.
		label := "rendering diffs"
		if sm.expandDiffs {
			label = "collapsing diffs"
		}
		id := sm.s.ID
		return m.track(label, func() tea.Msg {
			time.Sleep(30 * time.Millisecond)
			return toggleDiffsMsg{id}
		})
	case "e":
		sm.expand = !sm.expand
		sm.refresh(m, false)
	case "g", "home":
		sm.vp.GotoTop()
	case "G", "end":
		sm.vp.GotoBottom()
	case "r":
		return sm.poll(m, 0)
	case "o":
		return openURL(sm.s.URL)
	case "p":
		return openPR(sm.s)
	case "y":
		cs := sm.changeSet()
		if cs == nil {
			m.flash("No changes to copy yet", true)
			return nil
		}
		patch := cs.GitPatch.UnidiffPatch
		if strings.TrimSpace(patch) == "" {
			m.flash("The diff is empty; nothing to copy", true)
			return nil
		}
		return func() tea.Msg {
			if err := browser.Copy(patch); err != nil {
				return toastMsg{"Copy failed: " + err.Error(), true}
			}
			return toastMsg{"Patch copied to clipboard", false}
		}
	default:
		var cmd tea.Cmd
		sm.vp, cmd = sm.vp.Update(k)
		return cmd
	}
	return nil
}

// toggleDiffsMsg flips diff expansion once the indicator has been drawn.
type toggleDiffsMsg struct{ id string }

// changeSet is the session's final change set, else the latest one in the
// timeline, or nil.
func (sm *sessionModel) changeSet() *api.ChangeSet {
	for i := len(sm.s.Outputs) - 1; i >= 0; i-- {
		if o := sm.s.Outputs[i].ChangeSet; o != nil && o.GitPatch != nil {
			return o
		}
	}
	return api.LatestChangeSet(sm.tl.Activities)
}

func (sm *sessionModel) view(m *model) string {
	t := m.theme
	box := m.st.border
	if sm.composing {
		box = m.st.focus
	}
	composer := box.Width(m.width - 2).Render(sm.composer.View())

	scroll := ""
	if sm.vp.TotalLineCount() > sm.vp.Height {
		scroll = t.Faint.Render(" " + percent(sm.vp.ScrollPercent()))
	}
	if a := m.activity(); a != "" {
		scroll = t.Accent.Render(" "+a+" ") + scroll
	}
	divider := t.Faint.Render(strings.Repeat("─", max(0, m.width-lipgloss.Width(scroll)))) + scroll

	hints := "i message · a approve · d diffs · e expand · y copy patch · o web · p PR · esc back · ? help"
	if sm.composing {
		hints = "enter send · alt+enter newline · esc stop typing"
	}
	return sm.header(m) + "\n" + divider + "\n" + sm.vp.View() + "\n" + composer + "\n" + m.footer(hints)
}

func percent(f float64) string {
	return fmt.Sprintf("%d%%", int(f*100+0.5))
}
