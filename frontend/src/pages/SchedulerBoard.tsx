import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { api } from '../api'
import PageHeader from '../components/PageHeader'
import Pagination from '../components/Pagination'
import StateShell from '../components/StateShell'
import { useDataLoader } from '../hooks/useDataLoader'
import StatusBadge from '../components/StatusBadge'
import type { AccountRow, OpsOverviewResponse } from '../types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Select } from '@/components/ui/select'

export default function SchedulerBoard() {
  const [tierFilter, setTierFilter] = useState('all')
  const [sortBy, setSortBy] = useState('risk')
  const [page, setPage] = useState(1)
  const PAGE_SIZE = 12

  const loadSchedulerData = useCallback(async () => {
    const [overview, accountsResponse] = await Promise.all([
      api.getOpsOverview(),
      api.getAccounts(),
    ])

    return {
      overview,
      accounts: accountsResponse.accounts ?? [],
    }
  }, [])

  const { data, loading, error, reload, reloadSilently } = useDataLoader<{
    overview: OpsOverviewResponse | null
    accounts: AccountRow[]
  }>({
    initialData: {
      overview: null,
      accounts: [],
    },
    load: loadSchedulerData,
  })

  useEffect(() => {
    const timer = window.setInterval(() => {
      void reloadSilently()
    }, 15000)

    return () => window.clearInterval(timer)
  }, [reloadSilently])

  const overview = data.overview
  const accounts = data.accounts
  const updatedLabel = overview?.updated_at ? formatTimeLabel(overview.updated_at) : '--:--:--'
  const schedulerCounts = useMemo(() => ({
    healthy: accounts.filter((account) => account.health_tier === 'healthy').length,
    warm: accounts.filter((account) => account.health_tier === 'warm').length,
    risky: accounts.filter((account) => account.health_tier === 'risky').length,
    banned: accounts.filter((account) => account.health_tier === 'banned' || account.status === 'unauthorized').length,
  }), [accounts])
  const recentIssueCounts = useMemo(() => {
    const now = Date.now()
    const withinWindow = (iso?: string, minutes = 60) => {
      if (!iso) return false
      const ts = new Date(iso).getTime()
      if (Number.isNaN(ts)) return false
      return now - ts <= minutes * 60 * 1000
    }

    return {
      unauthorized24h: accounts.filter((account) => withinWindow(account.last_unauthorized_at, 24 * 60)).length,
      rateLimited1h: accounts.filter((account) => withinWindow(account.last_rate_limited_at, 60)).length,
      timeout15m: accounts.filter((account) => withinWindow(account.last_timeout_at, 15)).length,
    }
  }, [accounts])
  const spotlightAccounts = useMemo(() => {
    const priority = (account: AccountRow) => {
      if (account.health_tier === 'banned' || account.status === 'unauthorized') return 3
      if (account.health_tier === 'risky') return 2
      if (account.health_tier === 'warm') return 1
      return 0
    }

    return [...accounts]
      .filter((account) => {
        if (tierFilter === 'everything') return true
        if (tierFilter === 'all') {
          return priority(account) > 0
        }
        if (tierFilter === 'healthy') {
          return account.health_tier === 'healthy'
        }
        if (tierFilter === 'banned') {
          return account.health_tier === 'banned' || account.status === 'unauthorized'
        }
        return account.health_tier === tierFilter
      })
      .sort((left, right) => {
        switch (sortBy) {
          case 'score_asc':
            return (left.scheduler_score ?? 0) - (right.scheduler_score ?? 0)
          case 'usage_desc':
            return (right.usage_percent_7d ?? -1) - (left.usage_percent_7d ?? -1)
          case 'latency_penalty':
            return (right.scheduler_breakdown?.latency_penalty ?? 0) - (left.scheduler_breakdown?.latency_penalty ?? 0)
          case 'unauthorized':
            return Number(Boolean(right.last_unauthorized_at)) - Number(Boolean(left.last_unauthorized_at))
          case 'risk':
          default: {
            const priorityDiff = priority(right) - priority(left)
            if (priorityDiff !== 0) return priorityDiff
            return (left.scheduler_score ?? 0) - (right.scheduler_score ?? 0)
          }
        }
      })
  }, [accounts, tierFilter, sortBy])

  const totalPages = Math.max(1, Math.ceil(spotlightAccounts.length / PAGE_SIZE))
  const pagedAccounts = spotlightAccounts.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)

  // 筛选/排序变更时重置页码
  useEffect(() => {
    setPage(1)
  }, [tierFilter, sortBy])

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => void reload()}
      loadingTitle="正在加载调度面板"
      loadingDescription="账号健康分层和调度数据正在同步。"
      errorTitle="调度面板加载失败"
    >
      <>
        <PageHeader
          title="调度面板"
          description="独立查看账号健康分层、风险信号和调度排序结果。"
          actions={
            <div className="flex items-center gap-3 max-sm:w-full max-sm:flex-col max-sm:items-stretch">
              <Button variant="outline" asChild>
                <Link to="/ops">
                  <ArrowLeft className="size-3.5" />
                  系统运维
                </Link>
              </Button>
              <span className="text-sm text-muted-foreground max-sm:text-center">最后更新时间：{updatedLabel}</span>
              <Button variant="outline" onClick={() => void reload()}>
                <RefreshCw className="size-3.5" />
                刷新
              </Button>
            </div>
          }
        />

        {overview ? (
          <>
            <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4 mb-6">
              <SummaryPill label="账号总量" value={formatNumber(accounts.length)} />
              <SummaryPill label="可调度账号" value={`${overview.runtime.available_accounts} / ${overview.runtime.total_accounts}`} />
              <SummaryPill label="Healthy + Warm" value={formatNumber(schedulerCounts.healthy + schedulerCounts.warm)} />
              <SummaryPill label="高风险账号" value={formatNumber(schedulerCounts.risky + schedulerCounts.banned)} />
            </div>

            <Card className="mb-6">
              <CardContent className="p-6">
                <div className="flex items-center justify-between gap-4 max-sm:flex-col max-sm:items-start">
                  <div>
                    <h3 className="text-base font-semibold text-foreground">调度全局视图</h3>
                    <p className="mt-1 text-sm text-muted-foreground">从系统维度查看当前号池健康分层和高风险账号分布。</p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <SchedulerPill label="Healthy" value={schedulerCounts.healthy} tone="success" />
                    <SchedulerPill label="Warm" value={schedulerCounts.warm} tone="warning" />
                    <SchedulerPill label="Risky" value={schedulerCounts.risky} tone="danger" />
                    <SchedulerPill label="Banned" value={schedulerCounts.banned} tone="neutral" />
                  </div>
                </div>

                <div className="mt-4 grid gap-3 md:grid-cols-3">
                  <MiniOpsCard
                    label="最近 401"
                    value={formatNumber(recentIssueCounts.unauthorized24h)}
                    sub="24 小时内被判定 unauthorized 的账号数"
                    tone="danger"
                  />
                  <MiniOpsCard
                    label="最近 429"
                    value={formatNumber(recentIssueCounts.rateLimited1h)}
                    sub="1 小时内触发限流的账号数"
                    tone="warning"
                  />
                  <MiniOpsCard
                    label="最近 Timeout"
                    value={formatNumber(recentIssueCounts.timeout15m)}
                    sub="15 分钟内出现超时的账号数"
                    tone="neutral"
                  />
                </div>

                <div className="mt-5 flex flex-wrap items-center gap-3 rounded-2xl border border-border bg-white/45 px-4 py-3">
                  <span className="text-[12px] font-semibold text-muted-foreground">筛选</span>
                  <div className="w-[180px]">
                    <Select
                      value={tierFilter}
                      onValueChange={setTierFilter}
                      options={[
                        { label: '全部风险账号', value: 'all' },
                        { label: '全部账号', value: 'everything' },
                        { label: 'Healthy', value: 'healthy' },
                        { label: 'Warm', value: 'warm' },
                        { label: 'Risky', value: 'risky' },
                        { label: 'Banned', value: 'banned' },
                      ]}
                    />
                  </div>
                  <span className="text-[12px] font-semibold text-muted-foreground">排序</span>
                  <div className="w-[200px]">
                    <Select
                      value={sortBy}
                      onValueChange={setSortBy}
                      options={[
                        { label: '风险优先', value: 'risk' },
                        { label: '分数升序', value: 'score_asc' },
                        { label: '7d 用量高优先', value: 'usage_desc' },
                        { label: '延迟惩罚高优先', value: 'latency_penalty' },
                        { label: '401 近期优先', value: 'unauthorized' },
                      ]}
                    />
                  </div>
                </div>

                {spotlightAccounts.length > 0 ? (
                  <>
                    <div className="mt-5 grid gap-3 md:grid-cols-2">
                    {pagedAccounts.map((account) => (
                      <div key={account.id} className="rounded-2xl border border-border bg-white/50 px-4 py-3">
                        <div className="flex items-start justify-between gap-3">
                          <div className="min-w-0">
                            <div className="truncate text-[14px] font-semibold text-foreground">
                              {account.email || `ID ${account.id}`}
                            </div>
                            <div className="mt-1 text-[12px] text-muted-foreground">
                              分 {Math.round(account.scheduler_score ?? 0)} · 并发 {account.dynamic_concurrency_limit ?? '-'} · 套餐 {account.plan_type || '-'}
                            </div>
                          </div>
                          <StatusBadge status={account.status} />
                        </div>
                        <div className="mt-3 flex flex-wrap items-center gap-2">
                          <Badge variant="outline" className={getHealthTierClassName(account.health_tier)}>
                            {formatHealthTier(account.health_tier)}
                          </Badge>
                          {account.usage_percent_7d !== null && account.usage_percent_7d !== undefined ? (
                            <Badge variant="outline" className="text-[12px]">
                              7d {account.usage_percent_7d.toFixed(1)}%
                            </Badge>
                          ) : null}
                        </div>
                        <div className="mt-3 flex flex-wrap items-center gap-2">
                          {buildScoreReasonTags(account).map((tag) => (
                            <Badge key={tag.label} variant="outline" className={tag.className}>
                              {tag.label}
                            </Badge>
                          ))}
                        </div>
                      </div>
                    ))}
                    </div>
                    {totalPages > 1 && (
                      <div className="mt-4">
                        <Pagination
                          page={page}
                          totalPages={totalPages}
                          onPageChange={setPage}
                          totalItems={spotlightAccounts.length}
                          pageSize={PAGE_SIZE}
                        />
                      </div>
                    )}
                  </>
                ) : (
                  <div className="mt-5 rounded-2xl border border-border bg-white/40 px-4 py-4 text-sm text-muted-foreground">
                    当前没有需要重点关注的风险账号，号池整体处于稳定状态。
                  </div>
                )}
              </CardContent>
            </Card>
          </>
        ) : null}
      </>
    </StateShell>
  )
}

