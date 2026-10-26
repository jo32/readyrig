---
name: readyrig-local-agents
description: Find the coding agents installed on a computer connected through ReadyRig (Claude Code, Codex, Gemini CLI, Cursor Agent, opencode, Aider and others) and hand one of them a task in non-interactive mode with exec_command. Use when the user asks which agents a ReadyRig computer has, or asks for Codex, Claude, Gemini, Cursor or another agent on that computer to do, review or answer something.
---

# Local agents on a ReadyRig computer

You reach the computer through ReadyRig tools: MCP `call_computer_tool`, or HTTP `POST {gateway}/api/v1/tools/{tool}`. Agent CLIs on that computer are ordinary programs. Find and call them with `exec_command`; no other tool is needed.

## Before you start

- The terminal capability must be on. Run `list_projects`, pick the project the task is about, and pass it (`project`, or `cwd`) to every command.
- A local agent runs with the user's full account. ReadyRig's folder limits don't apply to what it does, and the activity log shows only the outer command. Say this to the user the first time you call one, and limit the agent with its own permission flags (table below).
- Start read-only. Allow edits only when the task asks for changes. Never use bypass flags (`--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox`, `--yolo`, Cursor's `-f`) unless the user explicitly asks for them.

## 1. Detect

Run this script with `exec_command`, `login_shell: true` (it loads PATH from the user's profile) and `yield_time_ms: 10000`:

```sh
bash -s <<'RR_DETECT'
for d in "$HOME/.local/bin" "$HOME/.opencode/bin" "$HOME/.bun/bin" "$HOME/.npm-global/bin" "$HOME/.cargo/bin" "$HOME"/.nvm/versions/node/*/bin /opt/homebrew/bin /usr/local/bin; do
  [ -d "$d" ] && case ":$PATH:" in *":$d:"*) ;; *) PATH="$PATH:$d" ;; esac
done
for name in claude codex gemini cursor-agent opencode aider goose qwen amp copilot droid crush; do
  bin=$(command -v "$name" 2>/dev/null) || continue
  out=$(perl -e 'alarm shift; exec @ARGV or exit 127' 8 "$bin" --version </dev/null 2>&1); code=$?
  ver=$(printf '%s\n' "$out" | grep -m1 . | cut -c1-80)
  case $code in 0) st=ok ;; 142) st=timeout ;; *) st=broken ;; esac
  printf 'cli\t%s\t%s\t%s\t%s\n' "$name" "$st" "$bin" "$ver"
done
if [ "$(uname)" = Darwin ]; then
  for app in Claude Codex Cursor ChatGPT Windsurf "Visual Studio Code"; do
    for root in /Applications "$HOME/Applications"; do
      [ -d "$root/$app.app" ] && printf 'app\t%s\tgui\t%s\t\n' "$app" "$root/$app.app"
    done
  done
fi
exit 0
RR_DETECT
```

Each line is `kind  name  status  path  version`. Show the user a short list.

- `ok`: callable.
- `broken`: on PATH but it doesn't start, e.g. a shim whose binary was removed. Don't call it; show the first line of the error and suggest reinstalling.
- `timeout`: `--version` hung, which usually means a first-run or login prompt. Ask the user to open it once in a terminal.
- `app`: desktop apps. They have no headless mode. Use their CLI if there is one; drive the window with the `computer_*` tools only if the user asks you to.

If an agent the user names is missing, ask where it is installed. Don't conclude that it isn't installed.

## 2. Check the flags

Agent CLIs change their flags often. Before you first call an agent in a session, run `<agent> --help` (and the subcommand's help, such as `codex exec --help`), then adjust the table below to match.

## 3. Call

| Agent | One-shot command | Read-only | Allow edits | Follow-up |
|---|---|---|---|---|
| Claude Code | `claude -p "$p" --output-format json` | `--permission-mode plan` | `--permission-mode acceptEdits` | `--resume <session_id>` from the JSON |
| Codex | `codex exec "$p"` (add `--skip-git-repo-check` outside git) | `-s read-only` | `-s workspace-write` | `codex exec resume --last "$p"` |
| Gemini CLI | `gemini -p "$p"` | `--approval-mode default` | `--approval-mode auto_edit` | — |
| Cursor Agent | `cursor-agent -p "$p" --output-format text` | none enforced; say so in the prompt | default (can edit; no shell without `-f`) | `--resume <chatId>` |
| opencode | `opencode run "$p"` | `--agent plan` | default (`build` agent) | `--continue` |
| Aider | `aider --message "$p" --no-auto-commits` | `--dry-run` | `--yes-always` | — |

For any other agent the script finds (goose, qwen, amp, copilot, droid, crush), read its `--help` and look for a print, exec or run mode first.

Pass the prompt through a quoted heredoc so quotes and `$` inside it survive, and always redirect stdin. There is no TTY, so an agent waiting for input hangs until the timeout.

```sh
p=$(cat <<'RR_PROMPT'
<the task>
RR_PROMPT
)
codex exec -s read-only --skip-git-repo-check "$p" </dev/null
```

The local agent doesn't see your conversation. Write it a complete brief: the goal, the files or folders it should use, any constraints, and what to return, such as a concise final answer or a list of changed files. Tell it not to use ReadyRig itself.

## 4. Long runs

Agents often take minutes.

- Use `background: true` with a `timeout` such as 1800. Later results carry `[progress]` lines and a `[notice]` when the job ends. Alternatively, use a short `yield_time_ms` and poll with `write_stdin` and the `session_id`.
- Long output is saved at `stdout_path`; read it with `read_file`. Prefer the agent's own final answer: `result` in Claude's JSON, or Codex's `--output-last-message <file>`.
- After a network timeout you can't tell whether the run happened. Check with `ps -ef | grep -v grep | grep <agent>` and `git status --short` before you start it again. Run only one agent at a time in the same folder.

## 5. Errors and reporting

- Authentication errors: ask the user to sign in on that computer (`claude`, `codex login`, `cursor-agent login`, `gemini`). Never ask for, type or move their API keys or tokens.
- Trust prompts or first-run prompts hang a run. Stop it and ask the user to open the agent once in that folder.
- When an agent edits files, run `git status --short` before and after, and report what changed.
- Report which agent ran, its command without the long prompt, the exit code, and the agent's answer in its own words. Don't present its claims as checked unless you checked them.
