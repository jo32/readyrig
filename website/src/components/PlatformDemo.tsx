import { useI18n, translateData } from '../i18n'
import { useState } from 'react'
import { AppIcon, Icon } from './Icon'
import { Tabs } from './Tabs'
import './platform-demo.css'

const platformsData = [
  {
    id: 'cue',
    name: 'Cue',
    badge: 'C',
    method: '粘贴 Prompt 即可',
    url: 'https://cue.im/',
    title: '把本机资料，整理成一份报告。',
    setup: '在 ReadyRig 复制 Prompt，直接粘贴到 Cue 对话。不需要额外配置。',
    task: '整理 research 文件夹里的访谈记录，归纳共同问题，把报告保存到同一目录。',
    reply: '已整理 8 份访谈记录，归纳了 3 个主要问题。报告已写回你的电脑。',
    folder: 'research',
    file: '访谈总结.md',
    actions: ['读取 research 中的访谈记录', '归纳共同问题与原文依据', '写入 research/访谈总结.md'],
    result: ['访谈总结', '8 份记录 · 3 个共同问题', '01  初次上手的步骤不够清晰', '02  重复录入占用了工作时间', '03  团队需要统一的资料入口'],
  },
  {
    id: 'spark',
    name: 'Gemini Spark',
    badge: '✦',
    method: '配置 MCP 接入',
    url: 'https://support.google.com/gemini/answer/17209137',
    title: '让云端 Agent，用你的开发环境。',
    setup: '在 Gemini Spark 添加 ReadyRig 的远程 MCP，填入 App 中复制的公网 MCP 地址，完成连接。',
    task: '检查 website 项目，运行构建，把结果和需要处理的问题保存为构建报告。',
    reply: '已在你的电脑上完成构建。构建通过，报告保存在项目目录，可继续安排下一步。',
    folder: 'website',
    file: 'build-report.md',
    actions: ['查看 website 项目与构建脚本', '在本机终端执行 npm run build', '写入 website/build-report.md'],
    result: ['构建报告', 'website · 本机开发环境', '✓  TypeScript 检查通过', '✓  生产构建完成', '输出目录  dist/'],
  },
  {
    id: 'workbuddy',
    name: 'WorkBuddy 网页版',
    badge: 'W',
    method: '粘贴 Prompt 即可',
    url: 'https://www.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/CloudAgent',
    title: '从线上安排，让本机浏览器来完成。',
    setup: '在 ReadyRig 复制 Prompt，直接粘贴到 WorkBuddy 网页版对话，然后交代任务。',
    task: '用我电脑上的 Chrome 检查本地官网，查看首页和导航，把检查结果保存到 website 目录。',
    reply: '已检查本机 Chrome 中的官网，首页和导航均可正常使用。检查报告已保存到你的电脑。',
    folder: 'website',
    file: '页面检查.md',
    actions: ['连接本机 Chrome，打开本地官网', '查看首页并检查导航跳转', '写入 website/页面检查.md'],
    result: ['页面检查', '本地官网 · Chrome', '✓  首页内容正常显示', '✓  导航可到达对应板块', '✓  报告已保存到项目目录'],
  },
] as const

type Platform = (typeof platformsData)[number]['id']
type Stage = 'connect' | 'run' | 'result'

