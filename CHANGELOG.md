# Changelog

Notable changes to ReadyRig are recorded here. For setup and current behavior, see the [README](README.md).

## 0.6.17

### Fixed

- Screenshot previews now display in the local and gateway dashboards. The image content security policy now allows the blob URLs used by screenshot previews.

## 0.6.16

### Added

- Safari MCP. Safari 27 and later has an MCP server (`safaridriver --mcp`), and ReadyRig now bridges it as `safari_*` tools, next to Chrome and with a switch of its own: a toggle under Connection, `readyrig capability safari on|off`, the `w` key in the terminal dashboard, and a saved `no-safari` setting. Exposure is lazy: the tool list is read once with a short-lived process, Safari is not connected until a Safari tool is first called, and only five everyday tools (`safari_create_tab`, `safari_navigate_to_url`, `safari_get_page_content`, `safari_page_interactions`, `safari_screenshot`) are advertised in `tools/list`. The other twelve are in the `advanced` group, reachable through `help` and `use_tool`. Safari's definitions in `tools/list` shrink from 13.5 KB to 7.4 KB. `--no-safari` turns it off alone; the status is in `GET /api/state` as `safari`. See the README, "Safari MCP".
- A Safari call that fails because remote automation is off now says which Safari setting to turn on, and Safari reports `permission_required` until a call succeeds.
- The dashboard shows Safari next to Chrome: a row in the Tools list and a card under Connection, with a setup guide, in English and Chinese. The tool runner now waits for the right browser: a Safari tool no longer waits for Chrome, and stays runnable while Safari asks for remote automation so that the answer can explain the setting.

### Fixed

- Choosing "Grant access" for Screen Recording or Accessibility now also asks macOS for that permission. A bare permission check never adds an app to Privacy & Security, so ReadyRig could be missing from the list you were sent to.
- Chrome tools that save or upload files, such as a screenshot's `filePath`, refused every path in your projects with "not within any of the configured workspace roots". ReadyRig never told the Chrome DevTools server which folders are allowed, so only the OS temp folder worked. ReadyRig now declares the approved projects (and everything under Full Access), tells the server when they change, and answers the server's own requests. A reply is no longer mistaken for a server request that happens to share its id. A refused path now says it must be inside an approved project.
- ReadyRig accepted only MCP protocol revisions from 2025-03-26 on, which would have rejected Safari's 2024-11-05 server.

### Changed

- The Chrome switch now controls only Chrome. `--no-chrome` no longer turns off Safari, and `--no-safari` is a saved setting like the others.
- Tools are registered per provider, so a refresh of the Chrome tools no longer removes Safari's, and the reverse.
- The benchmark budgets are unchanged: the benchmark does not enable browser tools.

## 0.6.15

### Fixed

- A temporary link no longer keeps showing a dead address as ready after the computer sleeps. Cloudflare drops the temporary hostname while `cloudflared` reconnects and logs a registered connection, so ReadyRig reported the old URL as ready and every remote call failed with an invalid response. A temporary link now checks that its public address reaches this instance about every 20 seconds, and again within seconds of waking from sleep. When the address stops answering, ReadyRig hides the URL and shows reconnecting, then creates a new temporary link after repeated failures, so the URL changes. It waits while the internet is down, backs off if replacements keep failing, and a Stop always wins over a pending replacement.

## 0.6.14

### Added

- Regression guards for tokens, round trips and job completion: Go budget tests for the tool definitions, the server instructions and the size of common results; `make bench-check` (run by the release workflow) with budgets in `scripts/bench-baseline.json`; and negative controls proving each guard can fail. See the README, "Guards against regressions".
- `list_tasks` accepts `finished` (default 5, up to 100).

### Fixed

- A `write_stdin` wait that the client abandons (timeout, disconnect, cancellation) no longer kills the command; only `terminate`, Pause, Stop, and the command's own timeout do. It was found when a long wait through the cloud relay timed out and took the running job with it.

### Changed

- `write_stdin` and `list_tasks` waits are capped at 45 seconds instead of 55: the cloud relay dropped a request at about 55 seconds, while 45 worked.
- `write_file` and `edit_file` return a one-line text result (24 and about 90 bytes) instead of a JSON object (180 and 264 bytes). REST and relay responses are unchanged.
- `list_tasks` shows running sessions and the five most recent finished ones, with a count of older ones, instead of the whole hour of history. It was 473 bytes after 20 commands instead of about 2 KB.

## 0.6.13

### Added

