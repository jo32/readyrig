import { useI18n, translateData, LanguageSelect } from './i18n'
import { Suspense, lazy, useEffect, useId, useRef, useState } from 'react'
import { AppIcon, GitHubIcon, Icon } from './components/Icon'
import type { IconName } from './components/Icon'
import { AppPreview, ConnectionExample } from './components/AppPreview'
import { UseCases } from './components/UseCases'
import { CloudConnection, SharingSection } from './components/RemoteAccess'
import { site } from './config'
import { CopyButton } from './components/CopyButton'

// The film brings its own markup and styles, so the hero loads it on demand.
const Film = lazy(() => import('./intro/Film'))

const featuresData: { number: string; icon: IconName; title: string; body: string; tags: string[] }[] = [
  {
    number: '01',
    icon: 'folder',
    title: '文件与终端，直接协作。',
    body: '读取项目、修改文件、搜索内容、运行命令。让 Agent 在你的工作环境里把事情做完。',
    tags: ['文件夹', '实时命令输出'],
  },
  {
    number: '02',
    icon: 'browser',
    title: '从浏览器，到整个桌面。',
    body: '接入官方 Chrome DevTools MCP；在 macOS 上截图、点击、输入，让网页与桌面操作连起来。',
    tags: ['Chrome DevTools', 'macOS 桌面'],
  },
  {
    number: '03',
    icon: 'activity',
    title: '看见过程，也看见结果。',
    body: '每次调用的参数、输出、状态与耗时，都在同一个界面。展开详情，或沿时间线查看桌面历史画面。',
    tags: ['执行日志', '屏幕回放'],
  },
  {
    number: '04',
    icon: 'shield',
    title: '每台电脑，分别控制。',
    body: '在本机或云端调整工具开关，暂停或恢复控制。每台电脑的项目目录和系统权限仍在本机设置。',
    tags: ['本机与云端控制', '每台电脑独立设置'],
  },
]

function ThemeButton() {
  const { t } = useI18n()
  const [theme, setTheme] = useState(() => (document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'))
  const [manual, setManual] = useState(() => {
    try {
      return ['light', 'dark'].includes(localStorage.getItem('readyrig-site-theme') || localStorage.getItem('readrig-site-theme') || '')
    } catch {
      return false
    }
  })

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#1c1a16' : '#d8d2c0')
  }, [theme])

  useEffect(() => {
    if (manual) return
    const media = matchMedia('(prefers-color-scheme: dark)')
    const change = () => setTheme(media.matches ? 'dark' : 'light')
    media.addEventListener('change', change)
    return () => media.removeEventListener('change', change)
  }, [manual])

  return (
    <button
      type="button"
      className="icon-button"
      aria-label={theme === 'light' ? t('切换深色外观') : t('切换浅色外观')}
      onClick={() => {
        const next = theme === 'light' ? 'dark' : 'light'
        setTheme(next)
        setManual(true)
        try {
          localStorage.setItem('readyrig-site-theme', next)
        } catch {
          /* Appearance still works without storage. */
        }
      }}
    >
      <Icon name={theme === 'light' ? 'moon' : 'sun'} width="18" height="18" />
    </button>
  )
}

