# ReadyRig Cloud Accounts and Device Control

The website, device console, and API share one Cloudflare Worker. D1 stores Google users, web sessions, device bindings, heartbeats, and command receipts. The service runs entirely on Cloudflare.

- [Website](https://readyrig.getmegaportal.com/)
- [Device console](https://readyrig.getmegaportal.com/console)
- D1 database: `readyrig-cloud`, configured in `wrangler.jsonc`.
- Web requests to the former `workers.dev` address redirect to the production domain. Device endpoints remain available to older apps; bound credentials require no migration.

See the [project README](../README.md) for the local app and the [changelog](../CHANGELOG.md) for version history.

## Relay mode

Relay is an opt-in backup that lets a computer whose Cloudflare tunnel is down or unavailable serve cloud MCP tool calls over a WebSocket. It is off by default. **Unlike a tunnel, tool arguments and results pass through this service while the relay is connected.** They are forwarded in memory by a Durable Object and never written to D1 or logs; only the connection state is stored (as part of the heartbeat snapshot).

```text
MCP client ──/mcp──▶ Worker ──▶ RelayHub (Durable Object, one per computer) ◀══ WebSocket ══ ReadyRig app
```

- `GET /api/agent/relay` with `Authorization: Bearer <device credential>` and a WebSocket upgrade. The device credential is the one used for heartbeats; browser sessions and Bearer discovery tokens cannot open it. A new connection replaces the old one, and unbinding or revoking a computer closes its socket.
- Frames are JSON text. Hub → app: `{"type":"call","id","tool","session","client","arguments"}` and `{"type":"cancel","id"}`. App → hub: `{"type":"result","id","http_status","body"}`, where `body` is the response the computer's own gateway would return for `POST /api/v1/tools/{tool}`. The app sends the text `ping` every 25 seconds and the hub answers `pong` without waking.
- Calls time out after 55 seconds, at which point the hub sends `cancel`. A timeout or a dropped socket reports that execution may have occurred; a computer with no socket reports that nothing was sent. Calls are never retried. The app runs at most eight at once and refuses results over 8 MiB.
- The app keeps the socket open only while its tunnel is not ready; with a working tunnel the relay is `standby` and nothing is connected. `computerTool` tries the tunnel first and uses the relay when the computer reports `relay.state: "connected"` and there is no usable link, or the tunnel edge answers `530`. Other tunnel failures are not replayed over the relay.
- Computers report `relay: { state, message }` (`off`, `standby`, `connecting`, `connected`, `error`) in the heartbeat snapshot. `GET /api/v1/computers` returns `relay: { state }`, which is `off` for an offline computer.
- `privacy.set` (payload `{"enabled": boolean}`) turns the computer's privacy mode on or off. Agents (cloud MCP and Bearer API) may only send `enabled: true`; turning it off is accepted only from the owner's signed-in console (`/api/devices/{id}/commands`). Heartbeats report `privacy: { enabled }`, and `GET /api/v1/computers` includes it.
- `relay.stop` (payload `{}`) turns relay off. There is deliberately no command that turns it on, because that needs the consent of the person at the computer. The app enables it with the local console, `readyrig cloud relay on --yes`, or the terminal dashboard.
- The cloud REST API does not relay tool calls. Only the MCP tools do, so REST-only agents still need a tunnel.

The Worker needs the `RELAY` Durable Object binding and the `v1-relay` migration in `wrangler.jsonc` (a SQLite-backed class, so it works on the free plan). `npm run deploy` applies them. Without the binding the service answers `503` to relay connections and apps report that relay mode is unavailable.

## Production domain and recovery

`readyrig.getmegaportal.com` uses a proxied Cloudflare `AAAA 100::` record. The `readyrig-cloud` Worker's `readyrig.getmegaportal.com/*` route handles every path without a Vercel origin. The Google OAuth client includes the production callback, and the app's default cloud URL uses this domain.

The former Vercel project `readyrig` (`prj_eV8ONBF48n8ynWrOHvIbdkg0Iux2`, team `team_c4my9iL2mRllE300soc8NtBD`) is paused. Preview deployments and automatic Git/deploy-hook deployments are disabled, and the custom domain binding is removed. The project and deployment history are retained; `readyrig.vercel.app` returns `503 DEPLOYMENT_PAUSED`.

To restore the former static website, resume the Vercel service, re-add the domain, change Cloudflare DNS to `CNAME readyrig → 47d77d4c7476c439.vercel-dns-016.com` with DNS only and TTL Auto, and remove the Worker route. Adjust Vercel deployment policies and preview settings if automatic deployments are also needed. The former static website cannot provide the current device APIs; assess connected apps before restoring it.

## Configure Google sign-in

The existing deployment has a `ReadyRig Web` client in Google project `readyrig-510216`, verified through a real Google sign-in. Its Client ID is in `wrangler.jsonc`, and its Client Secret is stored as a Cloudflare Worker Secret.

For your own deployment, create a **Web application** OAuth client in [Google Cloud Console](https://console.cloud.google.com/auth/clients), configure the app branding, and select an External audience. This project requests only `openid email profile`. Under [Google's audience rules](https://support.google.com/cloud/answer/15549945), these basic identity requests do not require a test-user list or display an unverified-app warning while in Testing. Adding other scopes requires reviewing the audience and verification configuration.

The existing Google app remains in Testing with unverified branding, so the consent page displays the app domain. Showing the ReadyRig name and icon requires a homepage, privacy policy, terms of service, and brand verification. Basic identity sign-in currently works without that branding verification.

Add this Authorized redirect URI:

```text
https://readyrig.getmegaportal.com/auth/callback
```

Set `vars.GOOGLE_CLIENT_ID` in `wrangler.jsonc`. Store the Client Secret only as a Worker Secret:

```sh
cd cloud
npm ci
npx wrangler secret put GOOGLE_CLIENT_SECRET
npm run deploy
```

The secret command prompts in the terminal. Keep the Client Secret out of source code, `VITE_*` values, and chat. Google authorization uses the system browser, state, PKCE, and nonce. The server verifies the signature through Google JWKS, issuer, audience, expiry, nonce, and verified email. It requests no Drive or Gmail data scopes and stores no Google access or refresh tokens.

Check `GET /api/health`: `google_configured: true` means both configuration values are present. Successful sign-in also requires correct Google callback and audience settings.

## Use the cloud console

1. Open **ReadyRig → Settings → Account**. The official URL is prefilled; you can enter your own deployment.
2. Click **Sign in with Google**, sign in through the system browser, verify the six-digit code shown in the app, and confirm device binding.
3. Sign in to the web console with the same account to view computer status, start/stop public sharing, choose temporary or fixed tunnels, rename devices, change file/terminal/browser/desktop switches, and pause/resume control.

Fixed domains and Tunnel Tokens remain locally configured and are not uploaded to the command database. Project folders, Full Access, and macOS permissions are managed locally. Binding authorizes that Google account to manage the supported switches; holders of the agent URL still cannot access account or management routes.

The app sends a heartbeat and retrieves one command every 15 seconds. Devices appear offline after 60 seconds without a heartbeat, and the website rejects new commands for offline devices. Network failures retry with backoff up to 60 seconds. Stopping the public tunnel leaves heartbeats active. Devices cannot execute commands after quitting, sleeping, or losing network access; heartbeat monitoring cannot remotely wake a sleeping or powered-off computer.

Commands queue in D1 and expire if not retrieved within five minutes. A retrieved command without confirmation after 90 seconds is marked as having an unknown result and is not automatically repeated. Receipts show success or failure; valid late receipts can complete unknown results. A successful tunnel-start command means the app accepted the request; connection readiness is reported separately in device status. After power loss, the app uses receipts saved before execution to report unconfirmed outcomes.

Device credentials are stored only in the private local data directory at `cloud/cloud.json` with `0600` permissions; the cloud stores only their SHA-256 hashes. Credentials do not follow HTTP redirects or get sent to a different service when the launch cloud URL changes. They remain valid until unbinding; web sessions expire after seven days. Unbinding revokes credentials and unfinished commands, but an already active tunnel must be stopped separately. The cloud stores defined device status without uploading local logs, screenshots, project paths, or Tunnel Tokens. Public agent URLs are visible to the device owner. Completed commands are retained for up to 30 days.

## Cloud computer API

Sign in to the console and choose **Copy cloud prompt**. It generates a Bearer credential tied to the current login session and includes it in the prompt. The credential can list and control computers bound to that account, including computers bound later. It expires with that session, and signing out invalidates it. The console keeps a single copy action without a separate Agent access management panel.

![Cloud console with Copy cloud prompt](../docs/readyrig-cloud-prompt.png)

| Endpoint | Authentication | Purpose |
| --- | --- | --- |
| `GET /api/v1/prompts?lang=zh-CN` | Public | Generic prompt with a credential placeholder; use `lang=en` for English |
| `POST /api/discovery-token` with `{}` | Same-origin signed-in browser session | Issue the computer API credential with the session's expiry |
| `GET /api/v1/computers` | `Authorization: Bearer <token>` | List this account's computers, status, enabled capabilities and public links |
| `GET /api/v1/computers/{id}` | Same Bearer token | Refresh one computer and its links |
| `POST /api/v1/computers/{id}/commands` | Same Bearer token | Queue a computer control change |
| `GET /api/v1/computers/{id}/commands` | Same Bearer token | Read the latest 30 command receipts, including status and errors |

To enable shell access, submit this JSON to the command endpoint:

```json
{
  "kind": "capability.set",
  "payload": { "category": "terminal", "enabled": true },
  "request_id": "a-new-unique-request-id"
}
```

| Command | Payload | Result |
| --- | --- | --- |
| `capability.set` | `category`: `files`, `terminal`, `browser` or `computer`; boolean `enabled` | Change a tool capability switch |
| `tunnel.start` | `mode`: `quick` or `fixed` | Start public sharing; fixed mode uses the computer's saved configuration |
| `tunnel.stop` | `{}` | Stop public sharing |
| `relay.stop` | `{}` | Turn relay mode off (it can only be turned on at the computer) |
| `privacy.set` | `{"enabled": true}` | Hide local paths from agents (agents can only turn it on; the console can also turn it off) |
| `control.pause` | Boolean `paused` | Pause or resume tool control |

A submission returns HTTP 202 with `id`, `kind`, `payload`, and `status`. It initially has status `queued`; the computer receives it on its next heartbeat and reports `completed` or `failed`. Poll command receipts for that ID before claiming success. Commands share the console's queue, with at most 20 pending/executing commands. Pending requests expire after five minutes, and unconfirmed execution expires after 90 seconds without automatic redelivery. Offline computers must reconnect to execute requests. Retries use the same `request_id` (at most 64 UTF-8 bytes) and identical command; a conflicting reuse returns 409. An idempotent retry works even if the queue is full.

An online computer with public sharing ready returns `links: { gateway, mcp, console }`. All URLs preserve the existing random access path. Offline computers, sharing that is not ready, and missing/invalid URLs return `links: null`. Refresh the computer after changing sharing, or when a temporary link rotates.

After discovery, the agent calls `POST {links.gateway}/api/v1/tools/help` and `POST {links.gateway}/api/v1/tools/list_projects` with `{}`, then invokes tools directly. A configured MCP client can use `links.mcp`. **Do not send the cloud Bearer token to the computer.** Actual shell execution and other tool calls use the computer's public URL, respecting its capability switches, pause state and project permissions.

Signing out stops future cloud queries and control submissions from that login session. Already accepted commands follow their normal execution/expiry lifecycle; public URLs already retrieved remain valid until sharing stops. Other login sessions and device binding credentials are unaffected. Cookies and device credentials do not authenticate this API, and its Bearer credential cannot manage login sessions, rename or unbind computers. Only credential hashes are stored; deleting a login session deletes its API credentials.

Before deployment, apply migration `0002_computer_discovery.sql` with `npm run db:remote`, then deploy the updated Worker and website together. The existing desktop client already handles these controls and requires no update for this feature.

## Deploy your own service

```sh
cd website && npm ci
cd ../cloud && npm ci
npx wrangler login
npx wrangler d1 create my-readyrig-cloud
```

Replace the Worker name, `account_id`, `database_name`, `database_id`, `routes`, and `PUBLIC_ORIGIN` in `wrangler.jsonc`. Replace or remove `LEGACY_ORIGIN`, configure the Google Client ID, secret, and callback, then run:

```sh
npm run db:remote
npm test
npm run deploy
```

Point the app at your deployment through the cloud website field, `--cloud-url https://your-domain`, `READYRIG_CLOUD_URL`, or the build value `computer-use-server/internal/buildinfo.CloudURL`. Already bound devices continue using their original service; disconnect the account before switching services.

## Local development and verification

Requires Node.js 22.12+; Node.js 24+ is recommended for integration tests. Local and production D1 databases are separate.

```sh
cd cloud
npm run build
npm run db:local
npm run dev
```

Open [the local site](http://localhost:8787). Configure a local Google Client ID in `.dev.vars`, use `.dev.vars.example` for the Client Secret, and add `http://localhost:8787/auth/callback` to the OAuth client. `npm run dev` overrides `PUBLIC_ORIGIN` and the local upstream host; this prevents production routes from rewriting local request origins. The API rejects hostnames that do not match `PUBLIC_ORIGIN`.

```sh
npm test                         # SQLite and signed mock Google identities; account isolation, commands, revocation.
npm run check                    # Worker type checks.
cd ..
go test -race -tags nogui ./...   # Local credentials, receipts, redirects, and public route isolation.
node scripts/test-cloud.mjs       # Local Worker/D1 → Go app → harmless tunnel process → receipt.
```

Integration tests create temporary directories and local test users. They do not connect to production D1, open real public tunnels, or bypass production Google sign-in. Local ports 18787, 18789, 17431, and 17432 must be available. Add `--keep` to retain test pages for visual verification; cleanup occurs when the test stops.

References: [Cloudflare Workers static assets](https://developers.cloudflare.com/workers/static-assets/), [D1](https://developers.cloudflare.com/d1/), and [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect).

## Cloud MCP with OAuth

In Gemini Spark, add `https://readyrig.getmegaportal.com/mcp` as the MCP server URL.
Spark can register its OAuth client automatically; no Client ID or secret needs
to be copied. Complete ReadyRig sign-in and review the browser consent page.
The console’s **Connect MCP** dialog also provides optional manual registration
for clients that require preconfigured credentials and exact redirect URLs.
These credentials are issued by ReadyRig, separate from Google sign-in credentials.

The endpoint implements stateless Streamable HTTP with JSON responses (authenticated
GET returns 405), protected resource metadata at
`/.well-known/oauth-protected-resource/mcp` and
`/.well-known/oauth-protected-resource`, and authorization server metadata at
`/.well-known/oauth-authorization-server`. Dynamic registration at `/oauth/register`
accepts public clients and clients using `client_secret_post` or
`client_secret_basic`. Registration is rate limited and grants no account access.
Authorization requires explicit signed-in consent, an exact registered redirect
URL, authorization code + S256 PKCE, and the `computers:control` scope.
Both authorize and token requests require `resource` equal to the MCP URL;
token requests may supply it in the query or form body.

Access tokens last one hour; grants expire after 30 days. Refresh tokens rotate,
and replay of an already used refresh token revokes its grant. Secrets, codes,
tokens and consent CSRF values are stored as hashes. Website sign-out does not
disconnect MCP. Revoke access in **Connect MCP** to invalidate your grants;
revoking a dynamically registered client does not affect other users. Previously
retrieved public computer URLs remain valid until public sharing stops.

Cloud MCP exposes `list_computers`, `get_computer`, `computer_commands`,
`control_computer`, `list_computer_tools`, and `call_computer_tool`. The first four
use the cloud API’s account ownership checks and command queue. The last two
relay tool discovery and execution to an owned, online computer with public
sharing ready, so Spark can use computer tools through one cloud connection.
Local capability switches, pause state, folder boundaries, Full Access and system
permissions still apply. The cloud forwards arguments and results, including
screenshots, but never forwards the cloud OAuth token to the computer.
Calls are not retried automatically if execution has an uncertain outcome.

When no tunnel is usable and the computer has opted in to [relay mode](#relay-mode), the same two tools forward over its WebSocket instead.

The relay permits HTTPS `*.trycloudflare.com` gateways by default. For fixed
tunnels, set the comma-separated `MCP_ALLOWED_TUNNEL_HOSTS` Worker variable to
exact trusted hostnames. Redirects and arbitrary destination URLs are rejected.

Apply `0003_mcp_oauth.sql` with `npm run db:remote` before deploying the Worker
and website together with `npm run deploy`. Verify with `npm run check`,
`npm test`, website checks/build, and `node scripts/test-cloud.mjs` from the root.