- `batch` runs several tools in one request, saving a round trip per call. `list_tasks` can wait for one or all running commands (`wait`), and `write_stdin` may wait up to 55 seconds, so waiting for a job takes one call instead of a poll every 20 seconds.
- Tools that lack a macOS permission fail at once with the `permission_required` error code naming the permission, and `help` shows them as unavailable with the reason.
- Specific error codes for file tools: `not_found`, `file_exists`, `is_directory`, `binary_file`, `too_large`, `outside_project`, `no_match`, `ambiguous_match`, `project_not_found`, `ambiguous_project`, `permission_denied`.
- The activity log gets a derived label for mutating calls that have no `description`.
- `scripts/bench-tools.py` compares two builds on the same agent tasks (requests, bytes as a token proxy, wall time, completion).
- The release workflow fails if `CHANGELOG.md` has no section for the tag.

### Changed

- Smaller results: a successful text result is a single text block with no repeated metadata, JSON-only results are `{"result": ...}`, and errors are `{"error", "error_code"}`. Finished command results drop progress fields and false flags, and `project` is added to results only when more than one project is approved.
- Smaller tool definitions and server instructions: the native tool list is about 16% smaller than in 0.6.12 even with the new tool.
- Commands default to a 600-second timeout (3,600 with `background`) instead of 60 seconds, so builds and test runs that outlast the first minute are no longer killed. Finished command sessions are kept for an hour, and a full session table forgets its oldest finished entry instead of refusing new commands.
- A command that exits non-zero returns REST `status` `success`; the activity log still marks it failed.

## 0.6.12

### Changed

- Terminating a command with `write_stdin` and `terminate: true` now returns a normal result (`terminated: true`, no `exit_code`) instead of a `cancelled` error, and the original call is recorded as successful. Pause, Stop, and client cancellation are still cancellations.

## 0.6.11

### Added

- Exporting the call log shows progress and can be cancelled.
- `edit_file` (exact-match replacement) and `glob`; `read_file` returns numbered, paged text and image files as images; `list_directory` gained `depth`, `pattern`, `type`, and `sort`; `search_files` gained regex, case-insensitive search, `include`, `context`, and paging. Recursive listings and searches honour `.gitignore`.
- `exec_command` accepts `background`, `login_shell`, and timeouts up to four hours. Long output is returned as its start and end and saved in full for `read_file`. A background job that finishes is reported in the next result of the same session and on the optional MCP event stream.
- `computer_action` accepts a chained `actions[]` batch, `triple_click`, `paste`, `wait`, held `modifiers`, and `element` targets; screenshots wait for the screen to settle. New tools: `computer_ui_tree`, `computer_app`, `computer_clipboard`. `computer_screenshot` can zoom into a region and choose a display.
- Progress without streaming: running commands report `elapsed_ms`, `output_bytes`, and `idle_ms`; other tool results in the session carry a `[progress]` line (at most every 15 seconds) while a command runs; the new `list_tasks` tool lists a session's commands; `write_stdin` accepts `return_on: "output"` and waits up to 20 seconds, so it can long-poll a log.
- `help` can list tools compactly, and `use_tool` runs tools that `tools/list` does not advertise. The Chrome DevTools inspection tools are now in that advanced group (`READYRIG_EXPOSE_ALL_TOOLS=1` lists them).
- Mutating tools take an optional `description` that appears in the activity log.
- MCP: `notifications/cancelled`, `logging/setLevel`, an optional `GET /mcp` event stream, plain-text result blocks, and `error_code` in results.

### Changed

- Project addressing: `project` accepts an ID or a project name; a session's relative paths stay with the project that was the default when the session first used one, so changing the default no longer moves a running agent; results name their `project`; `list_projects` reports `session_default`. The Projects page shows how many sessions use each project and explains the rule. `Projects.Resolve` and `Directory` take the session as their first argument.
- A non-zero command exit is returned as data, not as a tool error; the activity log still marks it failed. Timeouts return the `timeout` error code.
- `tools/list` omits tools whose capability is disabled and announces changes.
- Tools that must run one at a time now queue per capability, so a slow browser call no longer blocks file writes or screenshots.
- Commands inherit a `PATH` that includes Homebrew and common tool directories. `write_file` keeps existing permissions and creates new files as 0644 instead of 0600.
- Agent-facing errors are English and carry a stable `error_code`. Tool schemas now enforce `enum`, `minimum`, `maximum`, and array limits. Redaction no longer blanks counters such as `max_tokens`.
- MCP `serverInfo.version` reports the real version.

## 0.6.10

### Added

- An Account view in the terminal UI (key 5) to sign in with Google, see binding status, and disconnect or cancel a pending login.
- Browser screenshots in call details load on demand.

