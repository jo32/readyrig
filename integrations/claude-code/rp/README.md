# rp: attach a ReadyRig project to Claude Code

`rp` is a Claude Code mod (a plugin of function hooks). It adds a `/rp` command that lists the approved projects on every ReadyRig computer, grouped by machine. The project you pick is attached to the session: Claude gets its `AGENTS.md`, `CLAUDE.md` and skills, and works in it until you detach it. Link a folder to a project with `/rp link`, and every Claude Code session you start in that folder attaches the project by itself.

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
| `/rp` | Opens a pane with every machine and its projects, and a search box at the top. Type to narrow the list, then press Enter to attach the first match. Or move with Tab or the arrow keys, press Enter on a project to attach it, or Esc to close the pane. |
| `/rp detach` | Detaches the project (`/rp clear` does the same). The pane also has a `detach` button while a project is attached. |
| `/rp link` | Links the folder Claude Code runs in to the attached project. With nothing attached, it opens the pane: the project you pick is attached and linked. The pane also has a `link to this folder` button. |
| `/rp unlink` | Removes the folder's link. The project stays attached to the session until you detach it. |

When you pick a project, the pane shows each step as it runs:

```
Attaching OpenWorkBuddy2 @ jiangdailins-MacBook-Pro.local

  ✓ AGENTS.md                43 lines
  – CLAUDE.md                none
  … .agents/skills           reading 3 skills
  – .claude/skills           none
  · Add to the conversation
```

The status line counts the steps too, so you can close the pane with Esc and keep working while it loads. When it is done, the pane closes, a toast says what was found, and the status line shows `rp: <project> @ <machine> · AGENTS.md · 3 skills` for as long as the project stays attached (`· linked` at the end when the folder is linked to it).

What Claude gets:

- **Where to work**: the machine's `computer_id` and the project id to pass to the ReadyRig tools. The folder's path is left out, since it can include your user name.
- **Instructions**: `AGENTS.md` (the [agents.md](https://agents.md) standard) and `CLAUDE.md` from the project's root, each in full up to 40,000 characters. A `CLAUDE.md` that only says `@AGENTS.md` is skipped. Claude is also told to look for an `AGENTS.md` or `CLAUDE.md` in subfolders, where the nearest one wins.
- **Skills**: each `SKILL.md` in `.agents/skills/` (the universal location) and `.claude/skills/`, including linked folders. Only the name and description from each skill's front matter go in. Claude reads the whole `SKILL.md` when a task matches. If both folders have a skill with the same name, the one in `.agents/skills/` wins.

All of this goes in once, as a hidden note at the end of the conversation. It doesn't go into the system prompt, so attaching or detaching doesn't invalidate the prompt cache for the conversation so far. Your prompts are sent as you typed them. If `/compact` or `/clear` drops the note, it is added again before your next prompt. Attaching another project replaces the first one. Detaching adds a short note that tells Claude the project no longer applies.

## Linking a folder

A link ties a folder on the computer that runs Claude Code (the session's project root, such as `~/tvbox`) to one ReadyRig project. When Claude Code starts in that folder, or in a folder below it, the mod attaches the linked project while Claude Code starts. The nearest linked folder wins. Your first prompt waits up to 8 seconds for the attach to finish, so Claude has the project's note before it answers. If the ReadyRig MCP server isn't connected yet, the mod tries again every 2 seconds for up to 30 seconds. If the machine is offline, the project is gone, or you deny the ReadyRig call, a toast says why and nothing is attached. Starting Claude Code may ask you to allow `mcp__readyrig__list_computers` and `mcp__readyrig__call_computer_tool`.

Links are kept in the mod's own store, so they last across sessions. Each folder has at most one link; linking it again replaces the link. Detaching doesn't remove the link, so the next session attaches the project again. Use `/rp unlink` to stop that.

The search matches project names, paths and machine names, ignoring case. With several words, a project must match all of them (`mac tvbox`). Machines with no match are hidden while you search.

The pane lines up project names in one column. When the pane is too narrow for the name and path side by side, each path goes on its own indented line under the name.

## How it works

`/rp` calls the ReadyRig MCP tools through Claude Code, the same way Claude would: `list_computers`, then `list_projects` on each online computer through `call_computer_tool`. Attaching uses `read_file` and `list_directory` on the project. These calls go through Claude Code's normal permission checks, so you may be asked to allow them the first time.

| File | Purpose |
| --- | --- |
| `.claude-plugin/plugin.json` | Manifest |
| `hooks/hooks.json` | Points Claude Code at the hooks module |
| `hooks/register.tsx` | The `/rp` command, the pane, attaching and detaching, linking folders, and the prompt hook that restores the note |
| `types/index.d.ts` | Types for the values the mod keeps in session state |
| `tests/rp.test.ts` | Attaches projects using fake ReadyRig answers, then checks the note, the progress steps, search, re-adding the note after compaction, detach, and linking folders |

## Develop

```sh
claude plugin validate integrations/claude-code/rp
claude plugin test integrations/claude-code/rp
```

After Claude Code has loaded the mod once, `.claude-plugin/types/` holds the API types (it is git-ignored), and `tsc -p integrations/claude-code/rp` type-checks it.
