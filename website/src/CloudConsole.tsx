import { useCallback, useEffect, useState } from 'react'
import { AppIcon, Icon } from './components/Icon'
import { CopyButton } from './components/CopyButton'
import type { IconName } from './components/Icon'
import { publicConnectionPrompt } from './connection-prompt'
import { LanguageSelect, useI18n } from './i18n'
import type { Locale } from './locale'
import { api, APIError } from './cloud-api'
import { CloudMCP } from './CloudMCP'
import { CloudPromptButton } from './CloudPromptButton'
import './cloud-console.css'

type User = { email: string; name: string }
type Device = {
  id: string
  name: string
  platform: string
  online: boolean
  last_seen: number
  snapshot: {
    version?: string
    paused?: boolean
    enabled?: Record<string, boolean>
    tunnel?: { state?: string; message?: string; mode?: string; gateway?: string; mcp?: string; console?: string }
    relay?: { state?: string; message?: string }
  }
}
type Command = {
  id: string
  kind: string
  payload: Record<string, unknown>
  status: string
  created_at: number
  error?: string
}
type Pair = { name: string; platform: string; code: string; approved: boolean }
const commandNames: Record<string, string> = {
  'tunnel.start': '开启公网',
  'tunnel.stop': '关闭公网',
  'relay.stop': '关闭云端转发',
  'control.pause': '调整暂停状态',
  'capability.set': '调整能力开关',
}
const statusNames: Record<string, string> = {
  queued: '等待电脑领取',
  executing: '已送达，等待结果',
  completed: '已执行',
  failed: '执行失败',
  expired: '已过期',
  revoked: '设备已解绑',
}
// Relay is an opt-in backup for when the public link is unavailable. The console only reports it
// and can switch it off; turning it on needs the person at the computer, because it sends tool
// data through this service. While the public link works the relay stays on standby.
const relayStatusNames: Record<string, string> = {
  off: '未开启',
  standby: '待命',
  connecting: '连接中',
  connected: '已连接',
  error: '连接失败',
}
const capabilities: Record<string, string> = {
  files: '文件',
  terminal: '终端',
  browser: 'Chrome 浏览器',
  computer: '桌面操作',
}
const capabilityIcons: Record<string, IconName> = {
  files: 'folder',
  terminal: 'terminal',
  browser: 'browser',
  computer: 'monitor',
}
const platformNames: Record<string, string> = { darwin: 'macOS', linux: 'Linux', windows: 'Windows' }
function GoogleMark() {
  return (
    <svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24">
      <path
        fill="#4285F4"
        d="M21.6 12.23c0-.71-.06-1.39-.18-2.05H12v3.88h5.38a4.6 4.6 0 0 1-1.99 3.02v2.51h3.23c1.89-1.74 2.98-4.31 2.98-7.36Z"
      />
      <path
        fill="#34A853"
        d="M12 22c2.7 0 4.96-.9 6.62-2.41l-3.23-2.51c-.9.6-2.05.96-3.39.96-2.61 0-4.82-1.76-5.61-4.12H3.05v2.59A10 10 0 0 0 12 22Z"
      />
      <path fill="#FBBC05" d="M6.39 13.92a6 6 0 0 1 0-3.84V7.49H3.05a10 10 0 0 0 0 9.02l3.34-2.59Z" />
      <path
        fill="#EA4335"
        d="M12 5.96c1.47 0 2.79.51 3.83 1.51l2.87-2.87A9.6 9.6 0 0 0 12 2a10 10 0 0 0-8.95 5.49l3.34 2.59A5.93 5.93 0 0 1 12 5.96Z"
      />
    </svg>
  )
}
function time(seconds: number, locale: Locale, fallback: string) {
  return seconds ? new Date(seconds * 1000).toLocaleString(locale) : fallback
}

