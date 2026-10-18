package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/teaspotato/jules/internal/api"
)

type field int

const (
	fieldRepo field = iota
	fieldBranch
	fieldTitle
	fieldPrompt
	fieldApproval
	fieldAutoPR
	numFields
)

type formModel struct {
	sources []api.Source
	recent  []string
	recentI int

	repo, branch, title textinput.Model
	prompt              textarea.Model
	approval, autoPR    bool

	focus      field
	pick       int // highlighted suggestion for repo/branch
	submitting bool
	width      int
	height     int
}

func newForm(sources []api.Source, recent []string) *formModel {
	mk := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		return ti
	}
	ta := textarea.New()
	ta.Placeholder = "Describe the task for Jules (markdown is fine)…"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	f := &formModel{
		sources: sources,
		recent:  recent,
		recentI: -1,
		repo:    mk("owner/repo (type to search; leave empty for no repo)"),
		branch:  mk("default branch"),
		title:   mk("optional; Jules generates one"),
		prompt:  ta,
	}
	return f
}

func (f *formModel) init() tea.Cmd { return f.focusField(fieldRepo) }

func (f *formModel) setSources(s []api.Source) { f.sources = s }

func (f *formModel) resize(w, h int) {
	f.width, f.height = w, h
	iw := max(10, w-20)
	f.repo.Width, f.branch.Width, f.title.Width = iw, iw, iw
	f.prompt.SetWidth(max(10, w-6))
	f.prompt.SetHeight(max(3, h-22))
}

func (f *formModel) focusField(n field) tea.Cmd {
	f.repo.Blur()
	f.branch.Blur()
	f.title.Blur()
	f.prompt.Blur()
	f.focus = n
	f.pick = 0
	switch n {
	case fieldRepo:
		return f.repo.Focus()
	case fieldBranch:
		return f.branch.Focus()
	case fieldTitle:
		return f.title.Focus()
	case fieldPrompt:
		return f.prompt.Focus()
	}
	return nil
}

// repoMatches filters sources by the typed text.
func (f *formModel) repoMatches() []api.Source {
	q := strings.ToLower(strings.TrimSpace(f.repo.Value()))
	var out []api.Source
	for _, s := range f.sources {
		if q == "" || strings.Contains(strings.ToLower(s.Slug()), q) {
			out = append(out, s)
		}
	}
	return out
}

// source returns the source exactly matching the repo field, if any.
func (f *formModel) source() *api.Source {
	v := strings.TrimSpace(f.repo.Value())
	for i, s := range f.sources {
		if strings.EqualFold(s.Slug(), v) {
			return &f.sources[i]
		}
	}
	return nil
}

func (f *formModel) branchMatches() []string {
	src := f.source()
	if src == nil || src.GitHubRepo == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(f.branch.Value()))
	var out []string
	for _, b := range src.GitHubRepo.Branches {
		if q == "" || strings.Contains(strings.ToLower(b.DisplayName), q) {
			out = append(out, b.DisplayName)
		}
	}
	return out
}

func (f *formModel) defaultBranch() string {
	if src := f.source(); src != nil && src.GitHubRepo != nil && src.GitHubRepo.DefaultBranch != nil {
		return src.GitHubRepo.DefaultBranch.DisplayName
	}
	return ""
}

func (f *formModel) request() (api.CreateSessionRequest, string) {
	req := api.CreateSessionRequest{
		Prompt:              strings.TrimSpace(f.prompt.Value()),
		Title:               strings.TrimSpace(f.title.Value()),
		RequirePlanApproval: f.approval,
	}
	if req.Prompt == "" {
		return req, "Write a prompt first"
	}
	if f.autoPR {
		req.AutomationMode = api.AutomationAutoCreatePR
	}
	if repo := strings.TrimSpace(f.repo.Value()); repo != "" {
		src := f.source()
		if src == nil {
			return req, "Pick a repository from the list"
		}
		branch := strings.TrimSpace(f.branch.Value())
		if branch == "" {
			branch = f.defaultBranch()
		}
		req.SourceContext = &api.SourceContext{
			Source:            src.Name,
			GitHubRepoContext: &api.GitHubRepoContext{StartingBranch: branch},
		}
	}
	return req, ""
}

