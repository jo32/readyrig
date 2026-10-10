# rp: pick a ReadyRig project from Claude Code

`rp` is a Claude Code mod (a plugin of function hooks). It adds a `/rp` command that lists the approved projects on every ReadyRig computer, grouped by machine. The project you pick is added to your next prompt, so Claude knows which computer and folder to work in.

```
jiangdailins-MacBook-Pro.local (darwin)
    agent_workspace      /Users/jo32/agent_workspace
  ● computer-use-server  /Users/jo32/Projects/computer-use-server
    tvbox                /Users/jo32/Projects/tvbox

jo32-machine-linux (linux)
    agent_workspace      /root/agent_workspace
```

## Requirements

- Claude Code with function-hook plugins (2.1.291 or newer).
- The ReadyRig MCP server connected to Claude Code, so the tools `mcp__readyrig__list_computers` and `mcp__readyrig__call_computer_tool` are available. `/mcp` shows whether it is connected.
- At least one computer that is online and reachable (a ready tunnel, or relay connected). Offline machines are listed as `offline`, without projects.

## Install

From GitHub, at the Claude Code prompt in a terminal:

```
/plugin install rp --marketplace jo32/readyrig
```

Answer `y` to add the marketplace, then pick a scope (user scope loads it in every session). This repository's `.claude-plugin/marketplace.json` lists the mod.

From a local clone:

```sh
claude plugin marketplace add /path/to/readyrig
claude plugin install rp@readyrig
```

To try it without installing, or while changing it, load the folder for one session. Saving a file reloads the mod:

```sh
claude --plugin-dir /path/to/readyrig/integrations/claude-code/rp
```

## Use

| Command | What it does |
| --- | --- |
| `/rp` | Opens a pane with every machine and its projects, and a search box at the top. Type to narrow the list, then press Enter to pick the first match. Or move with Tab or the arrow keys, press Enter on a project to pick it, or Esc to close the pane. |
| `/rp clear` | Drops a project you picked but have not used yet. |

After you pick a project:

- A toast confirms it, and the status line shows `rp: <project> @ <machine>`.
- The next prompt you send gets one line added at the end:

  ```
  [ReadyRig project: "computer-use-server" on machine "jiangdailins-MacBook-Pro.local" (computer_id: 0WNo…). Use this machine for this request: pass project: "a3e4…" to its tools and use paths relative to the project.]
  ```

- It is added once, then cleared. Slash commands are left alone, so `/rp` again or any other command does not use it up.

The search matches project names, paths and machine names, ignoring case. With several words, a project must match all of them (`mac tvbox`). Machines with no match are hidden while you search.

The pane lines up project names in one column. When the pane is too narrow for the name and path side by side, each path goes on its own indented line under the name.

## How it works

`/rp` calls the ReadyRig MCP tools through Claude Code, the same way Claude would: `list_computers`, then `list_projects` on each online computer through `call_computer_tool`. These calls go through Claude Code's normal permission checks, so you may be asked to allow them the first time.

| File | Purpose |
| --- | --- |
| `.claude-plugin/plugin.json` | Manifest |
| `hooks/hooks.json` | Points Claude Code at the hooks module |
| `hooks/register.tsx` | The `/rp` command, the pane, and the prompt hook |
| `types/index.d.ts` | Types for the values the mod keeps in session state |
| `tests/rp.test.ts` | Picks a project with fake ReadyRig answers and checks the next prompt |

## Develop

```sh
claude plugin validate integrations/claude-code/rp
claude plugin test integrations/claude-code/rp
```

After Claude Code has loaded the mod once, `.claude-plugin/types/` holds the API types (it is git-ignored), and `tsc -p integrations/claude-code/rp` type-checks it.
