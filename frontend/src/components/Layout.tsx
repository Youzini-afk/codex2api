import type { PropsWithChildren, ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { LayoutDashboard, Users, Activity, Settings, Server, Workflow } from 'lucide-react'
import logoImg from '../assets/logo.png'

type NavItem = {
  to: string
  label: string
  icon: ReactNode
  end?: boolean
}

const navItems: NavItem[] = [
  { to: '/', label: '仪表盘', icon: <LayoutDashboard className="size-[18px]" />, end: true },
  { to: '/accounts', label: '账号管理', icon: <Users className="size-[18px]" /> },
  { to: '/ops', label: '系统运维', icon: <Server className="size-[18px]" />, end: true },
  { to: '/ops/scheduler', label: '调度面板', icon: <Workflow className="size-[18px]" />, end: true },
  { to: '/usage', label: '使用统计', icon: <Activity className="size-[18px]" /> },
  { to: '/settings', label: '系统设置', icon: <Settings className="size-[18px]" /> },
]

export default function Layout({ children }: PropsWithChildren) {
  return (
    <div className="min-h-dvh">
      <div className="grid grid-cols-[296px_minmax(0,1fr)] max-w-full max-lg:grid-cols-1 max-lg:px-4">
        {/* Sidebar - desktop */}
        <aside className="sticky top-0 self-start h-dvh border-r border-border bg-[hsl(var(--sidebar-background))] max-lg:hidden">
          <div className="flex flex-col h-full px-6 pt-8 pb-6">
            {/* Brand */}
            <div className="pb-6 border-b border-border">
              <div className="flex items-center gap-3.5">
                <img src={logoImg} alt="Codex2API" className="w-[52px] h-[52px] rounded-2xl object-cover shadow-[0_4px_16px_hsl(258_60%_63%/0.2)] shrink-0" />
                <div className="flex flex-col gap-1">
                  <h1 className="text-[26px] leading-tight font-bold bg-gradient-to-br from-[hsl(258,60%,63%)] to-[hsl(210,80%,60%)] bg-clip-text text-transparent">
                    Codex2API
                  </h1>
                  <span className="inline-flex items-center px-2 py-0.5 rounded-full bg-primary/10 text-primary text-[11px] font-bold w-fit">
                    v2.0
                  </span>
                </div>
              </div>
              <p className="mt-3 text-[13px] text-muted-foreground leading-relaxed">
                管理账号池与请求流量的控制台
              </p>
            </div>

            {/* Nav */}
            <nav className="flex-1 flex flex-col gap-2 pt-5" aria-label="主导航">
              <span className="text-[12px] font-bold tracking-[0.16em] uppercase text-primary/70 mb-1">
                控制台
              </span>
              {navItems.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) =>
                    `flex items-center gap-3 min-h-[50px] px-3.5 py-3 border rounded-2xl text-[20px] font-semibold transition-all duration-150 ${
                      isActive
                        ? 'bg-gradient-to-br from-primary/8 to-blue-500/6 border-primary/20 text-primary shadow-[inset_0_1px_0_rgba(255,255,255,0.8)]'
                        : 'border-transparent text-muted-foreground hover:-translate-y-px hover:bg-white/50 hover:border-border hover:text-foreground'
                    }`
                  }
                >
                  {item.icon}
                  <span>{item.label}</span>
                </NavLink>
              ))}
            </nav>

            {/* Footer */}
            <div className="mt-auto flex justify-start">
              <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-500/16 bg-[hsl(var(--success-bg))] px-3 py-1.5 text-[11px] font-bold text-[hsl(var(--success))] shadow-[inset_0_1px_0_rgba(255,255,255,0.55)]">
                <span className="size-2 rounded-full bg-emerald-500" />
                在线
              </span>
            </div>
          </div>
        </aside>

        {/* Main content */}
        <main className="min-w-0 p-6 max-lg:pb-[104px]">
          {/* Mobile topbar */}
          <header className="hidden max-lg:flex items-center justify-between gap-4 mb-4 p-3.5 border border-border rounded-[22px] bg-white/70">
            <div className="flex items-center gap-3">
              <img src={logoImg} alt="Codex2API" className="w-8 h-8 rounded-[10px] object-cover" />
              <strong className="text-lg">Codex2API</strong>
            </div>
            <span className="inline-flex items-center justify-center min-h-[28px] px-2.5 rounded-full text-[12px] font-bold bg-[hsl(var(--success-bg))] text-[hsl(var(--success))]">
              在线
            </span>
          </header>

          <div className="min-h-full">{children}</div>
        </main>

        {/* Mobile bottom nav */}
        <nav className="fixed left-4 right-4 bottom-4 z-40 hidden max-lg:grid grid-cols-6 gap-2 p-2 border border-border rounded-3xl bg-white/90 shadow-lg backdrop-blur-[20px]" aria-label="移动导航">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `flex flex-col items-center justify-center gap-1.5 min-h-[64px] p-2 border rounded-2xl text-center text-[11px] font-bold transition-all duration-150 ${
                  isActive
                    ? 'bg-white/80 border-primary/20 text-foreground'
                    : 'border-transparent text-muted-foreground'
                }`
              }
            >
              {item.icon}
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>
      </div>
    </div>
  )
}