func (m *model) updateForm(msg tea.Msg) tea.Cmd {
	f := m.form
	if f == nil {
		m.view = viewList
		return nil
	}
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return f.updateFocused(msg)
	}
	if f.submitting {
		return nil
	}
	switch k.String() {
	case "esc":
		m.form = nil
		m.view = viewList
		return nil
	case "ctrl+s":
		req, problem := f.request()
		if problem != "" {
			m.flash(problem, true)
			return nil
		}
		f.submitting = true
		m.cache.AddPrompt(req.Prompt)
		return m.createSession(req)
	case "tab":
		f.accept()
		return f.focusField((f.focus + 1) % numFields)
	case "shift+tab":
		return f.focusField((f.focus + numFields - 1) % numFields)
	case "ctrl+r":
		if len(f.recent) > 0 {
			f.recentI = (f.recentI + 1) % len(f.recent)
			f.prompt.SetValue(f.recent[f.recentI])
			m.flash("Recent prompt "+strconv.Itoa(f.recentI+1)+" of "+strconv.Itoa(len(f.recent)), false)
		}
		return nil
	}

	switch f.focus {
	case fieldRepo, fieldBranch:
		n := len(f.repoMatches())
		if f.focus == fieldBranch {
			n = len(f.branchMatches())
		}
		switch k.String() {
		case "up", "ctrl+p":
			f.pick = max(0, f.pick-1)
			return nil
		case "down", "ctrl+n":
			f.pick = min(max(0, n-1), f.pick+1)
			return nil
		case "enter":
			f.accept()
			return f.focusField(f.focus + 1)
		}
		f.pick = 0
	case fieldTitle:
		if k.String() == "enter" {
			return f.focusField(fieldPrompt)
		}
	case fieldApproval, fieldAutoPR:
		switch k.String() {
		case " ", "x", "enter":
			if f.focus == fieldApproval {
				f.approval = !f.approval
			} else {
				f.autoPR = !f.autoPR
			}
		case "j", "down":
			return f.focusField((f.focus + 1) % numFields)
		case "k", "up":
			return f.focusField(f.focus - 1)
		}
		return nil
	}
	return f.updateFocused(msg)
}

// accept fills the repo/branch field with the highlighted suggestion.
func (f *formModel) accept() {
	switch f.focus {
	case fieldRepo:
		if f.source() != nil || strings.TrimSpace(f.repo.Value()) == "" {
			return
		}
		if ms := f.repoMatches(); f.pick < len(ms) {
			f.repo.SetValue(ms[f.pick].Slug())
			f.branch.SetValue("")
			f.branch.Placeholder = f.defaultBranch() + " (default)"
		}
	case fieldBranch:
		if strings.TrimSpace(f.branch.Value()) == "" {
			return
		}
		if ms := f.branchMatches(); f.pick < len(ms) {
			f.branch.SetValue(ms[f.pick])
		}
	}
}

func (f *formModel) updateFocused(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch f.focus {
	case fieldRepo:
		f.repo, cmd = f.repo.Update(msg)
		if src := f.source(); src != nil {
			f.branch.Placeholder = f.defaultBranch() + " (default)"
		}
	case fieldBranch:
		f.branch, cmd = f.branch.Update(msg)
	case fieldTitle:
		f.title, cmd = f.title.Update(msg)
	case fieldPrompt:
		f.prompt, cmd = f.prompt.Update(msg)
	}
	return cmd
}

func (f *formModel) view(m *model) string {
	t := m.theme
	label := func(n field, s string) string {
		if f.focus == n {
			return m.st.key.Render("▸ " + padTo(s, 10))
		}
		return t.Faint.Render("  " + padTo(s, 10))
	}
	check := func(n field, on bool, s string) string {
		box := "[ ]"
		if on {
			box = "[x]"
		}
		line := box + " " + s
		if f.focus == n {
			return m.st.key.Render("▸ " + line)
		}
		return "  " + line
	}

	var b strings.Builder
	b.WriteString(m.st.header.Render(m.st.brand.Render("◆ New session")) + "\n\n")
	b.WriteString(label(fieldRepo, "Repo") + " " + f.repo.View() + "\n")
	if f.focus == fieldRepo && f.source() == nil {
		b.WriteString(f.suggestions(m, slugs(f.repoMatches()), 6))
	}
	b.WriteString(label(fieldBranch, "Branch") + " " + f.branch.View() + "\n")
	if f.focus == fieldBranch {
		b.WriteString(f.suggestions(m, f.branchMatches(), 6))
	}
	b.WriteString(label(fieldTitle, "Title") + " " + f.title.View() + "\n\n")
	b.WriteString(label(fieldPrompt, "Prompt") + "\n")
	box := m.st.border
	if f.focus == fieldPrompt {
		box = m.st.focus
	}
	b.WriteString(box.Width(m.width-4).Render(f.prompt.View()) + "\n")
	b.WriteString(check(fieldApproval, f.approval, "Require plan approval before Jules starts work") + "\n")
	b.WriteString(check(fieldAutoPR, f.autoPR, "Open a pull request automatically when done") + "\n")

	hints := "tab next field · ↑/↓ pick · space toggle · ctrl+r recent prompt · ctrl+s start · esc cancel"
	if f.submitting {
		hints = "Starting session…"
	}
	body := b.String()
	return fit(body, m.height-1) + "\n" + m.footer(hints)
}

func (f *formModel) suggestions(m *model, items []string, n int) string {
	if len(items) == 0 {
		return m.theme.Faint.Render("             no matches") + "\n"
	}
	start := 0
	if f.pick >= n {
		start = f.pick - n + 1
	}
	var b strings.Builder
	for i := start; i < len(items) && i < start+n; i++ {
		if i == f.pick {
			b.WriteString("             " + m.st.selected.Render(" "+items[i]+" ") + "\n")
		} else {
			b.WriteString("             " + m.theme.Faint.Render(" "+items[i]) + "\n")
		}
	}
	if more := len(items) - (start + n); more > 0 {
		b.WriteString(m.theme.Faint.Render("              … "+strconv.Itoa(more)+" more") + "\n")
	}
	return b.String()
}

func slugs(ss []api.Source) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Slug()
	}
	return out
}
