# Unreleased

## Added

- `rp`, a Claude Code mod in `integrations/claude-code/rp`. Its `/rp` command lists the approved projects on every ReadyRig computer, grouped by machine, and adds the picked project (machine, `computer_id` and path) to the next prompt; `/rp clear` drops an unused pick. The repository is now a Claude Code marketplace (`.claude-plugin/marketplace.json`), so the mod installs with `/plugin install rp --marketplace jo32/readyrig`.
