import type { ImgHTMLAttributes, SVGProps } from 'react'

const paths = {
  folder: <><path d="M3 7V5a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" /><path d="M3 7h18" /></>,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="3" /><path d="m7 9 3 3-3 3m6 0h4" /></>,
  monitor: <><rect x="3" y="3" width="18" height="13" rx="2" /><path d="M12 16v5m-5 0h10" /></>,
  browser: <><rect x="3" y="3" width="18" height="18" rx="3" /><path d="M3 8h18M7 5.5h.01m3 0h.01" /></>,
  shield: <><path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Z" /><path d="m8.5 12 2.5 2.5 4.5-5" /></>,
  activity: <><path d="M3 12h4l3-8 4 16 3-8h4" /></>,
  link: <><path d="m10 13 4-4m-7 6-1 1a3.5 3.5 0 0 0 5 5l4-4a3.5 3.5 0 0 0 0-5m2-3 1-1a3.5 3.5 0 0 0-5-5L9 7a3.5 3.5 0 0 0 0 5" /></>,
  download: <><path d="M12 3v12m-4-4 4 4 4-4M4 16v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4" /></>,
  copy: <><rect x="8" y="8" width="12" height="13" rx="2" /><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h3" /></>,
  check: <path d="m5 12 4 4L19 6" />,
  pencil: <><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16Z" /><path d="m13.5 6.5 4 4" /></>,
  chevron: <path d="m9 5 7 7-7 7" />,
  menu: <path d="M4 6h16M4 12h16M4 18h16" />,
  close: <path d="m6 6 12 12M6 18 18 6" />,
  sun: <><circle cx="12" cy="12" r="4" /><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5" /></>,
  moon: <path d="M20 14a8.5 8.5 0 0 1-10-10 8.5 8.5 0 1 0 10 10Z" />,
  pause: <><path d="M8 5v14m8-14v14" strokeWidth="3" /></>,
  play: <path d="m8 4 12 8-12 8Z" />,
  search: <><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></>,
  apple: <><path d="M16.8 12c0-2 1.6-3.2 1.6-3.2s-1-1.6-3-1.6c-1.4 0-2.3.8-3.4.8-1 0-1.8-.8-3.3-.8-2.5 0-4.5 2-4.5 5.2 0 3.8 2.8 8.6 4.9 8.6 1.1 0 1.7-.7 2.9-.7 1.1 0 1.7.7 2.9.7 1.9 0 3.7-3.5 4.3-5.1-1.5-.5-2.4-1.9-2.4-3.9ZM15.5 2c.1 2.5-1.7 4-3.4 4 .1-2.2 1.6-3.8 3.4-4Z" fill="currentColor" stroke="none" /></>,
}

export type IconName = keyof typeof paths

export function Icon({ name, ...props }: SVGProps<SVGSVGElement> & { name: IconName }) {
  return <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>{paths[name]}</svg>
}

export function AppIcon({ className = '', ...props }: ImgHTMLAttributes<HTMLImageElement>) {
  return <img src="/brand/app-icon.webp" alt="" width="96" height="96" className={`app-icon ${className}`} {...props} />
}

export function GitHubIcon({ width = 20, height = 20 }: { width?: number; height?: number }) {
  // Unmodified SVGs from https://brand.github.com/GitHub_Logos.zip.
  return <span className="github-mark" aria-hidden="true">
    <img className="github-mark-light" src="/brand/github-invertocat-black.svg" alt="" width={width} height={height} />
    <img className="github-mark-dark" src="/brand/github-invertocat-white.svg" alt="" width={width} height={height} />
  </span>
}
