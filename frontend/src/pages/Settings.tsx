import type { ChangeEvent, KeyboardEvent } from 'react'
import { useCallback, useState } from 'react'
import { api } from '../api'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import { useDataLoader } from '../hooks/useDataLoader'
import { useToast } from '../hooks/useToast'
import type { APIKeyRow, HealthResponse, SystemSettings } from '../types'
import { getErrorMessage } from '../utils/error'
import { formatRelativeTime } from '../utils/time'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
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

function maskKey(key: string): string {
  if (!key || key.length < 12) return key
  return key.slice(0, 7) + '???????' + key.slice(-4)
}

export default function Settings() {
  const [newKeyName, setNewKeyName] = useState('')
  const [newKeyValue, setNewKeyValue] = useState('')
  const [createdKey, setCreatedKey] = useState<string | null>(null)
  const [settingsForm, setSettingsForm] = useState<SystemSettings>({ max_concurrency: 2, global_rpm: 0, test_model: '', test_concurrency: 50 })
  const [savingSettings, setSavingSettings] = useState(false)
  const [modelList, setModelList] = useState<string[]>([])
  const { toast, showToast } = useToast()

  const loadSettingsData = useCallback(async () => {
    const [health, keysResponse, settings, modelsResp] = await Promise.all([api.getHealth(), api.getAPIKeys(), api.getSettings(), api.getModels()])
    setSettingsForm(settings)
    setModelList(modelsResp.models ?? [])
    return {
      health,
      keys: keysResponse.keys ?? [],
    }
  }, [])

  const { data, loading, error, reload } = useDataLoader<{
    health: HealthResponse | null
    keys: APIKeyRow[]
  }>({
    initialData: {
      health: null,
      keys: [],
    },
    load: loadSettingsData,
  })

  const handleCreateKey = async () => {
    try {
      const result = await api.createAPIKey(newKeyName.trim() || 'default', newKeyValue.trim() || undefined)
      setCreatedKey(result.key)
      setNewKeyName('')
      setNewKeyValue('')
      showToast('密钥创建成功')
      void reload()
    } catch (error) {
      showToast(`创建失败: ${getErrorMessage(error)}`, 'error')
    }
  }

  const handleDeleteKey = async (id: number) => {
    if (!confirm('确定删除此密钥？使用该密钥的客户端将无法访问。')) {
      return
    }

    try {
      await api.deleteAPIKey(id)
      showToast('密钥已删除')
      void reload()
    } catch (error) {
      showToast(`删除失败: ${getErrorMessage(error)}`, 'error')
    }
  }

  const handleCopy = (text: string) => {
    void navigator.clipboard.writeText(text)
    showToast('已复制到剪贴板')
  }

  const handleSaveSettings = async () => {
    setSavingSettings(true)
    try {
      const updated = await api.updateSettings(settingsForm)
      setSettingsForm(updated)
      showToast('设置已保存，实时生效')
    } catch (error) {
      showToast(`保存失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setSavingSettings(false)
    }
  }

  const { health, keys } = data
  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => void reload()}
      loadingTitle="正在加载系统设置"
      loadingDescription="密钥列表和系统状态正在同步。"
      errorTitle="设置页加载失败"
    >
      <>
        <PageHeader
          title="系统设置"
          description="密钥管理与系统状态"
        />

        {/* API Keys */}
        <Card className="mb-4">
          <CardContent className="p-6">
            <div className="flex items-center justify-between gap-4 mb-4">
              <h3 className="text-base font-semibold text-foreground">API 密钥</h3>
            </div>

            <div className="flex gap-2 mb-4 flex-wrap">
              <Input
                className="flex-[1_1_120px]"
                placeholder="密钥名称（可选）"
                value={newKeyName}
                onChange={(event: ChangeEvent<HTMLInputElement>) => setNewKeyName(event.target.value)}
              />
              <Input
                className="flex-[2_1_240px]"
                placeholder="自定义密钥（留空则自动生成）"
                value={newKeyValue}
                onChange={(event: ChangeEvent<HTMLInputElement>) => setNewKeyValue(event.target.value)}
                onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => {
                  if (event.key === 'Enter') {
                    void handleCreateKey()
                  }
                }}
              />
              <Button onClick={() => void handleCreateKey()} className="whitespace-nowrap">
                创建密钥
              </Button>
            </div>

            {createdKey ? (
              <div className="p-3 mb-4 rounded-xl bg-[hsl(var(--success-bg))] border border-[hsl(var(--success))]/20 text-sm">
                <div className="font-semibold mb-1 text-[hsl(var(--success))]">新密钥已生成（仅显示一次）</div>
                <div className="flex items-center gap-2">
                  <code className="flex-1 font-mono text-[13px] break-all">{createdKey}</code>
                  <Button variant="outline" size="sm" onClick={() => handleCopy(createdKey)}>复制</Button>
                </div>
              </div>
            ) : null}

            <StateShell
              variant="section"
              isEmpty={keys.length === 0}
              emptyTitle="暂无 API 密钥"
              emptyDescription="未设置密钥时接口无需鉴权，生成后会显示在这里。"
            >
              <div className="overflow-auto border border-border rounded-xl">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="text-[13px] font-semibold">名称</TableHead>
                      <TableHead className="text-[13px] font-semibold">密钥</TableHead>
                      <TableHead className="text-[13px] font-semibold">创建时间</TableHead>
                      <TableHead className="text-[13px] font-semibold">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {keys.map((keyRow) => (
                      <TableRow key={keyRow.id}>
                        <TableCell className="text-[14px] font-medium">{keyRow.name}</TableCell>
                        <TableCell>
                          <span className="font-mono text-[13px]">{maskKey(keyRow.key)}</span>
                        </TableCell>
                        <TableCell className="text-[14px] text-muted-foreground">
                          {formatRelativeTime(keyRow.created_at, { variant: 'compact' })}
                        </TableCell>
                        <TableCell>
                          <Button variant="destructive" size="sm" onClick={() => void handleDeleteKey(keyRow.id)}>
                            删除
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </StateShell>

            <div className="text-xs text-muted-foreground mt-3">
              未设置密钥时 API 无需鉴权。添加第一个密钥后，所有 /v1/* 请求需携带 Authorization: Bearer sk-xxx
            </div>
          </CardContent>
        </Card>

        {/* System Status */}
        <Card className="mb-4">
          <CardContent className="p-6">
            <h3 className="text-base font-semibold text-foreground mb-4">系统状态</h3>
            <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-3.5">
              <div className="flex flex-col gap-1.5 p-3.5 rounded-2xl bg-white/40 border border-border">
                <label className="text-xs font-bold text-muted-foreground">服务</label>
                <div className="text-[15px] font-semibold">
                  <Badge variant={health?.status === 'ok' ? 'default' : 'destructive'} className="gap-1.5">
                    <span className={`size-1.5 rounded-full ${health?.status === 'ok' ? 'bg-emerald-500' : 'bg-red-400'}`} />
                    {health?.status === 'ok' ? '运行中' : '异常'}
                  </Badge>
                </div>
              </div>
              <div className="flex flex-col gap-1.5 p-3.5 rounded-2xl bg-white/40 border border-border">
                <label className="text-xs font-bold text-muted-foreground">账号</label>
                <div className="text-[15px] font-semibold">{health?.available ?? 0} / {health?.total ?? 0}</div>
              </div>
              <div className="flex flex-col gap-1.5 p-3.5 rounded-2xl bg-white/40 border border-border">
                <label className="text-xs font-bold text-muted-foreground">PostgreSQL</label>
                <div className="text-[15px] font-semibold">
                  <Badge variant="default" className="gap-1.5">
                    <span className="size-1.5 rounded-full bg-emerald-500" />
                    已连接
                  </Badge>
                </div>
              </div>
              <div className="flex flex-col gap-1.5 p-3.5 rounded-2xl bg-white/40 border border-border">
                <label className="text-xs font-bold text-muted-foreground">Redis</label>
                <div className="text-[15px] font-semibold">
                  <Badge variant="default" className="gap-1.5">
                    <span className="size-1.5 rounded-full bg-emerald-500" />
                    已连接
                  </Badge>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Protection Settings */}
        <Card className="mb-4">
          <CardContent className="p-6">
            <h3 className="text-base font-semibold text-foreground mb-4">流量保护</h3>
            <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4 mb-4">
              <div>
                <label className="block mb-2 text-sm font-semibold text-muted-foreground">每账号最大并发</label>
                <Input
                  type="number"
                  min={1}
                  max={50}
                  value={settingsForm.max_concurrency}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setSettingsForm(f => ({ ...f, max_concurrency: parseInt(e.target.value) || 1 }))}
                />
                <p className="text-xs text-muted-foreground mt-1">每个账号同时处理的最大请求数（范围 1~50）</p>
              </div>
              <div>
                <label className="block mb-2 text-sm font-semibold text-muted-foreground">全局 RPM 限制</label>
                <Input
                  type="number"
                  min={0}
                  value={settingsForm.global_rpm}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setSettingsForm(f => ({ ...f, global_rpm: parseInt(e.target.value) || 0 }))}
                />
                <p className="text-xs text-muted-foreground mt-1">每分钟最大请求数，0 = 不限制</p>
              </div>
              <div>
                <label className="block mb-2 text-sm font-semibold text-muted-foreground">测试连接模型</label>
                <select
                  className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  value={settingsForm.test_model}
                  onChange={(e: ChangeEvent<HTMLSelectElement>) => setSettingsForm(f => ({ ...f, test_model: e.target.value }))}
                >
                  {modelList.map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </select>
                <p className="text-xs text-muted-foreground mt-1">账号测试连接时使用的模型</p>
              </div>
              <div>
                <label className="block mb-2 text-sm font-semibold text-muted-foreground">测试并发数</label>
                <Input
                  type="number"
                  min={1}
                  max={200}
                  value={settingsForm.test_concurrency}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setSettingsForm(f => ({ ...f, test_concurrency: parseInt(e.target.value) || 1 }))}
                />
                <p className="text-xs text-muted-foreground mt-1">批量测试连接时的并发数（范围 1~200）</p>
              </div>
            </div>
            <Button onClick={() => void handleSaveSettings()} disabled={savingSettings}>
              {savingSettings ? '保存中...' : '保存设置'}
            </Button>
          </CardContent>
        </Card>

        {/* API Endpoints */}
        <Card>
          <CardContent className="p-6">
            <h3 className="text-base font-semibold text-foreground mb-4">API 端点</h3>
            <div className="overflow-auto border border-border rounded-xl">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="text-[13px] font-semibold">方法</TableHead>
                    <TableHead className="text-[13px] font-semibold">路径</TableHead>
                    <TableHead className="text-[13px] font-semibold">说明</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow>
                    <TableCell><Badge variant="default" className="text-[13px]">POST</Badge></TableCell>
                    <TableCell className="font-mono text-[14px]">/v1/chat/completions</TableCell>
                    <TableCell className="text-[14px] text-muted-foreground">OpenAI 兼容</TableCell>
                  </TableRow>
                  <TableRow>
                    <TableCell><Badge variant="outline" className="text-[13px]">POST</Badge></TableCell>
                    <TableCell className="font-mono text-[14px]">/v1/responses</TableCell>
                    <TableCell className="text-[14px] text-muted-foreground">Responses API</TableCell>
                  </TableRow>
                  <TableRow>
                    <TableCell><Badge variant="secondary" className="text-[13px]">GET</Badge></TableCell>
                    <TableCell className="font-mono text-[14px]">/v1/models</TableCell>
                    <TableCell className="text-[14px] text-muted-foreground">模型列表</TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>

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