function Header() {
  const { t } = useI18n()
  const [active, setActive] = useState('overview')
  const [menuOpen, setMenuOpen] = useState(false)
  const menuId = useId()
  const menuButton = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    const sections = ['overview', 'use-cases', 'getting-started', 'download']
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) if (entry.isIntersecting) setActive(entry.target.id)
      },
      { rootMargin: '-15% 0px -65% 0px' },
    )
    sections.forEach((id) => {
      const section = document.getElementById(id)
      if (section) observer.observe(section)
    })
    return () => observer.disconnect()
  }, [])

  return (
    <header className="site-header">
      <div className="header-inner" onKeyDown={(event) => {
        if (event.key === 'Escape' && menuOpen) {
          event.preventDefault()
          setMenuOpen(false)
          menuButton.current?.focus()
        }
      }}>
        <a className="wordmark" href="#overview" aria-label={t('ReadyRig 首页')} onClick={() => setMenuOpen(false)}>
          <AppIcon width="34" height="34" />
          <span>ReadyRig</span>
        </a>
        <a className="mobile-console-link button button-secondary" href="/console" aria-label={t('设备控制台')}>
          {t('控制台')}
        </a>
        <button
          ref={menuButton}
          type="button"
          className="mobile-menu-toggle icon-button"
          aria-label={t(menuOpen ? '关闭导航菜单' : '打开导航菜单')}
          aria-expanded={menuOpen}
          aria-controls={menuId}
          onClick={() => setMenuOpen((open) => !open)}
        >
          <Icon name={menuOpen ? 'close' : 'menu'} width="21" height="21" />
        </button>
        <div id={menuId} className={`header-menu${menuOpen ? ' is-open' : ''}`}>
          <nav className="site-nav segmented" aria-label={t('主导航')}>
            {[
              { id: 'overview', label: t('概览') },
              { id: 'use-cases', label: t('场景') },
              { id: 'getting-started', label: t('上手') },
            ].map((item) => (
              <a key={item.id} className={active === item.id ? 'active' : ''} href={`#${item.id}`} aria-current={active === item.id ? 'location' : undefined} onClick={() => setMenuOpen(false)}>
                {item.label}
              </a>
            ))}
          </nav>
          <div className="header-actions">
            <a className="header-console-link button button-small button-secondary" href="/console">{t('设备控制台')}</a>
            <a href={site.repository} className="icon-button github-link" aria-label={t('GitHub 仓库')} target="_blank" rel="noopener noreferrer">
              <GitHubIcon width={19} height={19} />
            </a>
            <LanguageSelect />
            <ThemeButton />
            <a className="button button-small button-primary" href="#download" onClick={() => setMenuOpen(false)}>
              {t('下载')}
              <span className="header-download-extra">ReadyRig</span>
            </a>
          </div>
        </div>
      </div>
    </header>
  )
}

function Download() {
  const { t } = useI18n()
  const [architecture, setArchitecture] = useState<'arm64' | 'amd64'>('arm64')
  const download = site.downloads[architecture]
  const installCommand = `curl -fsSL ${site.cliInstallURL} | sh`
  return (
    <section className="download-section section-width" id="download" aria-labelledby="download-title">
      <div className="download-card">
        <div className="download-intro">
          <AppIcon width="90" height="90" loading="lazy" />
          <div>
            <span className="eyebrow">{t('准备好开始了吗')}</span>
            <h2 id="download-title">{t('把你的电脑，连接起来。')}</h2>
            <p>{t('在每台电脑安装 ReadyRig，绑定同一账号，把云端 Prompt 交给 Agent。')}</p>
          </div>
        </div>
        <div className="download-actions">
          <label className="architecture-select">
            {t('选择你的 Mac')}
            <select aria-label={t('Mac 处理器')} value={architecture} onChange={(event) => setArchitecture(event.target.value as 'arm64' | 'amd64')}>
              <option value="arm64">Apple Silicon</option>
              <option value="amd64">Intel</option>
            </select>
          </label>
          <a className="button button-primary" href={download || site.releases} target="_blank" rel="noopener noreferrer">
            <Icon name="download" width="17" height="17" />
            {download ? t('下载 macOS 版') : t('获取 macOS 安装包')}
          </a>
          <span className="download-meta">{t('macOS 12 及以上')}</span>
        </div>
      </div>
      <div className="cli-install">
        <div className="cli-install-intro">
          <span className="eyebrow">CLI · Linux / macOS</span>
          <h3>{t('在 VM 和服务器上使用 ReadyRig')}</h3>
          <p>{t('安装 CLI，配置工具和项目目录，无需打开桌面窗口。')}</p>
          <a href={site.cliDocs} target="_blank" rel="noopener noreferrer">{t('CLI 使用指南')} <Icon name="chevron" width="13" height="13" /></a>
        </div>
        <div className="cli-install-command">
          <pre><code>{installCommand}</code></pre>
          <CopyButton text={installCommand} label="复制安装命令" />
          <span className="download-meta">{t('支持 Intel / AMD 和 ARM；安装后自动进入配置向导，运行 readyrig 打开终端界面。')}</span>
        </div>
      </div>
      <div className="download-under">
        <span>{site.releasesRequireAccess && !download ? t('安装包通过 GitHub Releases 提供，当前需要仓库访问权限。') : t('安装后，在本机选择要开放的工具与项目目录。')}</span>
        <a href={site.releases} target="_blank" rel="noopener noreferrer">
          {t('所有版本与浏览器版')}
          <Icon name="chevron" width="13" height="13" />
        </a>
      </div>
    </section>
  )
}