export function PlatformDemo() {
  const { t } = useI18n()
  const platforms = translateData(platformsData, t)
  const [platform, setPlatform] = useState<Platform>('cue')
  const [stage, setStage] = useState<Stage>('result')
  const current = platforms.find((item) => item.id === platform)!
  const connected = stage !== 'connect'

  return (
    <div className="platform-demo" id="platform-demo">
      <Tabs
        label={t('云端 Agent 演示')}
        className="platform-selector"
        options={platforms.map((item) => ({
          value: item.id,
          label: (
            <>
              <strong>{item.name}</strong>
              <span>{item.method}</span>
            </>
          ),
        }))}
        value={platform}
        onChange={setPlatform}
      >
        <div className="platform-demo-heading">
          <div>
            <h3>{current.title}</h3>
            <p>{current.setup}</p>
          </div>
          <a href={current.url} target="_blank" rel="noopener noreferrer">
            {t('官方介绍')}
            <Icon name="chevron" width="12" height="12" />
          </a>
        </div>
        <div className="agent-window">
          <div className="agent-window-chrome">
            <span className="agent-window-dots" aria-hidden="true">
              <i />
              <i />
              <i />
            </span>
            <span>
              {current.name}
              <span className="agent-window-label">{t('/ 任务演示')}</span>
            </span>
            <span className="agent-demo-label">{t('模拟界面')}</span>
          </div>
          <Tabs
            label={t('任务演示阶段')}
            className="agent-stage-tabs"
            options={[
              { value: 'connect', label: t('1 接入电脑') },
              { value: 'run', label: t('2 执行任务') },
              { value: 'result', label: t('3 查看结果') },
            ]}
            value={stage}
            onChange={setStage}
          >
            <div className="agent-workspace">
              <div className="agent-conversation">
                <div className="agent-chat-label">
                  <span className="agent-avatar">{current.badge}</span>
                  <strong>{current.name}</strong>
                  <span>{t('云端对话')}</span>
                </div>
                <div className="agent-user-message">
                  {stage === 'connect'
                    ? platform === 'spark'
                      ? t('通过 ReadyRig MCP 连接我的电脑，查看可用工具和项目。')
                      : t('请通过 ReadyRig 连接我的电脑。\n[粘贴从 App 复制的完整 Prompt]')
                    : current.task}
                </div>
                <div className="agent-response">
                  <span className="agent-avatar small">{current.badge}</span>
                  <div>
                    {stage === 'connect'
                      ? t('接入后，我就能查看你开放的工具和项目，等待你安排任务。')
                      : stage === 'run'
                        ? t('我会通过 ReadyRig 在你的电脑上执行，结果保存回项目目录。')
                        : current.reply}
                  </div>
                </div>
                {connected && (
                  <div className="agent-tool-calls">
                    <span>{t('通过 ReadyRig 调用本机工具')}</span>
                    {current.actions.map((action, index) => (
                      <div key={action}>
                        <Icon name={stage === 'result' || index === 0 ? 'check' : 'activity'} width="14" height="14" />
                        <span>{action}</span>
                        <small>{stage === 'result' || index === 0 ? t('完成') : index === 1 ? t('执行中') : t('待执行')}</small>
                      </div>
                    ))}
                  </div>
                )}
                <div className="agent-composer">
                  <span>{t('继续安排下一件事…')}</span>
                  <Icon name="chevron" width="16" height="16" />
                </div>
              </div>
              <aside className="agent-local">
                <div className="agent-local-heading">
                  <AppIcon width="28" height="28" />
                  <div>
                    <strong>{t('你的 Mac')}</strong>
                    <span>{t('ReadyRig · 本机工作空间')}</span>
                  </div>
                </div>
                {stage === 'connect' ? (
                  <div className="agent-connect-card">
                    <Icon name="link" width="23" height="23" />
                    <h4>{current.method}</h4>
                    <p>{current.setup}</p>
                    <div className="agent-connect-value">
                      {platform === 'spark' ? (
                        <>
                          <span>{t('服务器名称')}</span>
                          <strong>ReadyRig</strong>
                          <span>{t('远程 MCP 地址')}</span>
                          <code>{t('〈从 App 复制的公网 MCP 地址〉')}</code>
                        </>
                      ) : (
                        <>
                          <span>{t('ReadyRig → 设置 → 远程')}</span>
                          <strong>{t('复制 Prompt → 粘贴到对话')}</strong>
                        </>
                      )}
                    </div>
                  </div>
                ) : (
                  <>
                    <div className="agent-local-status">
                      <span className="agent-status-dot" />
                      {stage === 'result' ? t('任务完成 · 文件已保存') : t('已连接 · 正在本机执行')}
                    </div>
                    <div className="agent-file-path">
                      <Icon name="folder" width="15" height="15" />
                      <span>
                        {current.folder}/ {current.file}
                      </span>
                    </div>
                    <div className="agent-result-file">
                      {stage === 'result' ? (
                        <>
                          <h4>{current.result[0]}</h4>
                          <p>{current.result[1]}</p>
                          <div>
                            {current.result.slice(2).map((line) => (
                              <p key={line}>{line}</p>
                            ))}
                          </div>
                          <span className="agent-file-saved">
                            <Icon name="check" width="13" height="13" />
                            {t('已写入本机')}
                          </span>
                        </>
                      ) : (
                        <>
                          <h4>{t('正在处理任务')}</h4>
                          <p>{current.actions[1]}</p>
                          <div className="agent-file-skeleton" aria-hidden="true">
                            <i />
                            <i />
                            <i />
                          </div>
                          <span className="agent-file-pending">{t('完成后，结果会保存在这里')}</span>
                        </>
                      )}
                    </div>
                  </>
                )}
              </aside>
            </div>
          </Tabs>
          <div className="agent-demo-footer">
            <span>{t('交互演示 · 示例数据，不会连接或操作你的电脑')}</span>
            <button type="button" onClick={() => setStage(stage === 'result' ? 'connect' : stage === 'connect' ? 'run' : 'result')}>
              {stage === 'result' ? t('从接入开始看') : t('下一步')}
              <Icon name="chevron" width="12" height="12" />
            </button>
          </div>
        </div>
      </Tabs>
    </div>
  )
}
