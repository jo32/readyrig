# Unreleased

## Added

- **Show images in Claude Code.** The `rp` mod (0.5.0) gives Claude a `show_image` tool that draws an image from the attached project in the terminal (Ghostty, kitty), with an Open in Preview button that opens it on the Mac. On macOS, JPEG, HEIC, WebP and other formats are converted to PNG with `sips` first.
- **Call the agents on a computer.** The `readyrig-local-agents` skill (`integrations/skills/readyrig-local-agents`) shows a connected agent how to find the coding agents installed on a ReadyRig computer (Claude Code, Codex, Gemini CLI, Cursor Agent, opencode, Aider and others) with `exec_command`, report broken installs, and hand one of them a task in non-interactive mode, read-only unless the task needs edits. The skill is one self-contained file, so its text can also be pasted into a prompt.