### Fixed

- Large call results no longer freeze the dashboard; previews are bounded and the full result stays available through Copy.
- English translations for the new dashboard messages.

## 0.6.9

### Added

- Cloud MCP with OAuth authorization, automatic client registration, revocable access, and tool discovery and execution through an owned computer's public connection.
- A Connect MCP dialog in the cloud console for connection details and client management.

### Fixed

- Adding a project folder in the macOS app now uses the system folder picker attached to the main window. Cancelling leaves project access unchanged and allows the picker to be opened again.

## 0.6.8

### Added

- Unapproved Screen Recording and Accessibility permissions in the macOS app now have shortcuts that open the corresponding System Settings pane.

### Fixed

- The update help text now correctly says capability switches restore their last selection after restarting.

## 0.6.7

### Fixed

- File, terminal, desktop, and Chrome capability switches now save the last selection and restore it on restart, including changes through the local dashboard, CLI/TUI, and bound cloud account. Update restarts retain current switches instead of replaying stale capability flags.

## 0.6.6

### Fixed

- The macOS app now relaunches through LaunchServices after an in-app update. This starts a fresh app process and preserves launch arguments, preventing macOS from rejecting its menu bar icon and window activation as an exiting process.
- If an older version has already restarted with a missing menu bar icon, fully quit ReadyRig and reopen it from Applications once to restore the icon.

## 0.6.5

Version 0.6.4 did not publish installation packages. This release includes all changes prepared for that version.

### Added

- An interactive setup guide that opens automatically after curl installation. Reinstalling preserves existing settings; unattended installation can skip the guide.
- A terminal dashboard opened by running `readyrig` without a command. It manages service state, capabilities, sharing, projects, tools, and recent activity; exiting keeps the service running.
- A CLI bundled with the macOS app, registered in the user's command path on first launch and updated with the app. Separately installed CLI binaries are preserved.
- A local setup prompt in the app's Connection page, with English and Chinese copy, the matching CLI executable, and the current data directory. Local agents can inspect, configure, and verify ReadyRig through CLI commands.

### Changed

- `serve` and `web` start a detached daemon on macOS/Linux, wait for readiness, and return to the terminal. Added `stop` and `restart`; `--foreground` remains available for supervisors and debugging, including systemd units.
- The app reads saved CLI startup settings on launch. Project management and runtime switches work while the app is open; startup settings are changed while the instance is stopped.
- Website installation instructions describe the guide and default terminal dashboard. Removed obsolete documentation screenshots.

### Fixed

- Pasting long or Unicode project paths into the terminal dashboard no longer redraws the entire screen for every character, keeping input responsive on slower terminals.
- Chrome approval-mode debugging can be detected through its local server before reading protected profile files. Background detection uses a rejected probe path to avoid opening approval dialogs; browser operations still require Chrome approval.

## 0.6.3

### Added

- A checksum-verified curl installer for the macOS/Linux CLI on amd64 and arm64, plus a copyable installation command on the website.
- Saved CLI startup settings, local status and connection discovery, project and capability management, tool invocation with stable sessions and JSON output, public sharing, and cloud device binding from a headless VM.
- Linux systemd user service installation and lifecycle commands. CLI administration on macOS/Linux uses a private Unix socket shared with the app's existing backend, without writing dashboard credentials to disk.
- Local project menus can open folders with the system file manager or, on macOS, a compatible installed application selected from its name, icon, and default association. The shared local endpoints also support opening files within authorized project boundaries.
- A cloud connection prompt with a Bearer credential tied to the current login session. Agents can list bound computers and public links, change capability switches, start or stop sharing, and pause or resume control, with asynchronous command receipts. Signing out invalidates the cloud credential; direct computer tools continue to use public links without a token.

## 0.6.2

### Fixed

- Chrome DevTools MCP can connect when Finder launches ReadyRig with an incompatible Node.js version on PATH. ReadyRig checks installed Node versions, skips unsupported versions, and uses a supported Homebrew, Volta, mise, or nvm installation for the MCP component and its subprocesses.
- Chrome runtime diagnostics report the detected Node.js version and path when no supported installation is available.

## 0.6.1

### Fixed

- Mac releases require Developer ID Application signing, hardened runtime, secure timestamps, and Apple notarization. Both architectures and legacy compatibility App packages receive stapled tickets and pass Gatekeeper assessment before packaging.
- Release builds stop when signing or notarization credentials are missing; CI does not overwrite an existing release.

