// Package cache keeps sessions and activities on disk so the TUI opens
// instantly and works read-only offline. Writes are atomic; any read error
// is treated as a cache miss.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/teaspotato/jules/internal/api"
)

// Cache is rooted at a directory; the zero value disables caching.
type Cache struct{ Dir string }

// Default uses $XDG_CACHE_HOME/jules (or the OS equivalent). Set
// JULES_NO_CACHE=1 to disable.
func Default() Cache {
	if os.Getenv("JULES_NO_CACHE") != "" {
		return Cache{}
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return Cache{}
	}
	return Cache{Dir: filepath.Join(d, "jules")}
}

func (c Cache) read(name string, v any) bool {
	if c.Dir == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, name))
	return err == nil && json.Unmarshal(b, v) == nil
}

func (c Cache) write(name string, v any) {
	if c.Dir == "" {
		return
	}
	path := filepath.Join(c.Dir, name)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

func (c Cache) Sessions() ([]api.Session, bool) {
	var s []api.Session
	ok := c.read("sessions.json", &s)
	return s, ok
}

func (c Cache) PutSessions(s []api.Session) { c.write("sessions.json", s) }

func (c Cache) Sources() ([]api.Source, bool) {
	var s []api.Source
	ok := c.read("sources.json", &s)
	return s, ok
}

func (c Cache) PutSources(s []api.Source) { c.write("sources.json", s) }

func activitiesFile(id string) string {
	return filepath.Join("activities", strings.ReplaceAll(strings.TrimPrefix(id, "sessions/"), "/", "_")+".json")
}

func (c Cache) Activities(sessionID string) ([]api.Activity, bool) {
	var a []api.Activity
	ok := c.read(activitiesFile(sessionID), &a)
	return a, ok
}

func (c Cache) PutActivities(sessionID string, a []api.Activity) {
	c.write(activitiesFile(sessionID), a)
}

// Prompts are recent prompts typed in the new-session form.
func (c Cache) Prompts() []string {
	var p []string
	c.read("prompts.json", &p)
	return p
}

func (c Cache) AddPrompt(p string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return
	}
	prompts := []string{p}
	for _, old := range c.Prompts() {
		if old != p && len(prompts) < 50 {
			prompts = append(prompts, old)
		}
	}
	c.write("prompts.json", prompts)
}
