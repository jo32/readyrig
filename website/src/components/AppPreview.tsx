import { useI18n, translateData } from '../i18n'
import { useState } from 'react'
import { AppIcon, Icon } from './Icon'
import type { IconName } from './Icon'
import { Tabs } from './Tabs'
import { CopyButton } from './CopyButton'

type View = 'activity' | 'projects' | 'tools' | 'connection'

const viewsData: { value: View; label: string }[] = [
  { value: 'activity', label: '活动日志' },
  { value: 'projects', label: '文件夹' },
  { value: 'tools', label: '工具库' },
  { value: 'connection', label: '设置' },
]

const callsData = [
  {
    time: '10:42:08',
    tool: 'read_file',
    args: 'src/App.tsx',
    icon: 'folder',
    duration: '12 ms',
    input: '{ "path": "src/App.tsx" }',
    output: 'export default function App() {\n  return <main>Hello, ReadyRig.</main>\n}',
  },
  {
    time: '10:42:06',
    tool: 'exec_command',
    args: 'npm run build',
    icon: 'terminal',
    duration: '1.2 s',
    input: '{\n  "command": "npm run build",\n  "cwd": "~/Projects/website"\n}',
    output: '> website@1.0.0 build\n> vite build\n\n✓ 32 modules transformed.\n✓ built in 1.2s',
  },
  {
    time: '10:42:04',
    tool: 'chrome_take_snapshot',
    args: '页面快照',
    icon: 'browser',
    duration: '86 ms',
    input: '{ "pageId": 1 }',
    output: 'RootWebArea "ReadyRig"\n  heading "Hello, ReadyRig." level=1\n  link "开始使用"',
  },
  {
    time: '10:42:02',
    tool: 'search_files',
    args: '查找项目中的组件',
    icon: 'search',
    duration: '24 ms',
    input: '{ "pattern": "export default" }',
    output: 'src/App.tsx:1: export default function App()\nsrc/components/Header.tsx:4: export default function Header()',
  },
] satisfies { time: string; tool: string; args: string; icon: IconName; duration: string; input: string; output: string }[]

const capabilitiesData: { key: string; icon: IconName; title: string; description: string }[] = [
  { key: 'files', icon: 'folder', title: '文件', description: '读取、编辑和搜索你的文件。' },
  { key: 'shell', icon: 'terminal', title: '终端', description: '以你的身份运行命令，权限等同你的账户。' },
  { key: 'desktop', icon: 'monitor', title: '屏幕控制', description: '使用你的鼠标键盘。鼠标移到角落即停止。' },
  { key: 'chrome', icon: 'browser', title: 'Chrome', description: '接入官方 Chrome DevTools MCP' },
]

function ActivityPreview() {
  const { t } = useI18n()
  const calls = translateData(callsData, t)
  const [expanded, setExpanded] = useState<number | null>(1)
  return (
    <>
      <div className="preview-stats">
        <div>
          <strong>24</strong>
          <span>{t('操作次数')}</span>
        </div>
        <div>
          <strong>
            100<span className="stat-unit">%</span>
          </strong>
          <span>{t('成功率')}</span>
        </div>
        <div>
          <strong>
            86<span className="stat-unit">ms</span>
          </strong>
          <span>{t('平均用时')}</span>
        </div>
        <div>
          <strong>1</strong>
          <span>{t('活跃会话')}</span>
        </div>
      </div>
      <div className="preview-section-label">
        <span>{t('最近活动')}</span>
        <span className="preview-session">{t('website · 当前会话')}</span>
      </div>
      <div className="preview-list log-list">
        <div className="log-columns">
          <span>{t('时间')}</span>
          <span>{t('工具')}</span>
          <span>{t('参数 / 操作')}</span>
          <span>{t('状态')}</span>
          <span>{t('耗时')}</span>
        </div>
        {calls.map((call, index) => (
          <div className="log-item" key={call.tool}>
            <button
              type="button"
              className={`log-row ${expanded === index ? 'is-expanded' : ''}`}
              aria-expanded={expanded === index}
              onClick={() => setExpanded(expanded === index ? null : index)}
            >
              <span className="log-time">
                <Icon name="chevron" width="11" height="11" />
                {call.time}
              </span>
              <span className="log-tool">
                <Icon name={call.icon} width="15" height="15" />
                <code>{call.tool}</code>
              </span>
              <span className="log-args">{call.args}</span>
              <span className="success">{t('已完成')}</span>
              <span className="log-duration">{call.duration}</span>
            </button>
            {expanded === index ? (
              <div className="log-detail">
                <div>
                  <span>{t('输入')}</span>
                  <pre>{call.input}</pre>
                </div>
                <div>
                  <span>
                    {t('输出')}
                    <Icon name="check" width="13" height="13" />
                  </span>
                  <pre>{call.output}</pre>
                </div>
              </div>
            ) : null}
          </div>
        ))}
      </div>
    </>
  )
}

