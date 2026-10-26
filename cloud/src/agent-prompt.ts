// Shared by the cloud computer API and console. Cloud controls use Bearer auth;
// direct computer tools keep the existing random URL path.
export function agentPrompt(origin: string, token: string, locale = 'zh-CN'): string {
  const instructions = locale === 'en' ? `Please use ReadyRig to find my computers, apply the computer settings I request, and complete my task on the chosen computer.

Cloud computer endpoint: ${origin}/api/v1/computers
Cloud credential: Authorization: Bearer ${token}
Send this header only to ${origin} when listing computers or submitting/checking computer controls. Keep it private and never put it in URLs.

1. GET ${origin}/api/v1/computers with the cloud credential. Read computers, online/paused state, enabled capabilities and links. If several computers match my task, ask which one to use. Never invent computer IDs or links.
2. For computer settings I request, POST ${origin}/api/v1/computers/{computer_id}/commands with the cloud credential and JSON {"kind":"capability.set","payload":{"category":"terminal","enabled":true},"request_id":"<new-unique-id>"} to enable shell access. The same command enables/disables files, terminal, browser or computer with a boolean enabled. Other supported commands are tunnel.start with {"mode":"quick"} or {"mode":"fixed"}, tunnel.stop with {}, and control.pause with {"paused":true} or {"paused":false}. Change only the settings I request.
3. HTTP 202 means queued, not completed. Save the returned id and poll GET ${origin}/api/v1/computers/{computer_id}/commands with the cloud credential until that id has status completed, failed, expired or revoked. Report errors and do not claim success while it is queued/executing. Pending commands expire after five minutes; an offline computer needs ReadyRig running and connected before it can execute. If submission has an uncertain result, retry only with the same request_id and identical kind/payload; never use a new ID to repeat it blindly.
4. GET ${origin}/api/v1/computers/{computer_id} to refresh status and links after a completed control change, then choose the tool URL:
   - If links is not null, the tool URL is {links.gateway}/api/v1/tools/. Use links.gateway as the complete connection URL. Direct computer tool calls require no Bearer token. Preserve the random path and do not send the cloud credential to the computer. Temporary links may change after sharing or the app restarts, so query them again if a link stops working.
   - If links is null and relay.state is "connected", the computer uses cloud forwarding, which I turned on in ReadyRig. The tool URL is ${origin}/api/v1/computers/{computer_id}/tools/, called with the cloud credential. Requests and responses are the same as on links.gateway. Do not start a tunnel to replace cloud forwarding.
   - Otherwise there is no connection. Ask me to start ReadyRig if the computer is offline, or to turn on cloud forwarding; start a tunnel with tunnel.start only if I agree.
5. POST {tool URL}help with {} (for example {links.gateway}/api/v1/tools/help) to read live tool definitions and availability. Then POST {tool URL}list_projects with {} to check authorized folders. Verify these requests before saying that you are connected.
6. Invoke tools with POST {tool URL}{tool_name}, sending the tool's JSON arguments directly without an extra arguments wrapper. Use a consistent X-Session-ID header for the task, and follow session_id with write_stdin for running commands. If you only have configured MCP tools, ask me to add links.mcp as the MCP URL, or ${origin}/mcp for cloud forwarding (it signs in with OAuth, not with the cloud credential).

Only operate within my task and authorized folders. The cloud controls change computer settings; shell commands themselves run through the computer's tools. Do not broaden permissions beyond my request or bypass local project restrictions. A cloud 401 means the login session ended or expired. Ask me to sign in and copy a new cloud prompt. Signing out stops future cloud queries and controls; existing copied public links remain usable until public sharing is stopped. Do not automatically repeat writes or clicks after an uncertain response. If you have no HTTP or terminal tool and no suitable MCP connection, explain that you cannot connect.` : `请通过 ReadyRig 查询我的电脑，按我的要求调整电脑设置，然后在选定电脑上完成任务。

云端电脑接口：${origin}/api/v1/computers
云端账号凭证：Authorization: Bearer ${token}
这个请求头只用于 ${origin} 上的电脑列表查询、控制请求和执行结果查询。保护凭证，不要放进 URL。

1. 带上云端账号凭证，GET ${origin}/api/v1/computers。读取 computers、在线与暂停状态、能力开关和 links。如果有多台符合任务，请让我选择。不要编造电脑 ID 或链接。
2. 对于我要求的电脑设置变更，带上云端凭证，POST ${origin}/api/v1/computers/{computer_id}/commands。例如开启 shell 的请求体为 {"kind":"capability.set","payload":{"category":"terminal","enabled":true},"request_id":"<新的唯一请求编号>"}。同一命令可通过布尔值 enabled 开关 files、terminal、browser、computer。其他命令：tunnel.start 的 payload 为 {"mode":"quick"} 或 {"mode":"fixed"}；tunnel.stop 的 payload 为 {}；control.pause 的 payload 为 {"paused":true} 或 {"paused":false}。只调整我要求的设置。
3. HTTP 202 表示已排队，不代表已执行。保存返回的 id，带云端凭证 GET ${origin}/api/v1/computers/{computer_id}/commands，查询该 id 的状态，直到 completed、failed、expired 或 revoked。报告执行错误，不要在 queued/executing 时声称成功。待执行命令五分钟后过期；离线电脑需要 ReadyRig 运行并联网才能执行。提交结果不确定时，只用相同 request_id、kind、payload 重试，不要换新编号盲目重复提交。
4. 控制操作完成后，GET ${origin}/api/v1/computers/{computer_id} 刷新状态与 links，然后确定工具地址：
   - links 不为 null 时，工具地址是 {links.gateway}/api/v1/tools/。以 links.gateway 作为完整接入地址。直接调用电脑工具不需要 Bearer token；必须保留 URL 中的随机路径，不要把云端凭证发给电脑。一次性链接可能在公网分享或应用重启后变化，链接失效时重新查询。
   - links 为 null 且 relay.state 为 "connected" 时，这台电脑使用我在 ReadyRig 中开启的云端转发。工具地址是 ${origin}/api/v1/computers/{computer_id}/tools/，调用时带上云端凭证；请求与返回和 links.gateway 上的相同。不要为替代云端转发而开启公网分享。
   - 其他情况下没有可用连接。电脑离线时请让我启动 ReadyRig，或让我开启云端转发；只有我同意时才用 tunnel.start 开启公网分享。
5. POST {工具地址}help，请求体 {}（例如 {links.gateway}/api/v1/tools/help），读取实时工具定义与可用状态。再 POST {工具地址}list_projects，请求体 {}，确认授权目录。完成实测后才能声称已连接。
6. 后续 POST {工具地址}{tool_name}，直接发送工具所需 JSON，不额外套 arguments。为任务保持一致的 X-Session-ID 请求头，运行中的命令按 session_id 调用 write_stdin。如果你只能使用已配置的 MCP 工具，请让我把 links.mcp 添加为 MCP 地址；使用云端转发时添加 ${origin}/mcp（它通过 OAuth 登录，不使用云端凭证）。

只在我授权的任务与目录范围内操作。云端控制接口用于调整电脑设置，实际 shell 命令通过电脑工具执行。不要超出我的要求扩大权限，也不要绕过本机项目目录限制。云端返回 401 表示登录态已过期或已退出，请让我重新登录并复制云端 Prompt。退出登录会阻止后续云端查询与控制；已复制的公网链接需要关闭公网分享才能失效。不确定写入或点击是否执行时不要自动重试。如果你没有 HTTP 或终端工具，也没有合适的 MCP 连接，请明确说明无法接入。`
  return `<tools-usage>\n${instructions}\n</tools-usage>\n\n${locale === 'en' ? 'My task: ...' : '我想要：...'}`
}
