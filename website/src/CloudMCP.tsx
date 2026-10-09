import { useEffect, useRef, useState } from 'react'
import { api } from './cloud-api'
import { mcpSetupPrompt } from './connection-prompt'
import { CopyButton } from './components/CopyButton'
import { useI18n } from './i18n'

type Client = { id: string; name: string; redirect_uris: string[]; automatic: boolean }
type Credential = { client_id: string; client_secret: string; mcp_url: string }
export function CloudMCP() {
  const { t } = useI18n()
  const dialog = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  const [panel, setPanel] = useState<'connect' | 'manual' | 'clients'>('connect')
  const [clients, setClients] = useState<Client[]>([])
  const [url, setURL] = useState('')
  const [name, setName] = useState('')
  const [redirects, setRedirects] = useState('')
  const [credential, setCredential] = useState<Credential | null>(null)
  const [showSecret, setShowSecret] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const load = async () => {
    const data = await api<{ mcp_url: string; clients: Client[] }>('/api/mcp/clients')
    setClients(data.clients); setURL(data.mcp_url)
  }
  useEffect(() => {
    if (!open) return
    let live = true
    void api<{ mcp_url: string; clients: Client[] }>('/api/mcp/clients')
      .then(data => { if (live) { setClients(data.clients); setURL(data.mcp_url) } })
      .catch(e => { if (live) setError(e.message) })
    return () => { live = false }
  }, [open])
  useEffect(() => {
    if (!open) return
    dialog.current?.showModal()
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.body.style.overflow = previous }
  }, [open])
  const close = () => { setOpen(false); setCredential(null); setShowSecret(false); setError('') }
  return <>
    <button className="button button-secondary" onClick={() => { setPanel('connect'); setOpen(true) }}>{t('连接 MCP')}</button>
    {open && <div className="cloud-mcp-backdrop">
      <dialog ref={dialog} className="cloud-mcp-dialog" aria-labelledby="mcp-title" onCancel={e => { e.preventDefault(); if (!busy) close() }}>
        <header className="cloud-mcp-header"><div><span className="cloud-mcp-eyebrow">READYRIG · MCP</span><h2 id="mcp-title">{t('连接你的 AI 助手')}</h2></div><button type="button" className="cloud-mcp-close" aria-label={t('关闭')} onClick={close} disabled={busy}>×</button></header>
        <nav className="cloud-mcp-tabs" aria-label={t('MCP 设置')}>
          {(['connect', 'manual', 'clients'] as const).map(item => <button key={item} type="button" aria-pressed={panel === item} onClick={() => setPanel(item)}>{t(item === 'connect' ? '快捷连接' : item === 'manual' ? '手动配置' : '授权管理')}</button>)}
        </nav>
        <div className="cloud-mcp-body">
        {panel === 'connect' && <div className="cloud-mcp-connect">
          <section className="cloud-mcp-prompt" aria-labelledby="mcp-prompt-title">
            <div className="cloud-mcp-intro"><h3 id="mcp-prompt-title">{t('让 AI 来设置')}</h3><p>{t('复制这段提示词，发送给你的 AI 助手。助手会配置连接，或指导你完成设置；登录授权仍由你确认。')}</p></div>
            {url ? <>
              <CopyButton text={mcpSetupPrompt(url, t)} label="复制说明" />
              <details><summary>{t('查看说明')}</summary><label className="cloud-mcp-field">{t('配置提示词')}<textarea readOnly rows={7} value={mcpSetupPrompt(url, t)} /></label></details>
            </> : <p role="status">{t('正在加载 MCP 地址…')}</p>}
          </section>
          <div className="cloud-mcp-intro"><h3>Gemini Spark</h3><p>{t('复制地址，登录授权，即可连接你的电脑。')}</p></div>
          <label className="cloud-mcp-field">MCP Server URL<div className="cloud-mcp-url"><input readOnly value={url} />{url && <CopyButton text={url} label="复制链接" />}</div></label>
          <ol className="cloud-mcp-steps"><li><span>1</span><div>{t('打开 Gemini Spark 的 Connected Apps')}</div></li><li><span>2</span><div>{t('粘贴 MCP 地址，点击 Next')}<small>{t('Client ID 和 secret 留空，系统会自动配置。')}</small></div></li><li><span>3</span><div>{t('登录 ReadyRig 并确认授权')}</div></li></ol>
          <p className="cloud-mcp-notice">{t('电脑需在线并开启公网分享。连接后，助手可调用你已开启的电脑工具。')}</p>
        </div>}
        {panel === 'manual' && <div>
        <div className="cloud-mcp-intro"><h3>{t('手动配置客户端')}</h3><p>{t('仅适用于要求填写 Client ID 和 secret 的客户端。Gemini Spark 可直接使用快捷连接。')}</p></div>
        {credential ? <section aria-label={t('新建的客户端凭证')}>
          <p role="status">{t('Client secret 仅显示这一次，请保存。关闭后如遗失，需要撤销并重新创建。')}</p>
          <label className="cloud-mcp-field">Client ID<input readOnly value={credential.client_id} /></label>
          <CopyButton text={credential.client_id} label="复制 Client ID" />
          <label className="cloud-mcp-field">Client secret<input readOnly type={showSecret ? 'text' : 'password'} value={credential.client_secret} autoComplete="off" /></label>
          <button type="button" className="button button-secondary" onClick={() => setShowSecret(!showSecret)}>{t(showSecret ? '隐藏密钥' : '显示密钥')}</button>
          <CopyButton text={credential.client_secret} label="复制 Client secret" />
          <button type="button" className="button button-secondary" onClick={() => setCredential(null)}>{t('已保存凭证')}</button>
        </section> : <form onSubmit={e => {
          e.preventDefault(); setBusy(true); setError('')
          void api<Credential>('/api/mcp/clients', 'POST', { name, redirect_uris: redirects.split('\n').map(s => s.trim()).filter(Boolean) })
            .then(async data => { setCredential(data); setShowSecret(false); setName(''); setRedirects(''); await load() })
            .catch(e => setError(e.message)).finally(() => setBusy(false))
        }}>
          <label className="cloud-mcp-field">{t('应用名称')}<input value={name} onChange={e => setName(e.target.value)} maxLength={128} placeholder="Gemini" required disabled={busy} /></label>
          <label className="cloud-mcp-field">{t('OAuth 回调地址')}<textarea value={redirects} onChange={e => setRedirects(e.target.value)} required disabled={busy} rows={3} /></label>
          <p className="cloud-note">{t('从客户端的连接说明获取准确的回调地址，每行一个。必须完全匹配，不支持通配符。')}</p>
          <button className="button button-primary" disabled={busy}>{t(busy ? '正在创建…' : '创建客户端凭证')}</button>
        </form>}
        </div>}
        {panel === 'clients' && <div className="cloud-mcp-management"><h3>{t('客户端与授权')}</h3>
        <p className="cloud-note">{t('授权最长保留 30 天，退出网页登录不会撤销。撤销后将立即停止此客户端的云端访问；已复制的公网链接需要关闭分享才能失效。')}</p>
        {clients.length ? <ul className="cloud-mcp-clients">{clients.map(client => <li key={client.id}>
          <div><strong>{client.name}</strong>{client.automatic && <small>{t('自动注册 · 本账号已授权')}</small>}<code>{client.id}</code>{client.redirect_uris.map(uri => <small key={uri}>{uri}</small>)}</div>
          <button className="button button-secondary" disabled={busy} onClick={() => {
            setBusy(true); setError('')
            void api('/api/mcp/clients/' + client.id, 'DELETE').then(async () => { if (credential?.client_id === client.id) setCredential(null); await load() }).catch(e => setError(e.message)).finally(() => setBusy(false))
          }}>{t('撤销')}</button>
        </li>)}</ul> : <p>{t('尚未创建 MCP 客户端')}</p>}
        </div>}
        {error && <p className="cloud-error" role="alert">{t(error)}</p>}
        </div>
        <footer className="cloud-mcp-footer">{t('可随时在授权管理中断开连接')}</footer>
      </dialog>
    </div>}
  </>
}
