# ReadyRig — Local Agent Adapter

ReadyRig is a local tool service written in Go with a Wails desktop console. It lets remote agents use REST or MCP to work with local files, run commands, and capture and control the macOS desktop, while keeping execution logs available on your computer. It also bridges the official Chrome DevTools MCP tools into the same interface.

[Website](https://readyrig.getmegaportal.com/) · [Cloud console](https://readyrig.getmegaportal.com/console) · [Releases](https://github.com/jo32/readyrig/releases) · [Changelog](CHANGELOG.md)

![ReadyRig control console](docs/readyrig-console.png)

The website, desktop app, local browser console, and cloud device console support English and Simplified Chinese. Choose a language or follow the system setting in the top-right corner. Native menus, authorization dialogs, connection prompts, and status messages follow the same preference. The desktop app saves its language in `language.json` in the private data directory.

## Getting started

Building from source requires Go 1.25+. The macOS desktop build also requires Xcode Command Line Tools. The core application needs no Node.js, npm, or frontend bundler; the optional Chrome MCP integration requires Node.js.

```sh
make app
open dist/ReadyRig.app
```

To run directly with a workspace:

```sh
go run ./cmd/adapter --workspace /absolute/path/to/workspace
```

To use the browser console with the same Go backend:

```sh
make cli
bin/readyrig-web web --workspace /absolute/path/to/workspace
```

Open the `Dashboard` URL printed in the terminal. Its startup key is exchanged for an HttpOnly, SameSite=Strict cookie and removed from the address bar after login. The agent API uses a separate random access path and needs no Authorization header.

Default locations and addresses:

- Workspace: `~/agent_workspace`.
- Data: `~/.local/share/readyrig/` for new installations, containing SQLite logs, screenshots, and application data. Existing installations reuse their previous directory; see [Compatibility](#compatibility).
- Agent API: `http://127.0.0.1:7332/<random-8-character-path>`. Copy the actual URL from the startup output or Connection page.
- Browser console: `http://127.0.0.1:7331`, in `web` mode only.

The initial data directory must be outside the initial workspace. Adding a project that contains the data directory, or enabling Full Access, expands what file tools can access.

## CLI and VMs

ReadyRig has a desktop app and a CLI using the same backend, tools, permission checks, and local execution records. The CLI runs on macOS and Linux with amd64 or arm64 processors; it can configure and use an instance running on that machine. It also controls the desktop app when both builds include the CLI interface and use the same data directory. Linux supports file, terminal, and Chrome MCP tools; native desktop control remains macOS-only.

The macOS App includes a CLI of the same version. On first launch after moving it to Applications, the App registers `~/.local/bin/readyrig` and adds that directory to your shell's startup settings (zsh, bash, or fish), without administrator access. Open a new terminal and run `readyrig` for the TUI or use any subcommand. It attaches to the running App's service and shares its project catalog and logs. App updates also update its bundled CLI; moving the App repairs its command link on the next launch. Separately installed CLI binaries are preserved. The App's Connection page shows CLI installation status and any problem that needs fixing. Update the bundled CLI through the App so its code signature stays valid.

Install without Go, Node.js, or administrator access:

```sh
curl -fsSL https://readyrig.getmegaportal.com/install.sh | sh
```

The installer chooses an existing, writable directory already in PATH, checking `~/.local/bin`, `~/bin`, then `/usr/local/bin`. If none qualifies, it installs into `~/.local/bin` and prompts you to add it to PATH. Override the destination with `--install-dir` or `READYRIG_INSTALL_DIR`. After installation, it automatically opens a configuration guide in your terminal. Choose a workspace, terminal access, and Chrome tools; the guide can start ReadyRig in the background when finished. Existing configurations are kept on reinstall. Downloads pin one stable release, verify its `SHA256SUMS`, and replace the binary atomically. Failed downloads, checksum mismatches, or releases without CLI commands leave an existing binary intact. Use `--no-setup` (or `READYRIG_NO_SETUP=1`) for unattended installation. Without an interactive terminal, the installer prints the command to open the guide later.

To inspect the installer first or select a version and destination:

```sh
curl -fsSL https://readyrig.getmegaportal.com/install.sh -o /tmp/readyrig-install.sh
less /tmp/readyrig-install.sh
sh /tmp/readyrig-install.sh --version X.Y.Z --install-dir "$HOME/.local/bin"
```

`--repo owner/repo` selects another public GitHub release repository. The installer is also included as `install.sh` in new releases. For source builds, `make cli` creates `bin/readyrig` and its compatibility copy `bin/readyrig-web`.

Open the terminal dashboard:

```sh
readyrig
```

On first launch it opens the setup guide; on later launches it starts or attaches to the service. The TUI shows live connection and capability status, approved projects, tools, and recent activity. Use Tab or 1–5 to change views, arrows or j/k to select items, `s` to start/stop, `p` to pause/resume, and `f`/`t`/`b`/`c` to toggle and save files/terminal/browser/computer. In Projects, `a` adds a folder, Enter selects it, and `d` removes access after confirmation. `h` toggles temporary public sharing. In Account (5), `l` starts Google sign-in and shows a link and verification code to use in your own computer’s browser, including when connected over SSH. Binding status refreshes automatically; `d` disconnects the account or cancels a pending login after confirmation. Arrow keys scroll long account details on small terminals. `q` or Ctrl-C closes the TUI and keeps the service running. Noninteractive invocation prints command help and returns.

Run `readyrig setup` to change saved startup settings while the service is stopped. For scripted configuration and lifecycle control:

```sh
readyrig init --workspace ~/agent_workspace --allow-shell --no-chrome
readyrig config show
readyrig serve
readyrig stop
readyrig restart
```

`init` saves startup settings in private `cli.json` and creates the initial workspace. On macOS/Linux, `serve` and `web` launch a detached background process, wait until both listeners are ready, print its connection URLs and private `daemon.log` location, and return to your shell. Repeated starts reuse a running instance; `restart` applies saved settings and launch overrides. `stop` waits for a clean shutdown. Use `serve --foreground` for debugging, containers, and process supervisors. The desktop app reads the same saved startup settings on launch; Windows retains foreground browser mode. Quit the desktop app before changing startup settings with `config set`, `init`, or `setup`, then reopen it. Runtime project and capability commands work while the app is open. Existing project catalogs are preserved: `--workspace` initializes the first project, and `projects add/use` manages later folders. Defaults are overridden by saved settings, then supported `READYRIG_*` environment variables, then explicit launch flags. Both listeners use loopback by default. The dashboard remains available at the printed URL, including through SSH port forwarding for a headless VM.

Change saved startup settings while the instance is stopped:

```sh
readyrig config set allow-shell false
readyrig config set no-chrome true
readyrig config set gateway 127.0.0.1:7442
```

Use commands while the app or background service is running:

```sh
readyrig status
readyrig connection
readyrig tools
readyrig projects list
readyrig projects add /srv/my-project
readyrig projects use <project-id>
readyrig capability terminal on
readyrig call --session vm-task help
readyrig call --session vm-task exec_command '{"command":"pwd"}'
readyrig call --session vm-task read_file '{"path":"README.md"}'
readyrig pause
readyrig resume
```

Results use JSON for scripting. `call <tool> -` reads JSON from stdin; `--session` before the tool name keeps process sessions and screenshot frames associated with the same task. Failed calls return a nonzero exit code and retain the service's JSON error result. Tool calls use the same audit log and capability/paused checks as REST and MCP. Capability changes from the local dashboard, TUI, CLI, and bound cloud account are saved in private `cli.json` and restored on restart. Explicit launch flags override saved choices for that run; changing a switch saves only that choice, preserving unrelated settings. Projects and their selection persist. Full Access remains session-only: use `serve --full-access` explicitly, and it cannot be saved by `init` or `config set`.

Connections and account binding also work without a browser on the VM:

```sh
readyrig share start             # Temporary HTTPS connection; check status for readiness
readyrig share status
readyrig share stop
readyrig cloud login --name my-vm
readyrig cloud status
```

Open the `login_url` returned by `cloud login` on your own computer, sign in, compare the code, and confirm the binding while the VM service remains running. `cloud logout` unbinds it. To save a fixed tunnel, pass the Tunnel Token through stdin rather than the command line, then start `share start fixed`:

```sh
readyrig share configure --url https://your-domain.example --token-stdin < /private/tunnel-token
readyrig share start fixed
```

For a Linux VM with a systemd user manager:

```sh
readyrig service install
readyrig service start           # Enable and start the service
readyrig service status
readyrig service restart
readyrig service stop
```

`install` writes `readyrig.service` in the user's systemd configuration directory and reloads the manager. Its command uses `serve --foreground` so systemd tracks the process; running `service install` again upgrades a matching older unit. `start` enables it for future user-manager starts. For startup at boot and continued operation after logout, an administrator can enable lingering with `sudo loginctl enable-linger "$USER"`. Minimal containers without systemd should run `readyrig serve --foreground` under their existing supervisor. `service print` shows the generated unit; `service uninstall` stops, disables, and removes the matching unit while retaining application data. Only one ReadyRig user unit is installed per user. Use `service stop/restart` when systemd manages the instance.

Use global `--data-dir /private/path` before or after a command for another instance, including `init`, `serve`, configuration, and service installation. Run the control commands as the same OS user. The CLI connects through a Unix socket in a private temporary directory, with `0600` socket permissions. Private `control.json` stores only its path, without a dashboard key or other credential; both are removed on clean shutdown. Browser-origin requests are rejected on this interface. CLI management is never exposed through the public agent connection. The existing Windows web service continues to use its browser console for local management.

## Using the console

- **Activity** shows live status, filters by session, category, and result, searches arguments and errors, and provides request/response details, pagination, and NDJSON export. Command output updates during execution without consuming unread `write_stdin` output. Individual calls can be cancelled while retaining their output.
- **Desktop replay** displays saved screenshots and action markers frame by frame or as playback, with a timeline and speed controls. It loads screenshots and actions from up to the latest 5,000 desktop calls. Playback displays history without executing actions again; missing frames are identified explicitly.
- **Tools** shows registered tools, JSON Schema, and example arguments. Test calls execute real operations and are logged. Detailed results load on demand, with readable terminal output, files, directory tables, and search results, plus expandable raw JSON.
- **Connection** provides the current agent URL and MCP configuration, system permission status, capability switches, sharing, and software updates. **Local configuration → Copy setup prompt** gives a local terminal-capable agent CLI instructions using this instance’s exact executable and data directory. The preview and copied prompt follow the selected language and explain which changes need a restart. This entry is available only in the local console.
- **Pause control** cancels active calls and terminal process groups and rejects new tool calls. Disabling one capability cancels only calls in that category.

File tools and Chrome detection are enabled by default. Chrome tools can be listed before the browser connects, but execution requires a ready debugging connection. Terminal and desktop operations can be enabled locally, explicitly through `--allow-shell` and `--allow-computer`, or through saved CLI startup settings for the app and `serve`/`web`. All five capability switches remember the last selection. `--no-files`, `--no-chrome` and `--no-safari` disable the file, Chrome and Safari tools on launch. The agent API cannot change permissions or resume paused control. A bound cloud account can manage the supported switches described below.

On macOS, left-click the menu bar computer icon to open the quick panel; clicking outside dismisses it. Right-click for the native menu to open the full window, pause or resume, check for updates, or quit. Closing the main window keeps the service running; quitting stops it. The icon animates during tool execution, indicates pause, and respects Reduce Motion.

## Projects and Full Access

Add local folders on the **Projects** page. You can browse folders, rename projects, choose a default, and remove access. The folder's **⋯** menu includes **Open folder** to use the system file manager. On macOS, **Open with** lists compatible installed applications from the system's file associations, including their icons and the default application. Choose an app to open the folder without changing its default association. The project list and default are saved in `projects.json` in the data directory. `--workspace` supplies the initial folder only when this list is first created. Removing a project leaves its files and already running terminal commands intact.

The desktop and local browser console share `POST /api/files/open`, accepting `project`, `path` and an optional `application` ID from `GET /api/files/applications?project=…&path=…`. These reusable endpoints support existing files and folders within the same project/Full Access boundaries as file tools. macOS uses NSWorkspace to list applications and open the selected path; builds without cgo query the same API through JXA and launch with `open`. Windows uses ShellExecute and Linux uses `xdg-open` for default opening, with application selection hidden where unavailable. These local console actions are excluded from REST/MCP agent tools and the public console.

- Agents call `list_projects {}` to obtain project IDs, names, absolute paths, the active project (the dashboard's default), `session_default`, and `full_access` status. This query also works while paused.
- File tools and `exec_command` accept an optional `project`, given as an ID or a project name (case-insensitive; an ambiguous name is rejected). Most calls need no `project`: an absolute path inside an added project finds its project, and a relative path uses the session's project. When a project is explicitly selected, the path must belong to it.
- **A session's project is stable.** The first time an agent session uses a relative path, it is pinned to the project that is the default at that moment; later changes to the default in the dashboard, TUI, or CLI affect only new sessions. Calls that send no session ID share one `default` session that is never pinned and always follows the current default. Removing a project releases its pinned sessions to the remaining default. The Projects page shows how many sessions use each project.
- Results from file tools and `exec_command` include `project: {id, name}` when more than one project is approved, naming the project the path or working directory resolved to.
- **Full Access** permits file paths and terminal working directories outside added projects, subject to the current system account's permissions. It lasts only for the current run and is disabled after restart unless started with `--full-access`.
- Full Access does not enable terminal, desktop, or Chrome capabilities, grant root privileges, or bypass macOS privacy permissions. The page links to Full Disk Access settings; restart after granting access to ReadyRig or the terminal app that launches it.
- The local console and CLI can manage project access. Full Access is set through the local console or explicit launch flag. Agents can query them. Active file operations finish before access is revoked. Logs include the actual file path or command `cwd` to identify the project used.

```json
{"project":"<id-from-list_projects>","path":"README.md"}
```

With Full Access enabled, file tools can use `{"path":"/Users/you/Documents/notes.txt"}` and terminal tools can use `{"command":"pwd","cwd":"/Users/you/Downloads"}`.

## Google sign-in and cloud device control

Under **Connection → Cloud account**, sign in with Google in the system browser, verify the code, and bind your computer to the account. The website and device console run on Cloudflare Workers; D1 stores accounts, device heartbeats, and commands.

Open the [cloud console](https://readyrig.getmegaportal.com/console) with the same Google account to manage bound computers. The app reports status and retrieves tunnel, capability, and pause/resume commands every 15 seconds, including when public sharing is off. Devices appear offline after 60 seconds without a heartbeat. Commands expire if not retrieved within five minutes, and execution results return to the web console.

The official deployment has Google sign-in configured and verified. See the [cloud deployment guide](cloud/README.md) for setup and verification, or use `--cloud-url` / `READYRIG_CLOUD_URL` with your own deployment. The web console manages only computers bound to the signed-in account. Unbinding revokes device credentials. Fixed tunnel credentials, project folders, Full Access, and operating system permissions remain locally configured.

From the [device console](https://readyrig.getmegaportal.com/console), choose **Copy cloud prompt** to get a prompt with a Bearer credential tied to your current login session. It lets an agent list your computers, retrieve public links and submit computer controls such as enabling shell, changing capability switches, starting/stopping sharing and pausing/resuming control. Actual tools run directly through the computer's public link without a token. Signing out or session expiry stops future cloud queries and control submissions. See the [cloud computer API](cloud/README.md#cloud-computer-api) for endpoints and deployment requirements.

## Chrome DevTools MCP

ReadyRig starts and bridges the **official Chrome DevTools MCP** subprocess, adding its tools to REST, MCP, OpenAPI, and the Tools page. The application and bridge are written in Go; the official MCP subprocess requires Node.js.

1. In Chrome 144+, open `chrome://inspect/#remote-debugging`, enable remote debugging, and keep Chrome running.
2. Install Node.js 20.19+, 22.12+, or a later supported version with npx. ReadyRig prefers `chrome-devtools-mcp` on PATH, otherwise it runs `npx --yes chrome-devtools-mcp@1.10.1`. Initial setup downloads and caches the component. Apps launched from Finder also search common Homebrew, Volta, mise, and nvm locations, skipping incompatible Node versions already on PATH. The component and its subprocesses use the selected Node installation; if no supported version is found, diagnostics report the incompatible version and path.
3. Check Chrome status in Connection or Tools. Allow Chrome's connection request when the first tool call prompts you.
4. Refresh `tools/list` through the current ReadyRig MCP URL to use tools such as `chrome_list_pages`, `chrome_take_snapshot`, and `chrome_click`. Follow the upstream schemas, including `pageId` where required. Clients that cache tools must refresh or reconnect; the gateway does not push tool-list changes.

ReadyRig checks stable Chrome's `DevToolsActivePort` and local `127.0.0.1:9222` every five seconds. It attaches to an existing Chrome instance without launching a browser or enabling debugging. Tool definitions load independently of the browser connection: while Chrome is unavailable or its debugging file requires authorization, tools remain listed as waiting for a connection, and calls return the corresponding explanation. ReadyRig switches to the detected debugging endpoint when it becomes available. A ready status can still require Chrome's approval on the first operation.

Custom debugging port, profile directory, or MCP executable:

```sh
bin/readyrig-web web --chrome-browser-url http://127.0.0.1:9223
bin/readyrig-web web --chrome-user-data-dir /absolute/path/to/chrome-profile
bin/readyrig-web web --chrome-mcp-command /absolute/path/to/chrome-devtools-mcp
```

Only local HTTP debugging URLs are accepted, without redirects or remote WebSocket endpoints. Profile discovery reads the debugging endpoint file rather than browsing history or account data. Use `--no-chrome` to disable startup detection, or the Chrome browser switch to save that choice for future launches. The direct agent API cannot change this setting; a bound cloud account can.

Browser calls log arguments, duration, results, and failures and can be filtered by the browser category. The bridge preserves upstream schemas, annotations, text, images, and `structuredContent`. Browser screenshots appear in call details and JSON logs, but are excluded from desktop replay and desktop snapshot counts. Calls execute serially with a two-minute limit. Pause or cancellation disconnects the MCP subprocess; it reconnects after control resumes without closing your Chrome or retrying previously issued actions. Completed browser actions cannot be undone.

Chrome MCP can access the attached browser profile. Its upload and save tools, such as a screenshot's `filePath`, may only use paths inside your approved projects or the OS temp folder: ReadyRig declares the approved projects to the Chrome DevTools server, tells it again when they change, and a path anywhere else is refused with a message that says so. Full Access widens this to every folder. Upstream usage statistics and CrUX queries are disabled by default. ReadyRig manages its own MCP subprocess; it cannot share another client's existing stdio process.

See the [upstream guide to connecting to a running Chrome instance](https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/docs/advanced-usage.md#connecting-to-a-running-chrome-instance).

### Debugging file authorization on macOS

Chrome 144+ remote debugging exposes a WebSocket endpoint; a `404` from `/json/version` is expected in this mode. ReadyRig recognizes the approval server on local port `9222` (or the explicitly configured debugging URL) and connects to `/devtools/browser` without needing to read `DevToolsActivePort`. Detection uses an unsupported WebSocket path that Chrome rejects before asking for approval, so background checks do not trigger permission dialogs. The first browser operation still requires approval in Chrome. The endpoint file remains a fallback for discovering other ports.

If the local debugging server cannot be detected and macOS blocks the endpoint file, click **Authorize debugging file** in the desktop app and select `DevToolsActivePort` inside the Chrome folder. ReadyRig then detects the endpoint again. Alternatively, configure `--chrome-browser-url` with the address shown in Chrome's remote debugging page to connect without reading the file. Cancelling the dialog leaves permissions unchanged. If access is still denied, check ReadyRig's data access under **System Settings → Privacy & Security**; in browser mode, grant access to the terminal that starts ReadyRig. You must still approve the first browser connection in Chrome. Only the local desktop window can open this system dialog.

## Safari MCP

Safari 27 and later has an MCP server built in (`safaridriver --mcp`). ReadyRig bridges it the way it bridges Chrome, so Safari's tools appear as `safari_*` next to the Chrome tools. Safari has a switch of its own, separate from Chrome's: the toggle under Connection, `readyrig capability safari on|off`, the `w` key in the terminal dashboard, or `--no-safari`. The choice is saved like the others.

1. Use macOS 27 or later with Safari 27 or later. ReadyRig checks that `/usr/bin/safaridriver` offers `--mcp`. On any other system the Safari tools do not appear, and `safari` in `GET /api/state` says why.
2. In Safari, turn on the Develop menu, open **Safari > Settings > Developer**, and check **Allow remote automation and external agents**. ReadyRig cannot do this for you. Until it is on, a Safari call fails with an error that names this setting, and the Safari status becomes `permission_required`.
3. Start with `safari_create_tab` or `safari_navigate_to_url`. Safari opens a window it controls and shows a banner. Other tools can fail before such a window exists.

Safari's tools are exposed lazily. ReadyRig reads the tool list once with a short-lived process and does not connect to Safari until a Safari tool is first called. The connection then lasts until the app quits, the browser switch is turned off, or a call is cancelled, and a failed call is never retried by itself. Only five everyday tools are advertised in `tools/list`; `help` lists the other twelve and `use_tool` runs them, for example `{"name":"safari_list_tabs","arguments":{}}`. Safari's names are shortened where they repeated themselves: `browser_dialogs` is `safari_dialogs`.

Safari's server sends no read-only hints, so ReadyRig marks the tools that only read (`safari_get_page_content`, `safari_screenshot`, `safari_list_tabs`, `safari_page_info`, `safari_console_messages`, the network listings and `safari_wait_for_navigation`).

The dashboard shows Safari next to Chrome, in the Tools list and as a card under Connection with a short setup guide. The card reads Connected, Authorization required (remote automation is off) or Needs setup (this Mac has no Safari MCP), in both languages.

Safari's server writes files wherever it is told, and it has no notion of projects. So ReadyRig checks `savePath` on `safari_screenshot` and `safari_get_page_content` itself: it must be an absolute path inside an approved project, with `..` and symbolic links resolved, or the call is refused before it reaches Safari.

As with Chrome, a Safari window can reach whatever you are signed in to, `safari_evaluate_javascript` runs code in the page, and a `file://` page can show local files. Turn off the Safari switch to remove Safari while keeping Chrome, or the other way round.

### Cloud relay (backup for when the tunnel is unavailable)

Cloud relay is an opt-in backup for the public link. The tunnel stays the main path. When it is down, or you cannot run `cloudflared` at all, the app opens one outbound **WebSocket** to your cloud site, and the cloud MCP (`list_computer_tools`, `call_computer_tool`) sends each tool call down that socket. An MCP client such as Gemini Spark keeps working with no tunnel and no inbound connection.

**It is off by default, and it changes where your data goes.** A tunnel carries traffic between the agent and your computer without storing it. While the relay is connected, every tool argument and result — file contents, command output, screenshots — passes **through the ReadyRig Cloud server**. The server holds them only in memory while forwarding and stores none of them, but it does see them.

- **Where it lives.** In **Connection → Connect your agent → Public** you choose how agents reach this computer: **Direct link** (recommended; Agent → Cloudflare → this computer, and ReadyRig's servers never see the data) or **Via ReadyRig cloud** (Agent → ReadyRig cloud → this computer, with the data passing through ReadyRig's servers). Under Direct link, *If the link stops working, switch to ReadyRig cloud automatically* is the backup described below; the cloud route can also be used on its own, and shows the MCP address to add in your AI app. The cloud console shows the same two routes for each computer.
- **Turn it on at this computer, after signing in.** Ticking the backup checkbox, or choosing **I understand my data passes through ReadyRig servers. Turn on** on the cloud route, is the acknowledgement. From a terminal: `readyrig cloud relay on --yes` (without `--yes` it prints the warning and does nothing), or press `m` on the TUI Account tab and type `yes`. The choice is remembered across restarts; disconnecting the account forgets it.
- **Standby while the tunnel works.** With a working tunnel, relay is on but holds **no connection**, so no data can pass through the cloud. It connects when the tunnel is not ready (stopped, starting, failed) and steps back to standby once the tunnel has been ready for five seconds and no call is running.
- **Tunnel first.** The cloud MCP uses the tunnel link when there is a usable one. It uses the relay when there is none, or when the tunnel's edge answers `530` (the connector is gone, so the request never reached the computer). Any other tunnel failure is never replayed over the relay, because the call may already have run.
- **Turn it off anywhere.** The same switch, `readyrig cloud relay off`, `m` in the TUI, **Turn off relay mode** in the cloud console, or `control_computer` with `relay.stop`. Nothing in the cloud can turn relay *on*.
- **Same rules as a tunnel.** Relayed calls go through the same tool registry as gateway calls: capability switches, pause, project folders, Full Access and macOS permissions all apply, and every call is logged locally under a `cloud-…` session. At most eight run at once, and results over 8 MiB are refused.
- **MCP only.** The cloud REST API still lists and controls computers but does not relay tool calls. REST-only agents need a tunnel.

`readyrig cloud status` shows `relay` (`off`, `standby`, `connecting`, `connected`, `error`). See the [cloud guide](cloud/README.md#relay-mode) for the protocol and deployment notes.

## Remote access and sharing

Under **Connection → Connect your agent**, select **Public → Temporary link** and start sharing to obtain a temporary HTTPS URL through **Cloudflare Quick Tunnels**, without a Cloudflare account or domain. ReadyRig uses an installed `cloudflared` first, including common macOS Homebrew paths. Otherwise it downloads the official GitHub release, verifies SHA-256, and stores it in the private data directory under `cloudflared/`. It leaves system installations unchanged and does not read existing named-tunnel configuration or login credentials for temporary sharing.

**Copy for your agent** generates a connection prompt for the selected local or public URL. Paste it into an agent with terminal or HTTP tools. It first calls `help` and `list_projects` to check the connection and authorized folders, then follows the supplied task or waits for one. Copying reads the current URL again; no prompt is provided before public sharing is ready.

Connection details and diagnostics are collapsed by default. Once sharing connects, you can copy the agent URL and MCP configuration, or open the read-only web console:

- Agent: `https://example.trycloudflare.com/aSsxba11`
- MCP: `https://example.trycloudflare.com/aSsxba11/mcp`
- Web console: `https://example.trycloudflare.com/aSsxba11/app/`

The full URL, including its eight-character access path, is a credential. Anyone holding it can use enabled tools and view logs and screenshots. The public console is read-only. It cannot change permissions, projects, Full Access, pause state, updates, or tunnel settings. REST/MCP retain the configured capability and folder restrictions. The tunnel forwards only the agent port, leaving the local management port private.

URLs become available only after connection succeeds; failures show an error and recent diagnostics. You can cancel connection, stop sharing, or reconnect. Sharing is off by default and does not resume automatically after restart. Stopping sharing or quitting stops `cloudflared` and clears the public URL. Temporary sharing receives a new domain whenever it starts, and its random path changes whenever ReadyRig restarts. Fixed sharing retains its domain and a separate access path, so restarting sharing restores the same full URL. Stopping fixed sharing immediately rejects new requests using that path.

To enable temporary sharing explicitly from the command line:

```sh
bin/readyrig-web web --share
# Optionally select an installed cloudflared executable.
bin/readyrig-web web --share --cloudflared /opt/homebrew/bin/cloudflared
```

### Fixed links

Fixed links use a remotely managed Cloudflare named tunnel:

1. Create a `cloudflared` tunnel in your Cloudflare account and copy its Tunnel Token.
2. Add a published application route for your domain, such as `readyrig.example.com`, targeting ReadyRig's **agent API**: service type `HTTP`, address `127.0.0.1:7332` by default. Use the current gateway port if changed; the console displays the service address.
3. Under **Public → Fixed link**, enter the domain and token, save, and start sharing. A full HTTPS URL or bare domain is accepted; paths, IP addresses, and temporary `trycloudflare` domains are rejected.

Fixed links require `cloudflared` 2025.4.0+ with `--token-file` support. The token and fixed access path are saved in a private configuration file readable and writable only by the current user. The UI reports whether a token is saved without returning it. Startup passes the token through a temporary `0600` file, removes it on exit, and excludes it from command arguments and diagnostics. The public console cannot read or modify this configuration. ReadyRig verifies that the fixed domain reaches the current instance before exposing links. Saving configuration does not start sharing, and restarting does not enable it automatically.

See [Cloudflare named tunnel setup](https://developers.cloudflare.com/tunnel/get-started/) and the [token-file parameter](https://developers.cloudflare.com/tunnel/reference/run-parameters/#token-file). Configure your account, domain, and DNS routes in Cloudflare.

Quick Tunnels provide temporary sharing without a stable domain or availability guarantee. They support up to 200 concurrent requests and do not support SSE. A temporary link checks its own public address about every 20 seconds and again shortly after the computer wakes from sleep. If the address stops answering, ReadyRig hides it and creates a new link, so the URL changes. It does not restart a link you stopped. The public console refreshes every five seconds; MCP uses JSON HTTP responses. See the [official Quick Tunnels documentation](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/).

## Agent tools and APIs

REST, MCP, OpenAPI, and the console share one tool registry.

| Tool | Purpose | Execution |
| --- | --- | --- |
| `help` | Current tool definitions and status, by name, or as a compact name list | Concurrent; available while paused |
| `use_tool` | Runs any tool by name, including advanced tools not listed by `tools/list` | Concurrent |
| `batch` | Runs up to 12 tools in one request; read-only batches run in parallel | Concurrent |
| `list_projects` | Read-only project folders, default, and Full Access status | Concurrent; available while paused |
| `read_file` | Line-numbered, paged text reads; images as image content; Base64 | Concurrent |
| `write_file` | Atomic writes, parent directories, permissions kept, `create_only` | Serial (files) |
| `edit_file` | Exact-match replacement in an existing file | Serial (files) |
| `list_directory` | Entries with depth, glob filter, sorting; skips ignored paths when recursing | Concurrent |
| `glob` | Find files by glob pattern | Concurrent |
| `search_files` | Literal or regex search with context, `include` globs, and paging | Concurrent |
| `exec_command` | Commands with cwd, environment, login shell, background mode, and head-and-tail output | Concurrent |
| `write_stdin` | Input, polling (optionally a long poll for new output), stdin closure, and process termination | Concurrent |
| `list_tasks` | This session's command sessions: elapsed time, output size, time since last output, exit code; can wait for one or all running jobs | Concurrent |
| `computer_screenshot` | JPEG of a display (longest edge 1,280 pixels) or a zoomed region of an earlier frame | Serial (computer) |
| `computer_action` | Mouse, keyboard, scroll, drag, paste, and wait; one action or a chained `actions[]` batch | Serial (computer) |
| `computer_ui_tree` | Accessibility elements with refs that `computer_action` can click | Serial (computer) |
| `computer_app` | List windows; open or focus an application | Serial (computer) |
| `computer_clipboard` | Read or replace clipboard text | Serial (computer) |
| `chrome_*` | Dynamically discovered official Chrome DevTools MCP tools | Serial (browser) |
| `safari_*` | Dynamically discovered tools of Safari's built-in MCP server (Safari 27+) | Serial (browser) |

"Serial" means one call at a time within that capability: a slow browser call no longer delays a file write or a screenshot. Mutating tools also accept an optional one-line `description`, shown in the activity log and not passed to the tool. When it is omitted ReadyRig writes a short label itself, such as the first words of a command or `edit src/a.go`.

### Tool help

Call `help` with `{}` to obtain currently allowed tools, including names, descriptions, categories, complete `inputSchema`, upstream `outputSchema` and annotations, mutation and concurrency flags, and `enabled` / `available` status. Definitions come from the live registry.

Use `{"name":"exec_command"}` to inspect one tool even when disabled, or `{"include_disabled":true}` to include all registered tools and unavailable reasons (`capability_disabled` / `control_paused`). `available` reflects ReadyRig's capability and pause checks; operating system permissions and Chrome connection approval may still be required. Tools removed from the registry are absent from the list.

`help` is read-only, always enabled, works while paused, and is logged. Call it through `POST /api/v1/tools/help`, the MCP tool named `help`, or the local Tools page. `{"compact":true}` lists only names and one-line descriptions; fetch one full schema with `{"name":"..."}`.

### Tool listing and advanced tools

`tools/list` advertises only tools whose capability is enabled, so a model is not shown tools that would fail. The REST catalogue and the local Tools page still list everything. The twelve Chrome DevTools inspection tools that most browsing tasks do not need (console and network inspection, CSS styles, emulation, resizing, Lighthouse, performance traces, heap snapshots) form the `advanced` group: they stay callable but are left out of `tools/list`. `help` shows them, and `use_tool` with `{"name":"chrome_list_network_requests","arguments":{...}}` runs one. Safari's tools work the same way: five everyday ones (`safari_create_tab`, `safari_navigate_to_url`, `safari_get_page_content`, `safari_page_interactions`, `safari_screenshot`) are listed, and the other twelve (`safari_list_tabs`, `safari_evaluate_javascript`, `safari_dialogs`, and so on) are in the `advanced` group. Set `READYRIG_EXPOSE_ALL_TOOLS=1` before starting ReadyRig to advertise them all.

### File tools

- `read_file` returns `cat -n` style numbered lines, up to 2,000 lines or 128 KiB per call, and names the `start_line` that continues a longer file. `start_line`, `end_line`, and `limit` page through it, and the result reports `total_lines`. PNG, JPEG, GIF, and WebP files (up to 5 MiB) come back as image content. Use `encoding: "base64"` for other binary data.
- `edit_file` replaces `old_string` with `new_string` and fails unless it matches exactly once; `replace_all` replaces every match. It keeps file permissions and CRLF line endings and returns the first changed line with a numbered snippet. `write_file` keeps the permissions of an existing file, creates new files as 0644, and refuses to overwrite with `create_only`.
- `list_directory` accepts `depth` (up to 8), `pattern`, `type`, `sort` (`name`, `modified`, `size`), and `limit`. `glob` finds files by pattern such as `**/*.go` or `*.{md,txt}`. When they recurse, both skip `.git`, `node_modules`, and paths excluded by `.gitignore` files (nested files and negation included) unless `no_ignore` is set.
- `search_files` is literal by default. `regex` selects RE2 expressions; `case_insensitive`, `include`, `context` (0-5 lines), and `output: "files"` refine it. At most `max_results` matches are returned; a truncated result carries `next_offset` to pass back as `offset`.

### Batching

`batch` takes `calls: [{tool, arguments}, ...]` (up to 12, no nesting) and returns every result in order, in one request. A batch of read-only tools runs in parallel, otherwise the calls run in order; a failing call is reported and the rest still run unless `stop_on_error` is set. Each inner call is audited on its own and is subject to the same capability, pause, and permission checks. Use it to read several files and search in one round trip.

### Commands and background jobs

- A non-zero exit code is data: `exec_command` returns `exit_code` together with the output and the call is marked failed in the activity log, but it is not a tool error. Timeouts, and cancellation by Pause, Stop, or the client, are tool errors with the codes `timeout` and `cancelled`. A kill you request with `write_stdin` `terminate: true` is not an error: the result has `terminated: true` and no `exit_code`, and `list_tasks` and the finished-job notice say "terminated". `timeout` defaults to 600 seconds (3,600 with `background`) and may be up to 14,400. Finished command sessions are remembered for an hour; when 128 are held, the one that ended longest ago is forgotten.
- Each stream returns at most 30 KiB per call, as its start and end with the omitted size in between. The whole stream (up to 64 MiB) is saved under the data directory and reported as `stdout_path` / `stderr_path`; read it with `read_file` using that `spill:<name>` path. Saved output older than 24 hours is deleted when the next command starts.
- Children get a scrubbed environment with the usual tool directories (Homebrew, `~/.local/bin`, `~/go/bin`, Cargo, Bun, Volta) appended to `PATH`. `login_shell: true` runs the command through your login shell so profile-defined tools (nvm, pyenv) are found.
- `background: true` returns immediately with a `session_id`. When the job later ends, ReadyRig reports it in the next tool result of the same session as a `[notice]` text line and a `notices` field (REST responses carry `notices` too), and sends a `notifications/message` event to clients that hold the optional event stream described under MCP. At most 32 undelivered notices are kept per session.
- **Progress without streaming.** MCP progress notifications need a streamed response, which Quick Tunnels and many relays cannot carry, so progress is pull-based. A running result reports `elapsed_ms`, `output_bytes`, and `idle_ms` (time since the last output), which tells a busy job from a hung one. While a job has run for more than five seconds, any other tool result in the same session carries a `[progress]` line for it (and a `task_progress` entry in `notices`), at most once every 15 seconds and for at most five jobs. `list_tasks` shows every command session of the session at once, including recently finished ones and their saved-output paths. `write_stdin` waits up to `yield_time_ms` (at most 45 seconds, because the cloud relay drops requests near 55; 25 is safe on every path, so choose a value below your client's request timeout) and returns early when the process exits, so one call with a long wait is how to wait for completion, and a wait the client abandons (timeout, disconnect, cancellation) never stops the command; `return_on: "output"` also returns as soon as the job prints something, to follow a log. `list_tasks` with `wait: "any"` or `"all"` (and `yield_time_ms`) blocks until one or all running jobs finish, so several jobs need one call instead of one poll each.

### REST

The path `aSsxba11` below is an example. Copy the actual URL from Connection:

```sh
export READYRIG_URL="http://127.0.0.1:7332/aSsxba11"
curl "$READYRIG_URL/api/v1/tools"

curl "$READYRIG_URL/api/v1/fs/read" \
  -H 'X-Session-ID: example-task' \
  -H 'X-Client-Name: My Agent' \
  -H 'Content-Type: application/json' \
  -d '{"path":"README.md","start_line":1,"end_line":20}'
```

All tools accept `POST /api/v1/tools/{name}`. These aliases and the OpenAPI endpoint are relative to `READYRIG_URL`, including its access path:

```text
POST /api/v1/bash/exec
POST /api/v1/bash/stdin
POST /api/v1/fs/read
POST /api/v1/fs/write
POST /api/v1/fs/list
POST /api/v1/fs/search
POST /api/v1/fs/edit
POST /api/v1/fs/glob
POST /api/v1/computer/screenshot
POST /api/v1/computer/action
POST /api/v1/computer/ui-tree
POST /api/v1/computer/app
POST /api/v1/computer/clipboard
GET  /api/v1/openapi.json
```

Responses use `{call_id, status, result, error}`, plus `error_code` for failures (`unknown_tool`, `invalid_arguments`, `capability_disabled`, `control_paused`, `permission_required`, `timeout`, `cancelled`, `not_found`, `permission_denied`, `file_exists`, `is_directory`, `binary_file`, `too_large`, `outside_project`, `no_match`, `ambiguous_match`, `project_not_found`, `ambiguous_project`, `too_many_sessions`, `session_not_found`, `browser_not_ready`, `tool_error`), `images` when the tool returns images, and `notices` for finished background jobs. Tool execution failures return HTTP 422; paused control or disabled capabilities return 423. A command that exits non-zero returns HTTP 200 with `exit_code` and the output, and `status` is `success` (the activity log still marks the call failed). Missing, incorrect, or previous-run access paths return 404; cross-origin browser requests return 403; exceeding 240 requests per minute returns 429.

### MCP

Local and temporary URLs get a new cryptographically random eight-character alphanumeric path at startup. All agent routes sit beneath it. Fixed links use a separately persisted path accepted only while fixed sharing is active. No Bearer Token is needed. Legacy `agent-token` files are retained but unused. Copy a new local or temporary configuration after restart:

```json
{
  "mcpServers": {
    "readyrig": {
      "url": "http://127.0.0.1:7332/aSsxba11/mcp"
    }
  }
}
```

MCP uses HTTP POST JSON-RPC. `initialize` returns `Mcp-Session-Id`, which subsequent requests must include. Supported operations are `initialize`, `ping`, `logging/setLevel`, `tools/list`, `tools/call`, initialization notifications, and `notifications/cancelled`, which stops the named in-flight call. The gateway supports protocol 2025-06-18 and single-request JSON transport compatibility with 2025-03-26. JSON-RPC batches, MCP stdio, and server-initiated requests are unsupported. `serverInfo.version` is the ReadyRig version.

Tool results are returned as MCP content blocks: images as image content, then the plain-text body (for file reads, searches, listings, commands, and `batch`), then any `[notice]` or `[progress]` lines. A JSON block follows only for tools with no text form, as `{"result": ...}`, and for failures, as `{"error": ..., "error_code": ...}`; a successful text result carries no repeated metadata, which keeps results small. File and command bodies are not JSON-escaped.

A client may also open `GET /mcp` with `Accept: text/event-stream` and its `Mcp-Session-Id`. That optional stream carries `notifications/tools/list_changed` when a capability is switched or Chrome tools appear or disappear, and `notifications/message` events with `kind: "task_finished"` for background jobs. It is not available through Quick Tunnels or other proxies that buffer SSE; the `notices` mechanism works everywhere. `notifications/progress` is not sent for the same reason; the pull-based progress described under Commands and background jobs replaces it. See [Security boundaries and limitations](#security-boundaries-and-limitations) for disconnect behavior.

### Computer-use coordinates

1. Call `computer_screenshot` in the same session.
2. Read `frame_id` and `image_size` from the result.
3. Choose coordinates in the returned image's pixel space and send the `frame_id` with the action.
4. ReadyRig maps image pixels to macOS display points, including Retina scaling.

```json
{
  "action": "left_click",
  "frame_id": "<previous-frame-id>",
  "coordinate": [640, 360],
  "capture_after": true
}
```

Supported actions: `mouse_move`, `left_click`, `right_click`, `middle_click`, `double_click`, `triple_click`, `drag`, `scroll`, `type`, `paste`, `key`, and `wait`. Drag uses `to: [x,y]`; scroll uses `scroll_delta: [horizontal,vertical]` and, when given a `coordinate`, scrolls there; a key chord is any modifiers plus one key, `keys: ["cmd","c"]`; `modifiers: ["cmd","shift"]` are held during a click, drag, or scroll; `paste` places text on the clipboard, presses Cmd+V, and restores the previous clipboard, which suits long or non-ASCII text. Coordinates must be within the image. Frames must belong to the current session and be less than five minutes old. Take a new screenshot after display layout changes.

- **Batches.** `actions: [...]` runs up to 25 steps in order in one call (each with the same fields as a single action; the top-level `frame_id` is their default) and takes one screenshot at the end. Every step is validated before any runs. If a step fails, the error names it and the result reports `completed`.
- **Settling.** The screenshot returned after an action is taken once two consecutive captures are identical, waiting at most `settle_ms` (default 1,000; `0` captures after a fixed 200 ms). `capture_after: false` skips it.
- **Zoom and displays.** `computer_screenshot` with `region: [x1,y1,x2,y2]` and the `frame_id` it refers to returns that area at full resolution as a new frame, and coordinates in it map back to the screen. `display` selects a monitor; frames from a second monitor map to its position in the global coordinate space.
- **Accessibility.** `computer_ui_tree` lists an application's buttons, fields, menu items, and text with refs (`e12`). Pass `element: "e12"` to `computer_action` instead of a coordinate. Refs belong to the session that listed them and last five minutes. This needs the Accessibility permission, and window titles in `computer_app` need Screen Recording. A tool that lacks its macOS permission fails at once with `permission_required`, naming the permission to enable, and `help` lists it as unavailable with that reason.
- **Clipboard and apps.** `computer_clipboard` reads or replaces the clipboard text (it is recorded in the activity log); `computer_app` lists windows and opens or focuses an application by name.

## Automatic updates

Release builds check GitHub Releases five seconds after startup and every six hours thereafter. New versions download in the background. The menu bar and **Connection → Software updates** show progress, release notes, and a restart/install action. Normal exit also installs a completed download. The service continues during downloads; restarting or quitting ends active calls.

The default release repository is [jo32/readyrig](https://github.com/jo32/readyrig). Private releases can use local `gh auth login` credentials, including Homebrew installations when launched from Finder, or `READYRIG_UPDATE_TOKEN` with read-only Contents access. Credentials go only to the GitHub API and are excluded from packages, logs, the console, and redirected downloads.

Updates match the current operating system, architecture, and desktop/browser build. ReadyRig verifies size and SHA-256, plus macOS app signature integrity, signing team, and bundle ID. Signed installations cannot downgrade to ad-hoc signatures. Downloads retry up to three times, duplicate checks are merged, completed downloads survive later network failures, and replacement failures roll back. Concurrent processes cannot update the same installation.

`dev` and source-description builds do not self-update. Read-only or Homebrew-managed installations show manual update instructions without requesting administrator privileges in the background. An update restart preserves launch options and current capability choices, and generates a new local/temporary agent path. The macOS app restarts through LaunchServices as a fresh app process so its menu bar icon and window activation remain available. If an older version has already restarted without a menu bar icon, fully quit ReadyRig and reopen it from Applications once. Browser mode requires login through the newly printed dashboard URL. Fixed sharing must be restarted to restore its saved URL.

```sh
bin/readyrig version                 # Show the current version.
bin/readyrig update                  # Check, download, and install; use the console if already running.
bin/readyrig --no-update              # Disable updates for this run; or set READYRIG_NO_UPDATE=1.
make app VERSION=0.5.0               # Builds without VERSION are marked dev.
make release VERSION=0.5.0           # Packages, binaries, and SHA256SUMS in dist/releases/0.5.0/.
```

Use `--update-repo owner/repo` / `READYRIG_UPDATE_REPO` to change the repository, or `--update-feed URL` / `READYRIG_UPDATE_FEED` for a Magpie-compatible feed with `{version, notes, url, assets: {filename: {url, size, sha256}}}`. Custom feeds require HTTPS; loopback HTTP is allowed for local testing.

Update management endpoints are local only: `GET /api/update`, `POST /api/update/check`, and `POST /api/update/restart`. Call detail and cancellation endpoints, `GET /api/calls/{id}` and `POST /api/calls/{id}/cancel`, are also local only. Agents terminate their own commands through `write_stdin`.

## Security boundaries and limitations

- **Files:** Go `os.Root` confines file operations to authorized projects by default, permits absolute paths within them, and rejects parent traversal and symlinks pointing outside. Full Access permits other directories under the current account's permissions.
- **Terminal:** Commands run as the current user through `/bin/sh` on the host. Working directories are restricted to projects unless Full Access is enabled, but commands can still access other paths, networks, and devices available to that user. There is no Docker execution backend, PTY, or Windows shell adaptation. Terminal access is disabled by default. Timeouts default to 60 seconds and are capped at 600 seconds; output is capped at 1 MiB per stream. Cancellation stops process groups, but intentionally detached processes require operating system sandboxing to constrain.
- **Desktop:** macOS requires Screen Recording and Accessibility permissions. In the local dashboard, use the “Grant access” shortcuts under Connection to open the matching System Settings pane, allow ReadyRig, and restart it. When started from a terminal, grant permission to that terminal app. The driver uses system screenshots and CoreGraphics input events, controls the main display with the real pointer, and has no accessibility element tree, background window input, OCR, browser extension, or display selector. Native Windows/Linux drivers return an explicit unsupported error. Input is rejected when the pointer is at a screen corner; drags check cancellation and corner conditions and release held buttons. Pause cannot undo completed actions.
- **Network:** Agent and management interfaces are separate. Access paths use constant-time comparisons. REST/MCP reject browser Origin headers; the public read-only console permits same-origin GET/HEAD. The local console defends against cross-site requests and DNS rebinding. `--allow-ip 127.0.0.1/32,::1/128` restricts directly connected peers; client-supplied forwarded IPs are not trusted.
- **Logs and credentials:** Per-run random paths stay in memory, and data directories are created with `0700` permissions. Logs redact token/password/secret fields, the current random path, and dashboard key, but cannot identify every secret in free text or images. Treat logs and screenshots as sensitive local data. Logs persist without automatic cleanup or quotas. SQLite records call start and completion; restart marks unfinished calls as `interrupted`. Long-running command output is saved as it changes, and final results are recorded without client polling. Screenshots are stored separately from list summaries.
- **Cloud relay:** Off by default. While connected (only when the tunnel is not working), tool arguments and results pass through ReadyRig Cloud in memory, unlike a tunnel; it can be enabled only at the computer after an explicit acknowledgement, never by a cloud command.
- **Shared access:** Holders of an agent URL share one authorization identity. Sessions correlate logs rather than isolate tenants. Writes and clicks are not automatically retried. Turning off public sharing does not itself cancel commands already running.
- **MCP:** The gateway implements a limited subset without claiming full protocol certification. A disconnect may cancel the current request; check logs before reconnecting and repeating an operation whose outcome is unknown. Quick Tunnel web, REST, and MCP access have been verified; integration with real cloud agent clients remains unverified.
- **Distribution:** Official Mac releases require Developer ID signing, hardened runtime, a secure timestamp, and Apple notarization. App bundles receive a stapled notarization ticket before packaging. Versions 0.6.0 and earlier used ad-hoc signing; switching from those installations to Developer ID requires one manual installation. Later updates must retain the signing team and bundle ID. System permissions may need to be granted again when the build identity changes.

## Development and verification

```sh
make test                       # Go race tests.
node --check internal/server/assets/app.js
node --test scripts/test-i18n.cjs
make build                      # Native Wails application.
make cli                        # Browser/headless binary.
```

For the website and cloud service, see [website/README.md](website/README.md) and [cloud/README.md](cloud/README.md).

```text
cmd/adapter/         Launch options, HTTP entry points, application lifecycle
internal/brand/      Shared ReadyRig icon assets
internal/harness/    Tool specs, registry, dispatch, pause, files, projects, terminal
internal/chromemcp/  Chrome discovery and Safari's built-in MCP server: stdio bridges, dynamic tools
internal/cloud/      Account binding, device credentials, heartbeats, command receipts
internal/tunnel/     Temporary/fixed tunnels, credentials, validation, downloads, diagnostics
internal/computer/   Replaceable driver, screenshot compression, coordinates, macOS input
internal/store/      SQLite logs, filters, sessions, crash recovery
internal/server/     REST, MCP, authentication, OpenAPI, events, embedded console
internal/desktop/    Wails window and menu bar
internal/i18n/       Native menu and dialog translations
internal/update/     Release authentication, downloads, verification, exit installation
internal/buildinfo/  Build version and release repository
scripts/             Packaging and integration checks
website/             React website and cloud device console
cloud/               Cloudflare Worker, D1 migrations, cloud API
```

Tests cover file traversal and symlink escape, file operations, validation, redaction, pause and queued cancellation, stdin and asynchronous processes, timeout and process-group termination, output limits, final audit records, MCP initialization/calls, access-path authorization and rotation, Origin/CSRF/DNS rebinding, log recovery, Retina mapping, expired and cross-session frames, individual cancellation, capability isolation, live output without consuming agent reads, and summary/detail loading. Chrome tests cover subprocess protocols, paginated discovery, complex schemas, image/structured results, failures, cancellation/reconnection, disabling, and local address validation. Tunnel tests cover subprocess lifecycle, duplicate starts, cleanup, configuration isolation, readiness/timeouts, diagnostic limits, checksum verification, path/archive boundaries, and public read-only routes. Tests do not open real public tunnels or operate your desktop automatically.

`python3 scripts/test-update.py` verifies a real binary update from 0.4.0 to 0.5.0 and restart in a temporary directory, retaining logs and workspace data.

The [release workflow](.github/workflows/release.yml) tests and builds macOS Intel/Apple Silicon desktop packages and macOS/Linux/Windows CLI/browser binaries for both architectures, then publishes GitHub Releases when a `vX.Y.Z` tag is pushed. The curl installer consumes the existing `readyrig-web-*` binaries and ships alongside them as `install.sh`. The website serves the canonical installer from `website/public/install.sh`; publish a release containing the CLI commands and deploy the website to make the one-line installation available. Missing Developer ID or notarization credentials stop the release; existing releases are not overwritten. Local `make release` runs the same signing and notarization steps without uploading. Mac app packages and `SHA256SUMS` are generated only after Apple accepts the submission and the bundles pass ticket validation and Gatekeeper assessment.

For local releases, set `SIGN_IDENTITY` to a Developer ID Application name or fingerprint and optionally set `SIGN_KEYCHAIN`. Authenticate notarization with an existing `NOTARY_PROFILE` (and optionally `NOTARY_KEYCHAIN`), or use a team API key through `NOTARY_KEY_PATH`, `NOTARY_KEY_ID`, and `NOTARY_ISSUER_ID`. To build first and notarize later, run `VERSION=0.6.1 sh scripts/release.sh --prepare`, then run `sh scripts/release.sh --finish` with the same version and notarization credentials. Development `make app` builds can still use ad-hoc signing.

CI requires these GitHub Secrets: `MACOS_CERTIFICATE_P12_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `APPLE_NOTARY_KEY_BASE64`, `APPLE_NOTARY_KEY_ID`, and `APPLE_NOTARY_ISSUER_ID`. Signing credentials stay in a temporary keychain and temporary files, are removed afterward, and never enter the repository or release assets.

### Guards against regressions

Checks that keep later changes from quietly costing agents more:

- **Size budgets (Go tests).** `internal/app/budget_test.go` bounds the tool definitions: the `tools/list` total, each tool, and each description. `internal/server/budget_test.go` bounds the server instructions and the bytes an agent receives for common operations: write, read, edit, list, glob, search, commands, errors, batches, `list_tasks` after a long history, a REST response, and the 30 KiB output cap. They run with `go test`.
- **Behaviour benchmark (`make bench-check`, also run by the release workflow).** `scripts/bench-tools.py --check` starts an isolated build and runs the tasks agents actually do: a survey of several files, a very large command output, small operations, waiting for one silent job, waiting for two jobs, and a 70-second command under the default timeout. It fails if a task needs more requests or bytes than `scripts/bench-baseline.json` allows, takes longer than its limit, or stops completing. `scripts/bench-tools.py OLD NEW` compares two builds side by side.
- **Negative controls.** Each guard has a test that proves it fails when a budget is exceeded, so a guard that silently stops checking is caught.

When a change must cost more (a new tool, a richer result), raise the budget in the same commit: change the constant, or rewrite the baseline with `scripts/bench-tools.py --write-baseline BINARY`, and give the reason in CHANGELOG.md. Run `make bench-check` before starting a feature to see the headroom.

## Compatibility

The product name is **ReadyRig**, the desktop bundle is `ReadyRig.app`, and commands are `readyrig` / `readyrig-web`. The website and app share `internal/brand/assets/readyrig-app-icon.png`.

If `~/.local/share/readyrig` does not exist, ReadyRig reuses an existing `~/.local/share/readrig` or `~/.local/share/relay`, in that order, preserving projects, logs, and sharing configuration without moving or deleting data. The signing identifier remains `dev.local.relay` for system permission and update verification compatibility.

Update configuration uses `READYRIG_UPDATE_REPO`, `READYRIG_UPDATE_FEED`, `READYRIG_UPDATE_TOKEN`, and `READYRIG_NO_UPDATE`. Corresponding legacy `RELAY_*` variables remain fallbacks when the new variables are unset.

Release scripts produce primary `readyrig-*` packages and compatibility `readrig-*` / `relay-*` assets so existing clients can find updates. Compatibility desktop bundles retain their old folder and executable names while displaying ReadyRig. Historical release names remain unchanged.

## Design references and attribution

- [Telegram for macOS](https://github.com/overtake/TelegramSwift/blob/579cebbf0c01fd41b712eff3647fa7f69db9665d/packages/ObjcUtils/Sources/ObjcUtils/ObjcUtils.m#L475): the Open With design queries Launch Services for matching applications and shows their names and icons. ReadyRig independently implements this with current NSWorkspace APIs. The source reference was cloned with one commit and no submodules; the upstream menu call is commented out in that revision.
- [Codex](https://github.com/openai/codex): tool specifications, registry/executor separation, dispatch lifecycle, and progressive `exec_command` / `write_stdin` output. Reference commit: `94d642d8b40e45e2e544770f0d1f28df9a717f06` from a shallow, sparse checkout of main.
- [Magpie](https://github.com/yetone/magpie): Go, Wails v3, embedded HTML/CSS/JS, shared desktop/web HTTP handlers, compact information density, and call tracing. Reference commit: `74834748b98daeb295bf78b38170967e426e0c59`.
- [Product research](docs/product-research.md): Cua Driver, Peekaboo, Munim, Gokin Studio, Go MCP servers, and Bytebot, including reusable capabilities and unverified claims.

The execution backend is independently implemented. The console reuses Magpie's MIT-licensed theme variables, segmented navigation, statistics, and call-detail styles; see `internal/server/assets/MAGPIE-LICENSE.txt`. Automatic updates reference Magpie commit `575a8f5fe3bba6ca22f8ec0509eb3af88100aac0`, with attribution in `internal/update/MAGPIE-LICENSE.txt`. Future third-party drivers require separate API, license, and distribution reviews.