export default function App() {
  const { t } = useI18n()
  const features = translateData(featuresData, t)
  return (
    <div className="retro-site">
      <a className="skip-link" href="#main">
        {t('跳到主要内容')}
      </a>
      <Header />
      <main id="main">
        <section className="hero section-width" id="overview" aria-labelledby="hero-title">
          <div className="hero-intro">
            <AppIcon className="hero-icon" width="96" height="96" fetchPriority="high" />
            <div className="hero-kicker">
              <span>ReadyRig</span>
              <i />
              {t('同一账号，多台电脑。')}
            </div>
            <h1 id="hero-title">
              {t('一个 Agent，')}
              <br />
              <span>{t('管理多台电脑。')}</span>
            </h1>
            <p className="hero-description">
              {t('把 Mac、Linux 服务器和 VM 连接到同一个账号。')}
              <br />
              {t('Agent 查询电脑、选择环境，使用每台电脑开放的文件、终端、浏览器和桌面工具。')}
            </p>
            <div className="hero-actions">
              <a className="button button-primary" href="/console">
                <Icon name="monitor" width="18" height="18" />
                {t('管理我的电脑')}
              </a>
              <a className="button button-secondary" href="#download">
                {t('下载与安装')}
              </a>
            </div>
            <div className="hero-meta">
              <span>macOS · Linux</span>
              <i />
              <span>{t('桌面 App + CLI')}</span>
              <i />
              <span>REST + MCP</span>
            </div>
          </div>
          <div className="hero-film">
            <Suspense fallback={<div className="hero-film-fallback" aria-hidden="true" />}>
              <Film />
            </Suspense>
            <a className="hero-film-link" href="/intro">
              {t('观看开场短片')}
              <Icon name="chevron" width="13" height="13" />
            </a>
          </div>
          <CloudConnection />
        </section>
        <UseCases />
        <SharingSection />
        <section className="features-section section-width" id="features" aria-labelledby="features-title">
          <div className="section-heading">
            <span className="eyebrow">{t('在本机查看与控制')}</span>
            <h2 id="features-title">
              {t('Agent 在执行。')}
              <br />
              <span>{t('你看得见，也管得住。')}</span>
            </h2>
            <p>{t('查看它调用了什么、得到什么结果，随时调整权限或暂停新调用。')}</p>
          </div>
          <AppPreview />
          <div className="features-grid">
            {features.map((feature) => (
              <article className="feature" key={feature.number}>
                <div className="feature-icon-row">
                  <span className="feature-icon">
                    <Icon name={feature.icon} width="25" height="25" />
                  </span>
                  <span className="feature-number">{feature.number}</span>
                </div>
                <h3>{feature.title}</h3>
                <p>{feature.body}</p>
                <div className="feature-tags">
                  {feature.tags.map((tag) => (
                    <span key={tag}>{tag}</span>
                  ))}
                </div>
              </article>
            ))}
          </div>
          <div className="permission-note">
            <Icon name="shield" width="18" height="18" />
            <p>
              {t('工具在本机执行，日志保存在本机。')}
              <span>{t('调用结果仍会返回云端 Agent。')}</span>
            </p>
          </div>
        </section>
        <section className="getting-started section-width" id="getting-started" aria-labelledby="getting-started-title">
          <div className="setup-copy">
            <span className="eyebrow">{t('也支持直接连接')}</span>
            <h2 id="getting-started-title">{t('单台电脑，也能直接接入。')}</h2>
            <ol className="steps">
              <li>
                <span className="step-number">1</span>
                <div>
                  <h3>{t('选择目录，按需开启工具')}</h3>
                  <p>{t('安装并打开 ReadyRig，添加这次任务的工作目录。需要时再开放终端、Chrome 或桌面。')}</p>
                </div>
              </li>
              <li>
                <span className="step-number">2</span>
                <div>
                  <h3>{t('开启一次性公网链接')}</h3>
                  <p>{t('在「设置 → 连接 Agent」切换到「远程」，选择并开启「一次性链接」。')}</p>
                </div>
              </li>
              <li>
                <span className="step-number">3</span>
                <div>
                  <h3>{t('接入 Agent，交代任务')}</h3>
                  <p>{t('Cue 和 WorkBuddy 网页版：直接粘贴 App 生成的 Prompt。Gemini Spark：配置 App 提供的公网 MCP 地址。连接后，就可以安排本机任务。')}</p>
                </div>
              </li>
            </ol>
            <a href={site.docs} className="text-link" target="_blank" rel="noopener noreferrer">
              {t('阅读完整接入文档')}
              <Icon name="chevron" width="14" height="14" />
            </a>
          </div>
          <ConnectionExample />
        </section>
        <section className="faq-section section-width" aria-labelledby="faq-title">
          <div className="faq-heading">
            <span className="eyebrow">{t('再多了解一点')}</span>
            <h2 id="faq-title">{t('你可能想问。')}</h2>
          </div>
          <div className="faq-list">
            <details>
              <summary>
                {t('一个 Agent 可以管理多台电脑吗？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>{t('可以。在每台电脑运行 ReadyRig，绑定同一个 Google 账号，再从设备控制台复制「云端 Prompt」给支持网络请求的 Agent。它能查询这些电脑的状态与连接，按你的要求选择电脑、开关工具与分享、暂停或恢复控制。电脑需要保持在线；项目目录、Full Access 和系统权限仍在各电脑本地设置。')}</p>
            </details>
            <details>
              <summary>
                {t('ReadyRig 在整个任务里做什么？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>
                {t(
                  '你继续在已有的 Agent 里聊天、安排任务。ReadyRig 将它的工具请求接到你的电脑上执行，把结果返回给它，并在本机保留执行记录。ReadyRig 不内置模型，也可以让其他人的 Agent 通过分享地址使用你开放的工具。',
                )}
              </p>
            </details>
            <details id="connection-faq">
              <summary>
                {t('Cue、Gemini Spark、WorkBuddy 如何接入？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>
                {t('Cue 和 WorkBuddy 网页版可以直接粘贴 ReadyRig 生成的完整 Prompt，无需额外配置。Gemini Spark 需要添加远程 MCP：在 ReadyRig 的公网连接中复制 MCP 地址，按')}
                <a href="https://support.google.com/gemini/answer/17209137" target="_blank" rel="noopener noreferrer">
                  {t('Gemini Spark 的 MCP 接入流程')}
                </a>
                {t('完成配置。连接后，告诉 Agent 要处理哪个项目、完成什么任务即可。')}
              </p>
            </details>
            <details>
              <summary>
                {t('本机执行，资料还会传到云端吗？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>
                {t(
                  '会。工具在你的电脑执行，但文件内容、命令输出、网页信息或桌面截图会作为工具结果返回 Agent；分享链接的持有者还可以查看日志与截图。请按任务范围开放资料，并确认接入的云端服务如何处理数据。',
                )}
              </p>
            </details>
            <details>
              <summary>
                {t('哪些系统可以使用？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>{t('原生桌面 App 支持 macOS 12 及以上，提供 Apple Silicon 与 Intel 版本。浏览器版支持 macOS、Linux 和 Windows；原生桌面截图与输入操作目前仅支持 macOS。')}</p>
            </details>
            <details>
              <summary>
                {t('我可以控制 Agent 的访问范围吗？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>
                {t(
                  '可以。文件工具默认只访问你添加的项目目录，终端与桌面能力默认关闭。工具开关与暂停控制可以在本机或账号设备控制台管理，项目目录、Full Access 和系统权限由本机设置。终端命令使用当前账户权限执行，项目目录限制不等同于系统沙箱。',
                )}
              </p>
            </details>
            <details>
              <summary>
                {t('分享后，电脑可以关机吗？')}
                <Icon name="chevron" width="16" height="16" />
              </summary>
              <p>{t('本机任务需要电脑保持开机、联网，并运行 ReadyRig。休眠、关机或断网后，云端 Agent 无法继续调用这台电脑的工具。结束任务时，可以在本机关闭公网分享。')}</p>
            </details>
          </div>
        </section>
        <Download />
      </main>
      <footer className="site-footer section-width">
        <div>
          <a className="wordmark" href="#overview">
            <AppIcon width="28" height="28" loading="lazy" />
            <span>ReadyRig</span>
          </a>
          <span className="footer-tagline">{t('一个 Agent，你的多台电脑。')}</span>
        </div>
        <nav aria-label={t('页脚导航')}>
          <a href={site.docs} target="_blank" rel="noopener noreferrer">
            {t('文档')}
          </a>
          <a href={site.releases} target="_blank" rel="noopener noreferrer">
            {t('版本发布')}
          </a>
          <a href="/intro">
            {t('观看开场短片')}
          </a>
          <a href={site.repository} target="_blank" rel="noopener noreferrer">
            GitHub
          </a>
        </nav>
        <span className="footer-copyright">© {new Date().getFullYear()}ReadyRig</span>
      </footer>
    </div>
  )
}