function DeviceCard({ device, refresh }: { device: Device; refresh: () => Promise<void> }) {
  const { t, locale } = useI18n()
  const [commands, setCommands] = useState<Command[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [editing, setEditing] = useState(false),
    [name, setName] = useState(device.name),
    [mode, setMode] = useState('quick')
  const load = useCallback(async () => {
    const result = await api<{ commands: Command[] }>(`/api/devices/${device.id}/commands`)
    setCommands(result.commands)
  }, [device.id])
  useEffect(() => {
    let alive = true
    const update = () => {
      if (alive)
        void load().catch((e) => {
          if (alive) setError(e.message)
        })
    }
    update()
    const timer = setInterval(update, 5000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [load])
  const send = async (kind: string, payload: Record<string, unknown>) => {
    setBusy(true)
    setError('')
    try {
      await api(`/api/devices/${device.id}/commands`, 'POST', { kind, payload, request_id: crypto.randomUUID() })
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  // A computer uses a direct link or ReadyRig cloud, so switching turns cloud forwarding off first.
  // Commands run one at a time in order, so the link only starts after the relay has stopped.
  const switchToDirect = async () => {
    setBusy(true)
    setError('')
    try {
      await api(`/api/devices/${device.id}/commands`, 'POST', { kind: 'relay.stop', payload: {}, request_id: crypto.randomUUID() })
      await api(`/api/devices/${device.id}/commands`, 'POST', { kind: 'tunnel.start', payload: { mode }, request_id: crypto.randomUUID() })
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const snapshot = device.snapshot,
    tunnel = snapshot.tunnel || {},
    pending = commands.some((c) => c.status === 'queued' || c.status === 'executing'),
    disabled = busy || pending || !device.online
  const relay = snapshot.relay || {},
    relayState = device.online && relay.state && relayStatusNames[relay.state] ? relay.state : 'off',
    relayOn = relayState !== 'off'
  const tunnelReady = tunnel.state === 'ready',
    summaryLine =
      tunnelReady && relayState === 'standby'
        ? '直连链接已开启 · 云端备用待命'
        : !tunnelReady && relayState === 'connected'
          ? tunnel.state === 'stopped' || !tunnel.state
            ? '经 ReadyRig 云端已连接'
            : '直连链接未就绪 · 云端备用已连接'
          : tunnelReady
            ? '已开启'
            : tunnel.state && ['installing', 'starting', 'stopping'].includes(tunnel.state)
              ? '正在连接或关闭'
              : '未开启',
    mcpURL = `${window.location.origin}/mcp`
  const active = ['installing', 'starting', 'ready', 'stopping'].includes(tunnel.state || '')
  const prompt = tunnel.state === 'ready' && tunnel.gateway ? publicConnectionPrompt(tunnel.gateway, tunnel.mode, t) : ''
  // ReadyRig cloud is carrying the connection (no tunnel is running): show that route alone.
  const cloudActive = relayOn && relayState !== 'standby' && !active
  return (
    <article className="cloud-device">
      <div className="cloud-device-heading">
        <span className="cloud-computer-icon">
          <Icon name="monitor" width="22" height="22" />
        </span>
        <div className="cloud-device-name">
          <h2 title={device.name}>{device.name}</h2>
          <p>
            {platformNames[device.platform] || device.platform} · ReadyRig {snapshot.version || '—'}
          </p>
        </div>
        <div className="cloud-device-status">
          <span className={`cloud-presence ${device.online ? 'online' : ''}`}>
            <i />
            {t(device.online ? '在线' : '离线')}
          </span>
          <span className="cloud-last-seen" title={time(device.last_seen, locale, t('尚未上报'))}>
            {t('最近心跳')} ·{' '}
            {device.last_seen
              ? new Date(device.last_seen * 1000).toLocaleString(locale, {
                  month: 'short',
                  day: 'numeric',
                  hour: '2-digit',
                  minute: '2-digit',
                  hour12: false,
                })
              : t('尚未上报')}
          </span>
        </div>
      </div>
      <div className="cloud-device-body">
        <section className="cloud-sharing" aria-label={t('公网访问')}>
          <div className="cloud-section-heading">
            <h3>{t('公网访问')}</h3>
            <span className={`cloud-tunnel-status ${tunnelReady || relayState === 'connected' ? 'ready' : ''}`}>{t(summaryLine)}</span>
          </div>
          {!cloudActive && (
          <div className="cloud-route">
          <div className="cloud-route-head">
            <strong>{t('直连链接')}</strong>
            <span className="cloud-route-badge">{t('推荐')}</span>
          </div>
          <p className="cloud-route-flow">
            {t('Agent → Cloudflare → 这台电脑')} · {t('数据不经过 ReadyRig 服务器')}
          </p>
          <p className="cloud-sharing-copy">
            {t(
              tunnel.state === 'ready'
                ? '复制 Prompt，粘贴给 Agent 即可连接。'
                : '开启后，云端 Agent 就能使用这台电脑的工具。',
            )}
          </p>
          <div className="cloud-actions">
            {prompt && <CopyButton text={prompt} label="复制 Prompt" />}
            {!active && (
              <select
                aria-label={t('公网链接类型')}
                value={mode}
                onChange={(e) => setMode(e.target.value)}
                disabled={disabled}
              >
                <option value="quick">{t('一次性链接')}</option>
                <option value="fixed">{t('固定域名')}</option>
              </select>
            )}
            <button
              className={`button ${prompt ? 'button-secondary' : 'button-primary'}`}
              disabled={disabled}
              onClick={() => void send(active ? 'tunnel.stop' : 'tunnel.start', active ? {} : { mode })}
            >
              {t(active ? '关闭公网' : '开启公网')}
            </button>
          </div>
          {tunnel.state === 'ready' && tunnel.gateway && (
            <>
              <div className="cloud-address">
                <label>{t('Agent 地址')}</label>
                <code>{tunnel.gateway}</code>
                <button
                  className="button button-secondary"
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(tunnel.gateway!)
                      .catch(() => setError(t('复制失败，请手动复制地址')))
                  }}
                >
                  {t('复制地址')}
                </button>
              </div>
              <details className="cloud-prompt-preview">
                <summary>
                  {t('查看 Prompt')}
                  <Icon name="chevron" width="14" height="14" />
                </summary>
                <textarea aria-label={t('连接 Prompt')} value={prompt} readOnly spellCheck={false} />
              </details>
            </>
          )}
          {tunnel.state === 'error' && tunnel.message && <p className="cloud-error">{t(tunnel.message)}</p>}
          </div>
          )}
          <div className={`cloud-route ${relayState === 'connected' ? 'is-active' : ''}`}>
            <div className="cloud-route-head">
              <strong>{t('经 ReadyRig 云端')}</strong>
              <span className={`cloud-tunnel-status ${relayState === 'connected' ? 'ready' : ''}`}>
                {t(relayStatusNames[relayState])}
              </span>
              {relayOn && (
                <button
                  className="button button-secondary"
                  disabled={disabled}
                  onClick={() => void send('relay.stop', {})}
                >
                  {t('关闭云端转发')}
                </button>
              )}
            </div>
            <p className="cloud-route-flow">
              {t('Agent → ReadyRig 云端 → 这台电脑')} · {t('数据会经过 ReadyRig 服务器')}
            </p>
            <p className={`cloud-route-copy ${relayOn && relayState !== 'standby' ? 'cloud-relay-warning' : ''}`}>
              {t(
                relayState === 'off'
                  ? '在这台电脑的 ReadyRig 中开启。可作为直连链接失效时的备用，也可单独使用。'
                  : relayState === 'standby'
                    ? '直连链接正常时待命，不传数据。'
                    : '文件内容、命令输出和截图正经 ReadyRig 服务器转发。',
              )}
            </p>
            {relayState === 'connected' && (
              <div className="cloud-address">
                <label>{t('MCP 地址（在 AI 应用中添加）')}</label>
                <code>{mcpURL}</code>
                <button
                  className="button button-secondary"
                  onClick={() => {
                    void navigator.clipboard.writeText(mcpURL).catch(() => setError(t('复制失败，请手动复制地址')))
                  }}
                >
                  {t('复制地址')}
                </button>
              </div>
            )}
            {relayState === 'error' && relay.message && <p className="cloud-error">{t(relay.message)}</p>}
          </div>
          {cloudActive && (
            <div className="cloud-route-switch">
              <button className="button button-secondary" disabled={disabled} onClick={() => void switchToDirect()}>
                {t('改用直连链接')}
              </button>
              <p>{t('会先关闭云端转发，再开启直连链接。')}</p>
            </div>
          )}
        </section>
        <section className="cloud-access" aria-label={t('访问权限')}>
          <div className="cloud-section-heading">
            <h3>{t('访问权限')}</h3>
            <span className="cloud-access-state">{t(snapshot.paused ? '控制已暂停' : '可用工具')}</span>
          </div>
          <div className="cloud-capabilities">
            {Object.entries(capabilities).map(([key, label]) => (
              <label key={key} className={disabled ? 'is-disabled' : ''}>
                <span className="cloud-capability-label">
                  <Icon name={capabilityIcons[key]} width="17" height="17" />
                  <span>{t(label)}</span>
                </span>
                <input
                  type="checkbox"
                  aria-label={t(label)}
                  role="switch"
                  checked={snapshot.enabled?.[key] || false}
                  disabled={disabled}
                  onChange={(e) => void send('capability.set', { category: key, enabled: e.target.checked })}
                />
                <span className="cloud-switch" aria-hidden="true" />
              </label>
            ))}
          </div>
        </section>
      </div>
      <div className="cloud-device-footer">
        <button
          className="button button-secondary"
          disabled={disabled}
          onClick={() => void send('control.pause', { paused: !snapshot.paused })}
        >
          <Icon name={snapshot.paused ? 'play' : 'pause'} width="14" height="14" />
          {t(snapshot.paused ? '恢复控制' : '暂停控制')}
        </button>
        <button
          className="button button-secondary"
          disabled={busy}
          onClick={() => {
            setName(device.name)
            setEditing(!editing)
          }}
        >
          {t('重命名')}
        </button>
        <button
          className="cloud-danger"
          disabled={busy}
          onClick={() => {
            if (!confirm(t('解绑后，网页将无法控制这台电脑。已开启的公网链接仍需关闭。确认解绑？'))) return
            setBusy(true)
            void api(`/api/devices/${device.id}`, 'DELETE')
              .then(refresh)
              .catch((e) => setError(e.message))
              .finally(() => setBusy(false))
          }}
        >
          {t('解绑电脑')}
        </button>
      </div>
      {editing && (
        <form
          className="cloud-rename"
          onSubmit={(e) => {
            e.preventDefault()
            setBusy(true)
            void api(`/api/devices/${device.id}`, 'PATCH', { name })
              .then(async () => {
                setEditing(false)
                await refresh()
              })
              .catch((e) => setError(e.message))
              .finally(() => setBusy(false))
          }}
        >
          <input
            aria-label={t('电脑名称')}
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            required
          />
          <button className="button button-secondary" disabled={busy}>
            {t('保存名称')}
          </button>
        </form>
      )}
      {!device.online && (
        <p className="cloud-note">{t('电脑离线，请确认 ReadyRig 正在运行且电脑没有休眠。恢复心跳后可下发命令。')}</p>
      )}
      {error && (
        <p className="cloud-error" role="alert">
          {t(error)}
        </p>
      )}
      <details className="cloud-command-history">
        <summary>
          <span>{t('最近命令')}</span>
          <span>{pending ? t('正在等待电脑处理') : t('{0} 条记录', { 0: commands.length })}</span>
          <Icon name="chevron" width="14" height="14" />
        </summary>
        {commands.length ? (
          <ol>
            {commands.map((c) => (
              <li key={c.id}>
                <div>
                  <strong>{t(commandNames[c.kind] || c.kind)}</strong>
                  <span>{t(statusNames[c.status] || c.status)}</span>
                </div>
                <small>
                  {time(c.created_at, locale, t('尚未上报'))}
                  {c.kind === 'capability.set'
                    ? ` · ${t(capabilities[String(c.payload.category)] || '')} ${t(c.payload.enabled ? '开启' : '关闭')}`
                    : ''}
                  {c.kind === 'control.pause' ? ` · ${t(c.payload.paused ? '暂停' : '恢复')}` : ''}
                </small>
                {c.error && <p className="cloud-error">{t(c.error)}</p>}
              </li>
            ))}
          </ol>
        ) : (
          <p>{t('暂无命令')}</p>
        )}
      </details>
    </article>
  )
}

export default function CloudConsole() {
  const { t } = useI18n()
  const [user, setUser] = useState<User | null>(null),
    [devices, setDevices] = useState<Device[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(''),
    [pair, setPair] = useState<Pair | null>(null),
    [approved, setApproved] = useState(false),
    [pairBusy, setPairBusy] = useState(false),
    [configured, setConfigured] = useState(true)
  const pairID = new URLSearchParams(location.search).get('pair')
  const refresh = useCallback(async () => {
    const data = await api<{ devices: Device[] }>('/api/devices')
    setDevices(data.devices)
  }, [])
  useEffect(() => {
    let live = true
    void api<{ user: User }>('/api/me')
      .then(async (data) => {
        if (!live) return
        setUser(data.user)
        await refresh()
        if (pairID) {
          const result = await api<Pair>(`/api/pairings/${encodeURIComponent(pairID)}`)
          if (live) setPair(result)
        }
      })
      .catch((e) => {
        if (live && !(e instanceof APIError && e.status === 401)) setError(e.message)
      })
      .finally(() => {
        if (live) setLoading(false)
      })
    void api<{ google_configured: boolean }>('/api/health')
      .then((data) => {
        if (live) setConfigured(data.google_configured)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [pairID, refresh])
  useEffect(() => {
    if (!user) return
    const timer = setInterval(() => {
      void refresh().catch((e) => setError(e.message))
    }, 5000)
    return () => clearInterval(timer)
  }, [user, refresh])
  return (
    <div className={`cloud-shell${user ? '' : ' cloud-auth'}`}>
      <header className="cloud-header">
        <a className="wordmark" href="/">
          <AppIcon width="32" height="32" />
          <span>ReadyRig</span>
        </a>
        {user && <span>{t('设备控制台')}</span>}
        <LanguageSelect />
        {user && (
          <div className="cloud-account">
            <span>{user.email}</span>
            <button
              className="button button-secondary"
              onClick={() => {
                void api('/api/logout', 'POST', {})
                  .then(() => location.assign('/console'))
                  .catch((e) => setError(e.message))
              }}
            >
              {t('退出登录')}
            </button>
          </div>
        )}
      </header>
      <main className="cloud-main">
        {user && (
          <div className="cloud-title">
            <div>
              <h1>{t('已连接的电脑')}</h1>
              <p>{t('管理已连接电脑的共享和访问权限。')}</p>
            </div>
            <div className="cloud-title-actions">
              {devices.length > 0 && (
                <span className="cloud-device-count">
                  {t('{0} 台电脑 · {1} 在线', { 0: devices.length, 1: devices.filter((device) => device.online).length })}
                </span>
              )}
              <div className="cloud-actions"><CloudPromptButton disabled={!devices.length} /><CloudMCP /></div>
            </div>
          </div>
        )}
        {loading ? (
          <p role="status">{t('正在加载…')}</p>
        ) : !user ? (
          <section className="cloud-sign-in" aria-labelledby="sign-in-title">
            <h1 id="sign-in-title">{t('登录 ReadyRig')}</h1>
            <p>{t('连接你的电脑。')}</p>
            {configured ? (
              <a
                className="button cloud-google-button"
                href={'/auth/google?return_to=' + encodeURIComponent(pairID ? '/console?pair=' + pairID : '/console')}
              >
                <GoogleMark />
                {t('使用 Google 登录')}
              </a>
            ) : (
              <>
                <button className="button cloud-google-button" disabled>
                  <GoogleMark />
                  {t('使用 Google 登录')}
                </button>
                <span className="cloud-auth-status" role="status">
                  {t('登录暂未开放')}
                </span>
              </>
            )}
          </section>
        ) : (
          <>
            {pair && !approved && (
              <section className="cloud-pair">
                <h2>
                  {t('绑定这台电脑')} · {pair.name}
                </h2>
                <p>
                  {t('核对 ReadyRig 显示的验证码，确认这是你正在登录的电脑。绑定后，本账号可远程开启公网与调整能力。')}
                </p>
                <strong className="cloud-pair-code">{pair.code}</strong>
                <button
                  className="button button-primary"
                  disabled={pairBusy}
                  onClick={() => {
                    setPairBusy(true)
                    void api(`/api/pairings/${pairID}`, 'POST', {})
                      .then(() => {
                        setApproved(true)
                        history.replaceState(null, '', '/console')
                        void refresh()
                      })
                      .catch((e) => setError(e.message))
                      .finally(() => setPairBusy(false))
                  }}
                >
                  {t('确认绑定到我的账号')}
                </button>
              </section>
            )}
            {approved && (
              <p className="cloud-pair-confirmed" role="status">
                <Icon name="check" width="16" height="16" />
                {t('绑定已确认，可以返回 ReadyRig。')}
              </p>
            )}
            {devices.length ? (
              <div className="cloud-device-grid">
                {devices.map((device) => (
                  <DeviceCard key={device.id} device={device} refresh={refresh} />
                ))}
              </div>
            ) : (
              <div className="cloud-empty">
                <h2>{t('还没有绑定的电脑')}</h2>
                <p>{t('打开 ReadyRig → 连接 → 云端账号，填写本站地址并登录 Google。')}</p>
                <code>{location.origin}</code>
              </div>
            )}
            <details className="cloud-help">
              <summary>
                {t('连接与设备说明')}
                <Icon name="chevron" width="14" height="14" />
              </summary>
              <p>
                {t(
                  '命令通常在下次心跳时送达，约 15 秒。60 秒未收到心跳显示离线；命令 5 分钟后过期。固定域名和 Tunnel Token 在本机配置。',
                )}
              </p>
            </details>
          </>
        )}
        {error && (
          <p className="cloud-error" role="alert">
            {t(error)}
          </p>
        )}
      </main>
    </div>
  )
}
