// Package cli wires the Cobra subcommands. Every TUI action has a
// subcommand here so the client is fully scriptable.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/auth"
	"github.com/teaspotato/jules/internal/cache"
	"github.com/teaspotato/jules/internal/render"
	"github.com/teaspotato/jules/internal/tui"
)

// Exit codes. Scripts can rely on these.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitAuth     = 3
	ExitNotFound = 4
	ExitFailed   = 5 // `wait`: session failed
	ExitTimeout  = 6 // `wait`: timed out
)

// ExitError carries a specific exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withCode(code int, err error) error { return &exitError{code, err} }

// app is shared state for one invocation.
type app struct {
	jsonOut bool
	out     io.Writer
	errOut  io.Writer
	cache   cache.Cache
	client  *api.Client
	version string
}

func (a *app) api() (*api.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	key, _, err := auth.Key()
	if err != nil {
		return nil, withCode(ExitAuth, err)
	}
	a.client = api.New(key)
	a.client.UserAgent = "jules-tui/" + a.version
	return a.client, nil
}

func (a *app) theme() *render.Theme { return render.NewTheme(a.out) }

// tty reports whether stdout is a terminal.
func (a *app) tty() bool {
	f, ok := a.out.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	if a.tty() {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// width of the terminal, or 100 when not a TTY.
func (a *app) width() int {
	if f, ok := a.out.(*os.File); ok {
		if w := termWidth(f); w > 0 {
			return w
		}
	}
	return 100
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{out: os.Stdout, errOut: os.Stderr, cache: cache.Default(), version: version}
	root := newRoot(a)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	fmt.Fprintln(os.Stderr, "jules: "+err.Error())
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if h := apiErr.Hint(); h != "" {
			fmt.Fprintln(os.Stderr, "hint: "+h)
		}
	}
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		return ee.code
	case api.IsAuth(err):
		return ExitAuth
	case api.IsNotFound(err):
		return ExitNotFound
	case strings.HasPrefix(err.Error(), "unknown command"), strings.HasPrefix(err.Error(), "unknown flag"),
		strings.Contains(err.Error(), "accepts"), strings.Contains(err.Error(), "required flag"):
		return ExitUsage
	}
	return ExitError
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "jules",
		Short: "A fast terminal client for Google Jules",
		Long: `jules is a terminal client for Google Jules.

Run it with no arguments for the interactive TUI, or use the subcommands
from scripts. Add --json to any command for machine-readable output.

The API key comes from JULES_API_KEY or the OS keychain (jules auth login).`,
		Version:       a.version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			return tui.Run(cmd.Context(), c, a.cache)
		},
	}
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "machine-readable JSON output (NDJSON for streams)")
	root.AddCommand(
		newSourcesCmd(a),
		newSessionsCmd(a),
		newShowCmd(a),
		newNewCmd(a),
		newSendCmd(a),
		newApproveCmd(a),
		newWaitCmd(a),
		newWatchCmd(a),
		newPatchCmd(a),
		newArchiveCmd(a, true),
		newArchiveCmd(a, false),
		newDeleteCmd(a),
		newOpenCmd(a),
		newAuthCmd(a),
	)
	return root
}
