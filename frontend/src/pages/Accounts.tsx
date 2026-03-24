import type { ChangeEvent } from 'react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import Modal from '../components/Modal'
import PageHeader from '../components/PageHeader'
import Pagination from '../components/Pagination'
import StateShell from '../components/StateShell'
import StatusBadge from '../components/StatusBadge'
import { useDataLoader } from '../hooks/useDataLoader'
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
import { Plus, RefreshCw, Trash2, Zap, FlaskConical, Ban, Timer } from 'lucide-react'

export default function Accounts() {
  const [showAdd, setShowAdd] = useState(false)
  const [page, setPage] = useState(1)
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
  const { toast, showToast } = useToast()

  const loadAccounts = useCallback(async () => {
    const data = await api.getAccounts()
    return data.accounts ?? []
  }, [])

  const { data: accounts, loading, error, reload, reloadSilently } = useDataLoader<AccountRow[]>({
    initialData: [],
    load: loadAccounts,
  })

  const totalPages = Math.max(1, Math.ceil(accounts.length / PAGE_SIZE))
  const pagedAccounts = accounts.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
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

  const handleDelete = async (account: AccountRow) => {
    if (!confirm(`确定删除账号 "${account.email || account.id}" 吗？`)) return
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
    if (!confirm(`确定删除选中的 ${selected.size} 个账号吗？`)) return
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
    if (!confirm('确定清理所有封禁（unauthorized）账号吗？')) return
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
    if (!confirm('确定清理所有限流（rate_limited）账号吗？')) return
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
            </div>
          )}
        />

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
                        <TableCell className="text-[14px] font-mono">{account.plan_type || '-'}</TableCell>
                        <TableCell><StatusBadge status={account.status} /></TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2 text-[13px]">
                            <span className="text-emerald-600 font-medium">{account.success_requests ?? 0}</span>
                            <span className="text-muted-foreground">/</span>
                            <span className="text-red-500 font-medium">{account.error_requests ?? 0}</span>
                          </div>
                        </TableCell>
                        <TableCell>
                          {(account.plan_type?.toLowerCase() === 'free' && (account.usage_percent_7d ?? 0) > 0) ? (
                            <div className="w-24">
                              <div className="flex items-center justify-between mb-1">
                                <span className="text-[12px] font-medium">{(account.usage_percent_7d ?? 0).toFixed(1)}%</span>
                              </div>
                              <div className="h-1.5 rounded-full bg-muted overflow-hidden">
                                <div
                                  className={`h-full rounded-full transition-all ${
                                    (account.usage_percent_7d ?? 0) >= 90 ? 'bg-red-500' :
                                    (account.usage_percent_7d ?? 0) >= 70 ? 'bg-amber-500' :
                                    'bg-emerald-500'
                                  }`}
                                  style={{ width: `${Math.min(100, account.usage_percent_7d ?? 0)}%` }}
                                />
                              </div>
                            </div>
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
            onClose={() => setTestingAccount(null)}
          />
        )}

        {toast ? (
          <div
            className={`fixed right-6 bottom-6 z-[2000] px-4 py-3 rounded-2xl text-white text-sm font-bold shadow-lg ${
              toast.type === 'error' ? 'bg-destructive' : 'bg-[hsl(var(--success))]'
            }`}
            style={{ animation: 'toast-slide-up 0.22s ease' }}
          >
            {toast.msg}
          </div>
        ) : null}
      </>
    </StateShell>
  )
}

// ==================== 测试连接弹窗 ====================

interface TestEvent {
  type: 'test_start' | 'content' | 'test_complete' | 'error'
  text?: string
  model?: string
  success?: boolean
  error?: string
}

function TestConnectionModal({ account, onClose }: { account: AccountRow; onClose: () => void }) {
  const [output, setOutput] = useState<string[]>([])
  const [status, setStatus] = useState<'connecting' | 'streaming' | 'success' | 'error'>('connecting')
  const [errorMsg, setErrorMsg] = useState('')
  const [model, setModel] = useState('')
  const abortRef = useRef<AbortController | null>(null)
  const outputEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const controller = new AbortController()
    abortRef.current = controller

    const run = async () => {
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
          return
        }

        const reader = res.body?.getReader()
        if (!reader) {
          setStatus('error')
          setErrorMsg('浏览器不支持流式读取')
          return
        }

        const decoder = new TextDecoder()
        let buffer = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

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
                  setStatus(event.success ? 'success' : 'error')
                  break
                case 'error':
                  setStatus('error')
                  setErrorMsg(event.error || '未知错误')
                  break
              }
            } catch { /* ignore non-JSON lines */ }
          }
        }
      } catch (err: unknown) {
        if (err instanceof DOMException && err.name === 'AbortError') return
        setStatus('error')
        setErrorMsg(err instanceof Error ? err.message : '连接失败')
      }
    }

    void run()

    return () => {
      controller.abort()
    }
  }, [account.id])

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
    >
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <span className={`text-sm font-semibold flex items-center gap-1.5 ${statusColor}`}>
            {statusLabel}
          </span>
          {model && <span className="text-xs font-mono px-2 py-0.5 rounded-md bg-muted text-muted-foreground">{model}</span>}
        </div>

        {(output.length > 0 || status === 'connecting' || status === 'streaming') && (
          <div className="min-h-[80px] max-h-[240px] overflow-auto rounded-xl border border-border bg-muted/30 p-3 text-sm leading-relaxed font-mono whitespace-pre-wrap">
            {output.length === 0 && status === 'connecting' && (
              <span className="text-muted-foreground animate-pulse">正在发送测试请求...</span>
            )}
            {output.join('')}
            <div ref={outputEndRef} />
          </div>
        )}

        {errorMsg && (
          <div className="rounded-xl border border-red-200 bg-red-50 dark:border-red-900/50 dark:bg-red-950/30 p-3.5 text-sm text-red-600 dark:text-red-400 break-words leading-relaxed">
            {errorMsg}
          </div>
        )}
      </div>
    </Modal>
  )
}