function SummaryPill({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-2xl border border-border bg-white/65 px-4 py-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.7)]">
      <div className="text-[12px] font-bold tracking-[0.14em] uppercase text-muted-foreground">{label}</div>
      <div className="mt-2 text-[20px] font-bold tracking-tight text-foreground">{value}</div>
    </div>
  )
}

function SchedulerPill({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: 'neutral' | 'success' | 'warning' | 'danger'
}) {
  const toneStyle = {
    neutral: 'bg-slate-500/10 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300',
    success: 'bg-emerald-500/10 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300',
    warning: 'bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300',
    danger: 'bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300',
  }[tone]

  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-semibold ${toneStyle}`}>
      <span>{label}</span>
      <span>{value}</span>
    </span>
  )
}

function MiniOpsCard({
  label,
  value,
  sub,
  tone,
}: {
  label: string
  value: string
  sub: string
  tone: 'neutral' | 'warning' | 'danger'
}) {
  const toneStyle = {
    neutral: 'bg-slate-500/10 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300',
    warning: 'bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300',
    danger: 'bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300',
  }[tone]

  return (
    <div className="rounded-2xl border border-border bg-white/45 px-4 py-4">
      <div className="text-[12px] font-semibold text-muted-foreground">{label}</div>
      <div className="mt-2 text-[28px] font-bold leading-none tracking-tight text-foreground">{value}</div>
      <div className={`mt-3 inline-flex rounded-full px-2.5 py-1 text-[11px] font-semibold ${toneStyle}`}>
        {sub}
      </div>
    </div>
  )
}

function getHealthTierClassName(healthTier?: string) {
  switch (healthTier) {
    case 'healthy':
      return 'border-transparent bg-emerald-500/10 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300'
    case 'warm':
      return 'border-transparent bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300'
    case 'risky':
      return 'border-transparent bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300'
    case 'banned':
      return 'border-transparent bg-slate-500/10 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300'
    default:
      return 'border-border text-muted-foreground'
  }
}

function formatHealthTier(healthTier?: string) {
  switch (healthTier) {
    case 'healthy':
      return '健康'
    case 'warm':
      return '预热'
    case 'risky':
      return '风险'
    case 'banned':
      return '隔离'
    default:
      return '未知'
  }
}

function buildScoreReasonTags(account: AccountRow) {
  const breakdown = account.scheduler_breakdown
  if (!breakdown) {
    return []
  }

  const tags: Array<{ label: string; className: string }> = []

  if (breakdown.unauthorized_penalty > 0) {
    tags.push({ label: `401 -${Math.round(breakdown.unauthorized_penalty)}`, className: 'border-transparent bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300' })
  }
  if (breakdown.rate_limit_penalty > 0) {
    tags.push({ label: `429 -${Math.round(breakdown.rate_limit_penalty)}`, className: 'border-transparent bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300' })
  }
  if (breakdown.timeout_penalty > 0) {
    tags.push({ label: `超时 -${Math.round(breakdown.timeout_penalty)}`, className: 'border-transparent bg-orange-500/10 text-orange-600 dark:bg-orange-500/20 dark:text-orange-300' })
  }
  if (breakdown.server_penalty > 0) {
    tags.push({ label: `5xx -${Math.round(breakdown.server_penalty)}`, className: 'border-transparent bg-rose-500/10 text-rose-600 dark:bg-rose-500/20 dark:text-rose-300' })
  }
  if (breakdown.failure_penalty > 0) {
    tags.push({ label: `失败串 -${Math.round(breakdown.failure_penalty)}`, className: 'border-transparent bg-slate-500/10 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300' })
  }
  if (breakdown.usage_penalty_7d > 0) {
    tags.push({ label: `7d -${Math.round(breakdown.usage_penalty_7d)}`, className: 'border-transparent bg-fuchsia-500/10 text-fuchsia-600 dark:bg-fuchsia-500/20 dark:text-fuchsia-300' })
  }
  if (breakdown.latency_penalty > 0) {
    tags.push({ label: `延迟 -${Math.round(breakdown.latency_penalty)}`, className: 'border-transparent bg-cyan-500/10 text-cyan-700 dark:bg-cyan-500/20 dark:text-cyan-300' })
  }
  if (breakdown.success_bonus > 0) {
    tags.push({ label: `成功 +${Math.round(breakdown.success_bonus)}`, className: 'border-transparent bg-emerald-500/10 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300' })
  }

  return tags
}

function formatNumber(value: number): string {
  return value.toLocaleString()
}

function formatTimeLabel(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return '--:--:--'
  }
  return date.toLocaleTimeString('zh-CN', {
    hour12: false,
  })
}
