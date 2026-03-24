import type { ChangeEvent } from 'react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import Modal from '../components/Modal'
import PageHeader from '../components/PageHeader'
import Pagination from '../components/Pagination'
import StateShell from '../components/StateShell'
import StatusBadge from '../components/StatusBadge'
import ToastNotice from '../components/ToastNotice'
import { useDataLoader } from '../hooks/useDataLoader'
import { useConfirmDialog } from '../hooks/useConfirmDialog'
import { useToast } from '../hooks/useToast'
import type { AccountRow, AddAccountRequest } from '../types'
import { getErrorMessage } from '../utils/error'
import { formatRelativeTime } from '../utils/time'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Plus, RefreshCw, Trash2, Zap, FlaskConical, Ban, Timer, Upload } from 'lucide-react'

export default function Accounts() {
  const [showAdd, setShowAdd] = useState(false)
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<'all' | 'normal' | 'rate_limited' | 'banned'>('all')
  const PAGE_SIZE = 20
  const [addForm, setAddForm] = useState<AddAccountRequest>({
    refresh_token: '',
    proxy_url: '',
  })
  const [submitting, setSubmitting] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [refreshingIds, setRefreshingIds] = useState<Set<number>>(new Set())
  const [batchLoading, setBatchLoading] = useState(false)
  const [batchTesting, setBatchTesting] = useState(false)
  const [cleaningBanned, setCleaningBanned] = useState(false)
  const [cleaningRateLimited, setCleaningRateLimited] = useState(false)
  const [testingAccount, setTestingAccount] = useState<AccountRow | null>(null)
  const [importing, setImporting] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const { toast, showToast } = useToast()
  const { confirm, confirmDialog } = useConfirmDialog()

  const loadAccounts = useCallback(async () => {
    const data = await api.getAccounts()
    return data.accounts ?? []
  }, [])

  const { data: accounts, loading, error, reload, reloadSilently } = useDataLoader<AccountRow[]>({
    initialData: [],
    load: loadAccounts,
  })
  const usageBootstrapReloadedRef = useRef(false)

  useEffect(() => {
    const hasMissingUsage = accounts.some(
      (account) => account.plan_type?.toLowerCase() === 'free' && (account.usage_percent_7d === null || account.usage_percent_7d === undefined)
    )
    if (!hasMissingUsage || usageBootstrapReloadedRef.current) {
      return
    }

    usageBootstrapReloadedRef.current = true
    const timer = window.setTimeout(() => {
      void reloadSilently()
    }, 4000)

    return () => window.clearTimeout(timer)
  }, [accounts, reloadSilently])

  const totalAccounts = accounts.length
  const normalAccounts = accounts.filter((account) => account.status === 'active' || account.status === 'ready').length
  const rateLimitedAccounts = accounts.filter((account) => account.status === 'rate_limited').length
  const bannedAccounts = accounts.filter((account) => account.status === 'unauthorized').length
  const healthyAccounts = accounts.filter((account) => account.health_tier === 'healthy').length
  const warmAccounts = accounts.filter((account) => account.health_tier === 'warm').length
  const riskyAccounts = accounts.filter((account) => account.health_tier === 'risky').length

  const filteredAccounts = accounts.filter((account) => {
    switch (statusFilter) {
      case 'normal':
        return account.status === 'active' || account.status === 'ready'
      case 'rate_limited':
        return account.status === 'rate_limited'
      case 'banned':
        return account.status === 'unauthorized'
      default:
        return true
    }
  })

  const totalPages = Math.max(1, Math.ceil(filteredAccounts.length / PAGE_SIZE))
  const pagedAccounts = filteredAccounts.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
  const allPageSelected = pagedAccounts.length > 0 && pagedAccounts.every((a) => selected.has(a.id))

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const toggleSelectAll = () => {
    if (allPageSelected) {
      setSelected((prev) => {
        const next = new Set(prev)
        for (const a of pagedAccounts) next.delete(a.id)
        return next
      })
    } else {
      setSelected((prev) => {
        const next = new Set(prev)
        for (const a of pagedAccounts) next.add(a.id)
        return next
      })
    }
  }

  const handleAdd = async () => {
    if (!addForm.refresh_token.trim()) return
    setSubmitting(true)
    try {
      const result = await api.addAccount(addForm)
      showToast(result.message || '账号添加成功')
      setShowAdd(false)
      setAddForm({ refresh_token: '', proxy_url: '' })
      void reload()
    } catch (error) {
      showToast(`添加失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setSubmitting(false)
    }
  }

  const handleFileImport = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return
    if (!file.name.endsWith('.txt')) {
      showToast('请选择 .txt 文件', 'error')
      return
    }
    setImporting(true)
    try {
      const formData = new FormData()
      formData.append('file', file)
      const res = await fetch('/api/admin/accounts/import', { method: 'POST', body: formData })
      const data = await res.json()
      if (!res.ok) {
        showToast(data.error || '导入失败', 'error')
      } else {
        showToast(data.message || '导入完成')
        void reload()
      }
    } catch (error) {
      showToast(`导入失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setImporting(false)
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  const handleDelete = async (account: AccountRow) => {
    const confirmed = await confirm({
      title: '删除账号',
      description: (
        <>
          账号 <span className="font-semibold text-foreground">{account.email || `ID ${account.id}`}</span> 将从当前账号池中移除。
          该操作会立即生效，且通常不再恢复。
        </>
      ),
      confirmText: '确认删除',
      tone: 'destructive',
      confirmVariant: 'destructive',
    })
    if (!confirmed) return
    try {
      await api.deleteAccount(account.id)
      showToast('已删除')
      void reload()
    } catch (error) {
      showToast(`删除失败: ${getErrorMessage(error)}`, 'error')
    }
  }

  const handleRefresh = async (account: AccountRow) => {
    setRefreshingIds((prev) => new Set(prev).add(account.id))
    try {
      await api.refreshAccount(account.id)
      showToast('刷新请求已发送')
      void reloadSilently()
    } catch (error) {
      showToast(`刷新失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setRefreshingIds((prev) => {
        const next = new Set(prev)
        next.delete(account.id)
        return next
      })
    }
  }

  const handleBatchDelete = async () => {
    if (selected.size === 0) return
    const confirmed = await confirm({
      title: '批量删除账号',
      description: `选中的 ${selected.size} 个账号会一起从账号池中移除。请确认当前选择无误。`,
      confirmText: '确认删除',
      tone: 'destructive',
      confirmVariant: 'destructive',
    })
    if (!confirmed) return
    setBatchLoading(true)
    let success = 0
    let fail = 0
    for (const id of selected) {
      try {
        await api.deleteAccount(id)
        success++
      } catch {
        fail++
      }
    }
    showToast(`批量删除完成：成功 ${success}，失败 ${fail}`)
    setSelected(new Set())
    setBatchLoading(false)
    void reload()
  }

  const handleBatchRefresh = async () => {
    if (selected.size === 0) return
    setBatchLoading(true)
    let success = 0
    let fail = 0
    for (const id of selected) {
      try {
        await api.refreshAccount(id)
        success++
      } catch {
        fail++
      }
    }
    showToast(`批量刷新完成：成功 ${success}，失败 ${fail}`)
    setBatchLoading(false)
    void reload()
  }

  const handleBatchTest = async () => {
    setBatchTesting(true)
    try {
      const result = await api.batchTestAccounts()
      showToast(`批量测试完成：成功 ${result.success} / 封禁 ${result.banned} / 限流 ${result.rate_limited} / 失败 ${result.failed}`)
      void reload()
    } catch (error) {
      showToast(`批量测试失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setBatchTesting(false)
    }
  }

  const handleCleanBanned = async () => {
    const confirmed = await confirm({
      title: '清理封禁账号',
      description: '会清除所有 unauthorized 状态标记，让这些账号重新参与调度与测试。',
      confirmText: '确认清理',
      tone: 'warning',
    })
    if (!confirmed) return
    setCleaningBanned(true)
    try {
      const result = await api.cleanBanned()
      showToast(result.message)
      void reload()
    } catch (error) {
      showToast(`清理失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setCleaningBanned(false)
    }
  }

  const handleCleanRateLimited = async () => {
    const confirmed = await confirm({
      title: '清理限流账号',
      description: '会清除所有 rate_limited 状态标记，让这些账号重新回到可调度状态。',
      confirmText: '确认清理',
      tone: 'warning',
    })
    if (!confirmed) return
    setCleaningRateLimited(true)
    try {
      const result = await api.cleanRateLimited()
      showToast(result.message)
      void reload()
    } catch (error) {
      showToast(`清理失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setCleaningRateLimited(false)
    }
  }

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => void reload()}
      loadingTitle="正在加载账号列表"
      loadingDescription="账号池和实时状态正在同步。"
      errorTitle="账号页加载失败"
    >
      <>
        <PageHeader
          title="账号管理"
          description="管理 Codex 反代账号（Refresh Token）"
          onRefresh={() => void reload()}
          actions={(
            <div className="flex items-center gap-1.5">
              <Button variant="outline" size="sm" disabled={batchTesting} onClick={() => void handleBatchTest()}>
                <FlaskConical className="size-3" />
                {batchTesting ? '测试中...' : '批量测试'}
              </Button>
              <Button variant="outline" size="sm" disabled={cleaningBanned} onClick={() => void handleCleanBanned()}>
                <Ban className="size-3" />
                {cleaningBanned ? '清理中...' : '清理封禁'}
              </Button>
              <Button variant="outline" size="sm" disabled={cleaningRateLimited} onClick={() => void handleCleanRateLimited()}>
                <Timer className="size-3" />
                {cleaningRateLimited ? '清理中...' : '清理限流'}
              </Button>
              <Button onClick={() => setShowAdd(true)}>
                <Plus className="size-3.5" />
                添加账号
              </Button>
              <Button variant="outline" disabled={importing} onClick={() => fileInputRef.current?.click()}>
                <Upload className="size-3.5" />
                {importing ? '导入中...' : '导入文件'}
              </Button>
              <input
                ref={fileInputRef}
                type="file"
                accept=".txt"
                className="hidden"
                onChange={(e) => void handleFileImport(e)}
              />
            </div>
          )}
        />

        <div className="mb-4 grid grid-cols-2 gap-3 xl:grid-cols-4">
          <CompactStat label="总账号数量" value={totalAccounts} tone="neutral" />
          <CompactStat label="正常账号" value={normalAccounts} tone="success" />
          <CompactStat label="限流账号" value={rateLimitedAccounts} tone="warning" />
          <CompactStat label="封禁账号" value={bannedAccounts} tone="danger" />
        </div>

        <div className="mb-4 flex flex-wrap items-center gap-2 rounded-2xl border border-border bg-white/55 px-4 py-3 text-[12px] text-muted-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.72)]">
          <span className="font-semibold text-foreground">筛选</span>
          {([['all', '全部'], ['normal', '正常'], ['rate_limited', '限流'], ['banned', '封禁']] as const).map(([key, label]) => (
            <button
              key={key}
              onClick={() => { setStatusFilter(key); setPage(1) }}
              className={`rounded-full px-3 py-1 font-semibold transition-colors ${
                statusFilter === key
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted/50 text-muted-foreground hover:bg-muted'
              }`}
            >
              {label} {key === 'all' ? totalAccounts : key === 'normal' ? normalAccounts : key === 'rate_limited' ? rateLimitedAccounts : bannedAccounts}
            </button>
          ))}  
        </div>

        <div className="mb-4 flex flex-wrap items-center gap-2 rounded-2xl border border-border bg-white/55 px-4 py-3 text-[12px] text-muted-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.72)]">
          <span className="font-semibold text-foreground">调度视图</span>
          <SchedulerChip label="Healthy" value={healthyAccounts} tone="success" />
          <SchedulerChip label="Warm" value={warmAccounts} tone="warning" />
          <SchedulerChip label="Risky" value={riskyAccounts} tone="danger" />
          <SchedulerChip label="Banned" value={bannedAccounts} tone="neutral" />
        </div>

        {selected.size > 0 && (
          <div className="flex items-center justify-between gap-3 px-4 py-2.5 mb-4 rounded-2xl bg-primary/10 border border-primary/20 text-sm font-semibold text-primary">
            <span>已选 {selected.size} 项</span>
            <div className="flex items-center gap-1.5">
              <Button variant="outline" size="sm" disabled={batchLoading} onClick={() => void handleBatchRefresh()}>
                批量刷新
              </Button>
              <Button variant="destructive" size="sm" disabled={batchLoading} onClick={() => void handleBatchDelete()}>
                批量删除
              </Button>
              <Button variant="outline" size="sm" onClick={() => setSelected(new Set())}>
                取消选择
              </Button>
            </div>
          </div>
        )}

        <Card>
          <CardContent className="p-6">
            <StateShell
              variant="section"
              isEmpty={accounts.length === 0}
              emptyTitle="还没有账号"
              emptyDescription="导入 Refresh Token 后，账号会立即加入号池并显示在这里。"
              action={<Button onClick={() => setShowAdd(true)}>添加账号</Button>}
            >
              <div className="overflow-auto border border-border rounded-xl">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10">
                        <input
                          type="checkbox"
                          className="size-4 cursor-pointer accent-[hsl(var(--primary))]"
                          checked={allPageSelected}
                          onChange={toggleSelectAll}
                        />
                      </TableHead>
                      <TableHead className="text-[13px] font-semibold">ID</TableHead>
                      <TableHead className="text-[13px] font-semibold">邮箱</TableHead>
                      <TableHead className="text-[13px] font-semibold">套餐</TableHead>
                      <TableHead className="text-[13px] font-semibold">状态</TableHead>
                      <TableHead className="text-[13px] font-semibold">请求统计</TableHead>
                      <TableHead className="text-[13px] font-semibold">用量</TableHead>
                      <TableHead className="text-[13px] font-semibold">更新时间</TableHead>
                      <TableHead className="text-[13px] font-semibold text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {pagedAccounts.map((account) => (
                      <TableRow key={account.id} className={selected.has(account.id) ? 'bg-primary/5' : ''}>
                        <TableCell>
                          <input
                            type="checkbox"
                            className="size-4 cursor-pointer accent-[hsl(var(--primary))]"
                            checked={selected.has(account.id)}
                            onChange={() => toggleSelect(account.id)}
                          />
                        </TableCell>
                        <TableCell className="text-[14px] font-mono text-muted-foreground">{account.id}</TableCell>
                        <TableCell className="text-[14px] text-muted-foreground">{account.email || '-'}</TableCell>
                        <TableCell
                          className="text-[18px] font-medium"
                          style={{ fontFamily: 'var(--font-geist-mono)' }}
                        >
                          {account.plan_type || '-'}
                        </TableCell>
                        <TableCell>
                          <div className="space-y-1">
                            <StatusBadge status={account.status} />
                            <div className="text-[11px] text-muted-foreground">
                              {formatHealthTier(account.health_tier)} · 分 {Math.round(account.scheduler_score ?? 0)} · 并发 {account.dynamic_concurrency_limit ?? '-'}
                            </div>
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2 text-[13px]">
                            <span className="text-emerald-600 font-medium">{account.success_requests ?? 0}</span>
                            <span className="text-muted-foreground">/</span>
                            <span className="text-red-500 font-medium">{account.error_requests ?? 0}</span>
                          </div>
                        </TableCell>
                        <TableCell>
                          {account.plan_type?.toLowerCase() === 'free' ? (
                            account.usage_percent_7d !== null && account.usage_percent_7d !== undefined ? (
                            <div className="w-24">
                              <div className="flex items-center justify-between mb-1">
                                <span className="text-[12px] font-medium">{account.usage_percent_7d.toFixed(1)}%</span>
                              </div>
                              <div className="h-1.5 rounded-full bg-muted overflow-hidden">
                                <div
                                  className={`h-full rounded-full transition-all ${
                                    account.usage_percent_7d >= 90 ? 'bg-red-500' :
                                    account.usage_percent_7d >= 70 ? 'bg-amber-500' :
                                    'bg-emerald-500'
                                  }`}
                                  style={{ width: `${Math.min(100, account.usage_percent_7d)}%` }}
                                />
                              </div>
                            </div>
                            ) : (
                              <span className="text-[12px] font-medium text-muted-foreground">未采集</span>
                            )
                          ) : (
                            <span className="text-[13px] text-muted-foreground">-</span>
                          )}
                        </TableCell>
                        <TableCell className="text-[14px] text-muted-foreground">{formatRelativeTime(account.updated_at)}</TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center gap-1.5 justify-end">
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => setTestingAccount(account)}
                              title="测试连接"
                            >
                              <Zap className="size-3" />
                              测试
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={refreshingIds.has(account.id)}
                              onClick={() => void handleRefresh(account)}
                              title="刷新 AT"
                            >
                              <RefreshCw className={`size-3 ${refreshingIds.has(account.id) ? 'animate-spin' : ''}`} />
                              {refreshingIds.has(account.id) ? '刷新中' : '刷新'}
                            </Button>
                            <Button variant="destructive" size="sm" onClick={() => void handleDelete(account)}>
                              <Trash2 className="size-3" />
                              删除
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <Pagination
                page={page}
                totalPages={totalPages}
                onPageChange={setPage}
                totalItems={accounts.length}
                pageSize={PAGE_SIZE}
              />
            </StateShell>
          </CardContent>
        </Card>

        <Modal
          show={showAdd}
          title="添加账号"
          onClose={() => setShowAdd(false)}
          footer={(
            <>
              <Button variant="outline" onClick={() => setShowAdd(false)}>取消</Button>
              <Button onClick={() => void handleAdd()} disabled={submitting || !addForm.refresh_token.trim()}>
                {submitting ? '添加中...' : '添加'}
              </Button>
            </>
          )}
        >
          <div className="space-y-4">
            <div>
              <label className="block mb-2 text-sm font-semibold text-muted-foreground">Refresh Token *</label>
              <textarea
                className="w-full min-h-[96px] p-3 border border-input rounded-xl bg-background text-sm resize-y focus:outline-none focus:ring-2 focus:ring-ring"
                placeholder="每行一个 Refresh Token，支持批量粘贴"
                value={addForm.refresh_token}
                onChange={(event: ChangeEvent<HTMLTextAreaElement>) =>
                  setAddForm((form) => ({ ...form, refresh_token: event.target.value }))
                }
                rows={3}
              />
            </div>
            <div>
              <label className="block mb-2 text-sm font-semibold text-muted-foreground">代理地址（可选）</label>
              <Input
                placeholder="例如：http://127.0.0.1:7890"
                value={addForm.proxy_url}
                onChange={(event: ChangeEvent<HTMLInputElement>) =>
                  setAddForm((form) => ({ ...form, proxy_url: event.target.value }))
                }
              />
            </div>
          </div>
        </Modal>

        {testingAccount && (
          <TestConnectionModal
            account={testingAccount}
            onSettled={() => {
              void reloadSilently()
            }}
            onClose={() => setTestingAccount(null)}
          />
        )}

        {confirmDialog}

        <ToastNotice toast={toast} />
      </>
    </StateShell>
  )
}

function CompactStat({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: 'neutral' | 'success' | 'warning' | 'danger'
}) {
  const toneStyle = {
    neutral: {
      chip: 'bg-slate-500/10 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300',
      dot: 'bg-slate-500',
    },
    success: {
      chip: 'bg-emerald-500/10 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300',
      dot: 'bg-emerald-500',
    },
    warning: {
      chip: 'bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300',
      dot: 'bg-amber-500',
    },
    danger: {
      chip: 'bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300',
      dot: 'bg-red-500',
    },
  }[tone]

  return (
    <div className="flex items-center justify-between rounded-2xl border border-border bg-white/65 px-4 py-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.7)]">
      <div className="min-w-0">
        <div className="text-[12px] font-semibold text-muted-foreground">{label}</div>
        <div className="mt-1 text-[24px] font-bold leading-none tracking-tight text-foreground">{value}</div>
      </div>
      <div className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-semibold ${toneStyle.chip}`}>
        <span className={`size-2 rounded-full ${toneStyle.dot}`} />
        {label.replace('账号', '')}
      </div>
    </div>
  )
}

function SchedulerChip({
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
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-semibold ${toneStyle}`}>
      <span>{label}</span>
      <span>{value}</span>
    </span>
  )
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

// ==================== 测试连接弹窗 ====================

interface TestEvent {
  type: 'test_start' | 'content' | 'test_complete' | 'error'
  text?: string
  model?: string
  success?: boolean
  error?: string
}

function formatTestErrorMessage(message: string) {
  const normalized = message.trim()
  const jsonStart = normalized.indexOf('{')

  if (jsonStart === -1) {
    return normalized
  }

  const prefix = normalized.slice(0, jsonStart).trim().replace(/[：:]\s*$/, '')
  const jsonText = normalized.slice(jsonStart)

  try {
    const parsed = JSON.parse(jsonText)
    const prettyJson = JSON.stringify(parsed, null, 2)
    return prefix ? `${prefix}\n${prettyJson}` : prettyJson
  } catch {
    return normalized
  }
}

function formatTestOutput(text: string) {
  try {
    const parsed = JSON.parse(text);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return text;
  }
}

function TestConnectionModal({
  account,
  onClose,
  onSettled,
}: {
  account: AccountRow
  onClose: () => void
  onSettled: () => void
}) {
  const [output, setOutput] = useState<string[]>([])
  const [status, setStatus] = useState<'connecting' | 'streaming' | 'success' | 'error'>('connecting')
  const [errorMsg, setErrorMsg] = useState('')
  const [model, setModel] = useState('')
  const abortRef = useRef<AbortController | null>(null)
  const outputEndRef = useRef<HTMLDivElement>(null)
  const settledRef = useRef(false)
  const onSettledRef = useRef(onSettled)
  onSettledRef.current = onSettled

  const markSettled = useCallback(() => {
    if (settledRef.current) return
    settledRef.current = true
    onSettledRef.current()
  }, [])

  useEffect(() => {
    // 重置状态（StrictMode 二次 mount 时清理上一次的残留）
    setOutput([])
    setStatus('connecting')
    setErrorMsg('')
    settledRef.current = false

    const controller = new AbortController()
    abortRef.current = controller

    const run = async () => {
      if (controller.signal.aborted) return

      try {
        const res = await fetch(`/api/admin/accounts/${account.id}/test`, {
          signal: controller.signal,
        })

        if (!res.ok) {
          const body = await res.text()
          let msg = `HTTP ${res.status}`
          try {
            const parsed = JSON.parse(body)
            if (parsed.error) msg = parsed.error
          } catch { /* ignore */ }
          setStatus('error')
          setErrorMsg(msg)
          markSettled()
          return
        }

        const reader = res.body?.getReader()
        if (!reader) {
          setStatus('error')
          setErrorMsg('浏览器不支持流式读取')
          markSettled()
          return
        }

        const decoder = new TextDecoder()
        let buffer = ''
        let receivedTerminalEvent = false

        const processEventLines = (lines: string[]) => {
          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed.startsWith('data: ')) continue

            try {
              const event: TestEvent = JSON.parse(trimmed.slice(6))

              switch (event.type) {
                case 'test_start':
                  setModel(event.model || '')
                  setStatus('streaming')
                  break
                case 'content':
                  if (event.text) {
                    setOutput((prev) => [...prev, event.text!])
                  }
                  break
                case 'test_complete':
                  receivedTerminalEvent = true
                  setStatus(event.success ? 'success' : 'error')
                  markSettled()
                  break
                case 'error':
                  receivedTerminalEvent = true
                  setStatus('error')
                  setErrorMsg(event.error || '未知错误')
                  markSettled()
                  break
              }
            } catch { /* ignore non-JSON lines */ }
          }
        }

        while (true) {
          const { done, value } = await reader.read()
          if (done) {
            buffer += decoder.decode()
            break
          }

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''
          processEventLines(lines)
        }

        if (buffer.trim()) {
          processEventLines([buffer])
        }

        if (!receivedTerminalEvent) {
          setStatus('error')
          setErrorMsg('连接已结束，但未收到完整的结束事件')
          markSettled()
        }
      } catch (err: unknown) {
        if (err instanceof DOMException && err.name === 'AbortError') return
        setStatus('error')
        setErrorMsg(err instanceof Error ? err.message : '连接失败')
        markSettled()
      }
    }

    // 延迟 50ms 启动，确保 StrictMode cleanup 有足够时间执行 abort
    const timer = window.setTimeout(() => {
      void run()
    }, 50)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [account.id, markSettled])

  useEffect(() => {
    outputEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [output])

  const statusLabel = {
    connecting: '⏳ 连接中...',
    streaming: '🔄 接收响应...',
    success: '✅ 测试成功',
    error: '❌ 测试失败',
  }[status]

  const statusColor = {
    connecting: 'text-muted-foreground',
    streaming: 'text-blue-500',
    success: 'text-emerald-500',
    error: 'text-red-500',
  }[status]
  const formattedErrorMsg = errorMsg ? formatTestErrorMessage(errorMsg) : ''

  return (
    <Modal
      show={true}
      title={`测试连接 - ${account.email || `ID ${account.id}`}`}
      onClose={() => {
        abortRef.current?.abort()
        onClose()
      }}
      footer={
        <Button
          variant="outline"
          onClick={() => {
            abortRef.current?.abort()
            onClose()
          }}
        >
          关闭
        </Button>
      }
      contentClassName="sm:max-w-[680px]"
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <span className={`flex items-center gap-1.5 text-sm font-semibold ${statusColor}`}>
            {statusLabel}
          </span>
          {model && (
            <span className="max-w-full rounded-md bg-muted px-2 py-0.5 font-mono text-xs break-all text-muted-foreground">
              {model}
            </span>
          )}
        </div>

        {(output.length > 0 || status === 'connecting' || status === 'streaming') && (
          <div
            className="min-h-[80px] max-h-[240px] overflow-auto rounded-xl border border-border bg-muted/30 p-3 text-[20px] leading-[1.8] whitespace-pre-wrap break-all"
            style={{ fontFamily: 'var(--font-geist-mono)' }}
          >
            {output.length === 0 && status === 'connecting' && (
              <span className="text-muted-foreground animate-pulse">正在发送测试请求...</span>
            )}
            {output.join('')}
            <div ref={outputEndRef} />
          </div>
        )}

        {errorMsg && (
          <div className="max-h-[40vh] overflow-auto rounded-xl border border-red-200 bg-red-50 p-3.5 text-red-600 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-400">
            <div className="mb-2 text-sm font-semibold">失败详情</div>
            <pre
              className="text-[20px] leading-[1.8] whitespace-pre-wrap break-all"
              style={{ fontFamily: 'var(--font-geist-mono)' }}
            >
              {formattedErrorMsg}
            </pre>
          </div>
        )}
      </div>
    </Modal>
  )
}