Moving from the ad-hoc signatures in 0.6.0 and earlier to Developer ID requires one manual installation. Subsequent updates retain the same signing team and bundle identifier.

### Changed

- Reorganized the project READMEs in English and moved version history into this document.

## 0.6.0

### Added

- Google account binding and cloud device management through a Cloudflare Worker and D1. Bound devices report heartbeats and receive sharing, capability, and pause/resume commands, with expiry and execution receipts.
- Persistent project folders and default selection, a `list_projects` tool, optional project selection for file and terminal tools, and a Full Access switch for the current run.
- English and Simplified Chinese across the website, desktop app, browser console, cloud console, native menus, authorization dialogs, and connection prompts, with saved preferences.
- Temporary Cloudflare Quick Tunnel sharing, fixed links through named tunnels, and a public read-only console, with credential storage, route validation, and connection diagnostics.
- A connection prompt that checks `help` and `list_projects` before an agent begins work.
- A menu bar quick panel and animated computer icon, including execution and pause states and Reduce Motion support.
- A local macOS file authorization dialog for Chrome's `DevToolsActivePort`.

### Changed

- Standardized the product name, app bundle, commands, website, and shared icon as ReadyRig, retaining legacy data directories, signing identity, update variables, and release assets for upgrade compatibility.
- Moved the website and cloud console to `readyrig.getmegaportal.com` on Cloudflare Workers. Legacy `workers.dev` web requests redirect to the production domain while existing device API credentials remain usable.
- Chrome tool definitions now load before the debugging connection is ready. Tools stay listed while waiting for Chrome or debugging file authorization, and calls return the relevant connection status.
## 0.5.1

### Fixed

- Preserved macOS bundle signatures when creating and extracting update archives, with regression coverage for signature metadata.

## 0.5.0

### Added

- Automatic updates modeled on Magpie's lifecycle: release builds check five seconds after startup and every six hours, download in the background, and install on restart or normal exit.
- Update progress, release notes, and restart/install controls in the menu bar and Connection page, plus `version` and `update` commands.
- GitHub private-release authentication through local `gh` credentials or a read-only update token, with credentials restricted to GitHub API requests.
- Platform, architecture, and build-specific asset selection, size and SHA-256 verification, and macOS signature, signing-team, and bundle-ID checks.
- Download retries, duplicate-check merging, retention of completed downloads after network failures, rollback on replacement failure, and protection against concurrent installation updates.
- Manual update guidance for development builds, read-only installations, and Homebrew-managed installations.
- Local-only update status, check, and restart endpoints.
- Configurable release repositories and Magpie-compatible update feeds, with HTTPS required except on loopback addresses.
- A GitHub release workflow for macOS desktop packages and macOS/Linux/Windows browser binaries, plus a local release build command and optional macOS signing identity.
- An integration check for updating a real binary from 0.4.0 to 0.5.0 while retaining logs and workspace data.

## 0.4

### Added

- A Go bridge to the official Chrome DevTools MCP, exposing dynamically discovered browser tools through REST, MCP, OpenAPI, and the Tools page.
- Local Chrome debugging discovery, configurable debugging endpoints and profile directories, and support for an installed MCP executable or the pinned npm package.
- Browser call logging with preserved upstream schemas, annotations, text, images, and structured results. Browser screenshots appear in call details and JSON logs.
- Serial browser execution with a two-minute limit, cancellation and reconnection, and local capability controls.
- Local-only debugging address validation and default disabling of upstream usage statistics and CrUX queries.

## 0.3

### Changed

- Rebuilt the console using Magpie's theme and components: centered segmented navigation, 48-pixel tool rows, connected statistics, gray surfaces, rounded lists, and inline input/result details.
- Replaced the sidebar, promotional heading, and separate detail panel with the inline layout, using Magpie's light and dark theme values while retaining the product's tool capabilities.

## 0.2

### Added

- Live command output saved when stdout/stderr changes, with console updates that do not consume unread agent `write_stdin` output.
- Per-call cancellation of the selected task and process group, retaining output and marking the call `cancelled`.
- Readable terminal output, file contents, directory tables, and search results, with full-result copying, expandable raw JSON, and stacked details in narrow windows.
- Desktop action replay using saved same-session reference frames, click/drag markers, before/after views, timeline seeking, and playback speed controls. Missing frames are identified rather than inferred.
- Summary-only call lists with full details loaded on demand, preserving expanded state and reading position while avoiding repeated transfer of long output.
- Capability isolation: disabling one category cancels only its in-flight calls.
- Local-only call detail and cancellation endpoints: `GET /api/calls/{id}` and `POST /api/calls/{id}/cancel`.
