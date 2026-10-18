# jules

A fast, keyboard-first terminal client for [Google Jules](https://jules.google.com), built on the
[Jules REST API](https://developers.google.com/jules/api).

It does the two things the official client doesn't: **read a session's full content** (plan,
messages, progress, diffs, command output) and **message existing sessions**. Every action in the
TUI is also a subcommand, so it scripts cleanly.

```
◆ Jules  373 sessions  74 need you
[all] needs attention active completed failed
▌needs input     Investigate flaky integration test      acme/api              18m
 needs input     Scan recent commits for bugs            acme/app                2d
 completed  ⇡    Fix XSS in escapeHtml                   acme/app               52m
```

## Install

```sh
go install github.com/teaspotato/jules/cmd/jules@latest
# or, from a checkout
make build
```

## Authenticate

Create an API key in the Jules web app (Settings, API keys), then either:

```sh
jules auth login            # validates the key, stores it in the OS keychain
export JULES_API_KEY=...    # or use the environment (CI, scripts)
jules auth status           # shows where the key comes from and checks it
```

The key is never written to a config file or logged.

## Interactive TUI

Run `jules` with no arguments. It opens instantly from a local cache, then refreshes in the
background. Press `?` for help.

| View | Keys |
|---|---|
| Session list | `j/k` move, `enter` open, `/` search, `tab` cycle state filter, `n` new session, `a` approve plan, `o` open in browser, `p` open PR, `r` refresh, `q` quit |
| Session | `j/k`, `ctrl+d/u`, `g/G` scroll, `i` or `m` write a message (`enter` sends, `alt+enter` newline), `a` approve plan, `d` full diffs, `e` expand prompt and output, `y` copy latest patch, `o`/`p` open session/PR, `esc` back |
| New session | `tab` next field, `↑/↓` pick repo or branch, `space` toggle option, `ctrl+r` recall a recent prompt, `ctrl+s` start, `esc` cancel |

Sessions that need you (awaiting plan approval or input) are pinned to the top. An open session
polls every few seconds while Jules is working, more slowly while it waits for you, and stops when
it finishes. Messages you send appear immediately and are reconciled when Jules records them.

## Scripting

Every command takes `--json` (one JSON document, or NDJSON for streams). Errors go to stderr.
Sessions can be named by full name (`sessions/123`), bare ID, web URL, or a unique ID prefix.

```sh
jules sources                                   # connected repos
jules sessions --state awaiting                 # awaiting, approval, input, active, completed, failed, …
jules sessions --repo owner/repo --json | jq -r '.[].id'
jules show 3186 [--diff] [--full] [--follow]    # timeline; --follow tails it like tail -f
jules new --repo owner/repo [--branch b] [--title t] [--approve-plan] [--auto-pr] "prompt"
echo "prompt" | jules new --repo owner/repo     # prompt from stdin
jules send 3186 "Please also add tests" --wait  # block until Jules replies, then print it
jules approve 3186
jules wait 3186 --until needs-input             # or: completed (default), or a state name
jules patch 3186 | git apply                    # latest change set as a unified diff
jules open 3186 [--pr]
jules archive 3186 / jules unarchive 3186 / jules delete 3186 [--yes]
```

Exit codes: `0` ok, `1` error, `2` usage, `3` auth, `4` not found, `5` session failed (`wait`),
`6` timed out (`wait`, `send --wait`).

Environment: `JULES_API_KEY`, `NO_COLOR`, `JULES_NO_CACHE=1` to disable the cache.

## Notes on the API

The API is `v1alpha` and the client is written to tolerate change: unknown activity and artifact
types render as raw JSON instead of failing, and `--json` re-emits exactly what the API returned.
Findings from probing the live API that shaped the client:

- There is no push or streaming, so the client polls, using the `create_time` filter on
  `activities.list` to fetch only new activities.
- Change sets are cumulative: Jules attaches the whole current diff to most progress updates. The
  timeline shows a diff only when it changed, and always shows the final one.
- The initial prompt is not an activity, so the timeline shows it as your first message.
- `sendMessage` returns nothing; replies are matched by order (the first agent activity after
  your message). The echo of your message shows up within a few seconds.
- Messaging a recently completed session resumes it: the echo appears within seconds, the
  session stays COMPLETED for about a minute, then goes back to IN_PROGRESS and Jules replies.
  `send --wait` waits through that gap. A session finished months earlier turned FAILED instead.
  Finished sessions keep polling slowly in the TUI, so replies (including ones sent from the web
  app) show up without reopening them.
- The discovery document (`api/discovery-v1alpha.json`) exposes archive, unarchive and delete,
  which aren't in the public reference; this client supports them.

## Layout

```
cmd/jules           entry point
internal/api        typed REST client, pagination, retries, incremental timeline
internal/render     shared rendering (markdown, plans, diffs, bash output)
internal/cli        Cobra subcommands
internal/tui        Bubble Tea views: list, session, new session
internal/cache      on-disk cache for instant startup and offline reading
internal/auth       key lookup (env, OS keychain)
```