function ProjectsPreview() {
  const { t } = useI18n()
  const [selected, setSelected] = useState('website')
  const projects = [
    { name: 'website', path: '~/Projects/website' },
    { name: 'research', path: '~/Documents/research' },
    { name: 'automation', path: '~/Projects/automation' },
  ]
  return (
    <div className="projects-preview">
      <div className="preview-title">
        <h3>{t('文件夹')}</h3>
        <span>{t('选择 Agent 能打开的文件夹。')}</span>
      </div>
      <div className="preview-list">
        <div className="preview-list-heading">
          <span>{t('我的文件夹')}</span>
          <span className="small-tag">3</span>
        </div>
        {projects.map((project) => (
          <button className="project-row" type="button" key={project.name} onClick={() => setSelected(project.name)} aria-pressed={selected === project.name}>
            <Icon name="folder" />
            <span>
              <strong>{project.name}</strong>
              <code>{project.path}</code>
            </span>
            {selected === project.name ? <span className="small-tag">{t('默认项目')}</span> : <span className="project-select">{t('设为默认')}</span>}
          </button>
        ))}
      </div>
      <div className="preview-access-note">
        <Icon name="shield" />
        <div>
          <strong>{t('仅限列表')}</strong>
          <p>{t('文件工具的访问范围，由你在本机决定。')}</p>
        </div>
        <Icon name="check" />
      </div>
    </div>
  )
}

function ToolsPreview() {
  const { t } = useI18n()
  const capabilities = translateData(capabilitiesData, t)
  const [selected, setSelected] = useState('files')
  return (
    <div className="tools-preview">
      <div className="preview-title">
        <h3>{t('工具库')}</h3>
        <span>{t('Agent 能用的所有工具')}</span>
      </div>
      <div className="preview-list">
        {capabilities.map((capability) => (
          <div key={capability.key}>
            <button
              type="button"
              className="tool-preview-row"
              aria-expanded={selected === capability.key}
              onClick={() => setSelected(selected === capability.key ? '' : capability.key)}
            >
              <Icon name={capability.icon} />
              <span>
                <strong>{capability.title}</strong>
                <small>{capability.description}</small>
              </span>
              <Icon name="chevron" className={selected === capability.key ? 'rotated' : ''} width="15" height="15" />
            </button>
            {selected === capability.key ? (
              <div className="preview-tool-names">
                <code>
                  {capability.key === 'files'
                    ? 'read_file · write_file · list_directory · search_files'
                    : capability.key === 'shell'
                      ? 'exec_command · write_stdin'
                      : capability.key === 'desktop'
                        ? 'computer_screenshot · computer_action'
                        : 'chrome_list_pages · chrome_take_snapshot · chrome_click'}
                </code>
              </div>
            ) : null}
          </div>
        ))}
      </div>
    </div>
  )
}

function ConnectionPreview() {
  const { t } = useI18n()
  const capabilities = translateData(capabilitiesData, t)
  const [enabled, setEnabled] = useState<Record<string, boolean>>({ files: true, shell: true, desktop: false, chrome: true })
  return (
    <div className="connection-preview">
      <div className="preview-list">
        <div className="preview-list-heading">{t('权限')}</div>
        {capabilities.map((capability) => (
          <div className="capability-preview-row" key={capability.key}>
            <Icon name={capability.icon} />
            <div>
              <strong>{capability.title}</strong>
              <small>{capability.description}</small>
            </div>
            <button
              type="button"
              className={`switch ${enabled[capability.key] ? 'on' : ''}`}
              role="switch"
              aria-label={t('预览{0}权限', { 0: capability.title })}
              aria-checked={enabled[capability.key]}
              onClick={() => setEnabled({ ...enabled, [capability.key]: !enabled[capability.key] })}
            >
              <span />
            </button>
          </div>
        ))}
      </div>
      <div className="preview-list connection-info">
        <div className="preview-list-heading">
          {t('连接 Agent')}
          <span className="small-tag">REST + MCP</span>
        </div>
        <div className="connection-content">
          <small>{t('MCP 连接配置示例')}</small>
          <pre>{t('{\n  "mcpServers": {\n    "readyrig": {\n      "url": "<你的 MCP 地址>"\n    }\n  }\n}')}</pre>
          <p>{t('从 App 复制当前连接地址，添加到支持 HTTP MCP 的 Agent。')}</p>
          <span className="connection-local">
            <Icon name="shield" width="15" height="15" />
            {t('权限仅在本机修改')}
          </span>
        </div>
      </div>
    </div>
  )
}

export function AppPreview() {
  const { t } = useI18n()
  const views = translateData(viewsData, t)
  const [view, setView] = useState<View>('activity')
  const [paused, setPaused] = useState(false)
  return (
    <figure className="app-preview" aria-label={t('ReadyRig 交互式界面预览')}>
      <div className="preview-window">
        <div className="preview-chrome">
          <div className="traffic-lights" aria-hidden="true">
            <i />
            <i />
            <i />
          </div>
          <span>ReadyRig</span>
          <button type="button" className={`preview-pause ${paused ? 'paused' : ''}`} onClick={() => setPaused(!paused)} aria-label={paused ? t('恢复预览') : t('暂停预览')}>
            <Icon name={paused ? 'play' : 'pause'} width="12" height="12" />
            <span>{paused ? t('恢复') : t('暂停')}</span>
          </button>
        </div>
        <Tabs label={t('App 预览页面')} options={views} value={view} onChange={setView} className="preview-tabs">
          <div className="preview-content">
            <div className="preview-machine">
              <AppIcon width="28" height="28" />
              <span>
                {t('你的 Mac')}
                <span className="machine-meta">{t('本机工作空间')}</span>
              </span>
              <span className={`preview-status ${paused ? 'is-paused' : ''}`}>
                <i />
                {paused ? t('已暂停') : t('已连接')}
              </span>
            </div>
            {paused ? (
              <div className="preview-paused-banner">
                <Icon name="pause" width="14" height="14" />
                {t('已暂停接受新的工具调用。点击「恢复」继续预览。')}
              </div>
            ) : null}
            {view === 'activity' ? <ActivityPreview /> : view === 'projects' ? <ProjectsPreview /> : view === 'tools' ? <ToolsPreview /> : <ConnectionPreview />}
          </div>
        </Tabs>
        <div className="preview-footer">
          <span>
            <Icon name="shield" width="12" height="12" />
            {t('执行记录保存在本机')}
          </span>
          <span>{t('交互预览 · 示例数据')}</span>
        </div>
      </div>
      <figcaption>
        {t('每一次调用，都有迹可循。')}
        <span>{t('点击上方标签，看看 ReadyRig 如何工作。')}</span>
      </figcaption>
    </figure>
  )
}

export function ConnectionExample() {
  const { t } = useI18n()
  const [protocol, setProtocol] = useState<'prompt' | 'mcp' | 'rest'>('prompt')
  const examples = {
    prompt: t(
      '请通过 ReadyRig 连接我的电脑。\n公网 Agent 地址：<从 App 复制的公网 Agent 地址>\n\n先用 POST 请求调用：\n1. /api/v1/tools/help，请求体为 {}\n2. /api/v1/tools/list_projects，请求体为 {}\n\n确认连接和可用工具后，等我安排任务。\n只使用我授权的目录与工具。',
    ),
    mcp: t('{\n  "mcpServers": {\n    "readyrig": {\n      "url": "<从 App 复制的公网 MCP 地址>"\n    }\n  }\n}'),
    rest: t('curl -X POST \\\n  "<从 App 复制的公网 Agent 地址>/api/v1/tools/help" \\\n  -H "Content-Type: application/json" \\\n  -d \'{}\''),
  }
  const titles = { prompt: t('粘贴到 Agent 对话'), mcp: t('添加远程 MCP'), rest: t('先查看工具说明') }
  return (
    <div className="connection-example">
      <Tabs
        label={t('接入方式')}
        options={[
          { value: 'prompt', label: t('复制 Prompt') },
          { value: 'mcp', label: 'MCP' },
          { value: 'rest', label: 'REST' },
        ]}
        value={protocol}
        onChange={setProtocol}
      >
        <div className="code-toolbar">
          <span>
            {titles[protocol]}
            <span className="code-example-label">{t('示例')}</span>
          </span>
          <CopyButton text={examples[protocol]} label={t('复制示例')} />
        </div>
        <pre className={protocol === 'prompt' ? 'prompt-example' : ''}>
          <code>{examples[protocol]}</code>
        </pre>
      </Tabs>
      <div className="code-footnote">
        <Icon name="link" width="16" height="16" />
        <span>
          {t(
            '这里是占位示例。安装后，在 App「设置 → 远程」复制包含当前地址的完整 Prompt。Cue 和 WorkBuddy 网页版直接粘贴 Prompt；Gemini Spark 切换到 MCP，使用 App 提供的公网 MCP 地址配置连接。',
          )}
        </span>
      </div>
    </div>
  )
}
