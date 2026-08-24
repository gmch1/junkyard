import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Badge,
  Box,
  Button,
  Container,
  Group,
  Loader,
  NativeSelect,
  Paper,
  Progress,
  ScrollArea,
  SimpleGrid,
  Stack,
  Table,
  Text,
  ThemeIcon,
  Title,
} from '@mantine/core'
import { useDocumentTitle } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import {
  IconActivity,
  IconAlertCircle,
  IconBrandSpeedtest,
  IconClock,
  IconCpu,
  IconDatabase,
  IconRouteAltLeft,
  IconTrophy,
} from '@tabler/icons-react'
import './App.css'

type ClientMetrics = {
  requests: number
  successes: number
  failures: number
  average_latency_ms: number
  p95_latency_ms: number
  last_latency_ms: number
}

export type ModelMetrics = {
  id: string
  enabled: boolean
  role: string
  routing_priority: number
  rpm: number
  tpm: number
  min_interval_seconds: number
  in_flight: number
  requests_last_minute: number
  successes: number
  failures: number
  throttles: number
  average_latency_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  last_latency_ms: number
  input_tokens: number
  output_tokens: number
  attempts: number
  adoptions: number
  adoption_rate: number
  hedge_participations: number
  hedge_wins: number
  hedge_win_rate: number
  discarded_responses: number
  cooldown_seconds: number
  cooldown_reason: string
  unavailable: boolean
  unavailable_reason: string
}

type DashboardData = {
  status: string
  generated_at: string
  uptime_seconds: number
  base_url: string
  model_alias: string
  metrics_persistence: {
    enabled: boolean
    flush_interval_seconds: number
    last_flushed_at: number
  }
  client: ClientMetrics
  process: { rss_mb: number; cpu_percent: number }
  totals: {
    model_successes: number
    model_failures: number
    upstream_attempts: number
    adoptions: number
    adoption_rate: number
    hedged_requests: number
    hedge_wins: number
    hedge_win_rate: number
    discarded_responses: number
    in_flight: number
    requests_last_minute: number
    input_tokens: number
    output_tokens: number
    throttles: number
  }
  models: ModelMetrics[]
}

type MetricCardProps = {
  title: string
  value: string
  detail: string
  icon: typeof IconActivity
  color: string
}

type ModelSortField =
  | 'model'
  | 'status'
  | 'rpm'
  | 'adoption_rate'
  | 'adoptions'
  | 'attempts'
  | 'hedge_win_rate'
  | 'hedge_wins'
  | 'discarded_responses'
  | 'successes'
  | 'failures'
  | 'throttles'
  | 'average_latency_ms'
  | 'p95_latency_ms'
  | 'last_latency_ms'
  | 'requests_last_minute'
  | 'input_tokens'
  | 'output_tokens'
  | 'total_tokens'
type SortDirection = 'asc' | 'desc'
type ModelSort = { field: ModelSortField; direction: SortDirection }
type ModelStatus = 'unavailable' | 'cooldown' | 'disabled' | 'processing' | 'available'

const MODEL_SORT_STORAGE_KEY = 'aliyun-proxy:model-sort:v2'
const MODEL_SORT_OPTIONS: { value: ModelSortField; label: string }[] = [
  { value: 'model', label: '模型名称' },
  { value: 'status', label: '状态' },
  { value: 'rpm', label: 'RPM' },
  { value: 'adoption_rate', label: '采纳率' },
  { value: 'adoptions', label: '采纳数' },
  { value: 'attempts', label: '参与数' },
  { value: 'hedge_win_rate', label: '竞速胜率' },
  { value: 'hedge_wins', label: '竞速胜出数' },
  { value: 'discarded_responses', label: '丢弃数' },
  { value: 'successes', label: '成功数' },
  { value: 'failures', label: '失败数' },
  { value: 'throttles', label: '限流数' },
  { value: 'average_latency_ms', label: '平均延迟' },
  { value: 'p95_latency_ms', label: 'P95 延迟' },
  { value: 'last_latency_ms', label: '最近延迟' },
  { value: 'requests_last_minute', label: '近 1 分钟请求数' },
  { value: 'input_tokens', label: '输入 Token' },
  { value: 'output_tokens', label: '输出 Token' },
  { value: 'total_tokens', label: '总 Token' },
]
const MODEL_SORT_SELECT_OPTIONS = [
  { value: '', label: '选择排序指标' },
  ...MODEL_SORT_OPTIONS,
]
const MODEL_SORT_FIELDS = new Set(MODEL_SORT_OPTIONS.map((option) => option.value))
const DEFAULT_SORT_DIRECTIONS: Record<ModelSortField, SortDirection> = {
  model: 'asc',
  status: 'asc',
  rpm: 'desc',
  adoption_rate: 'desc',
  adoptions: 'desc',
  attempts: 'desc',
  hedge_win_rate: 'desc',
  hedge_wins: 'desc',
  discarded_responses: 'desc',
  successes: 'desc',
  failures: 'desc',
  throttles: 'desc',
  average_latency_ms: 'asc',
  p95_latency_ms: 'asc',
  last_latency_ms: 'asc',
  requests_last_minute: 'desc',
  input_tokens: 'desc',
  output_tokens: 'desc',
  total_tokens: 'desc',
}
const MODEL_STATUS_RANKS: Record<ModelStatus, number> = {
  unavailable: 0,
  cooldown: 1,
  disabled: 2,
  processing: 3,
  available: 4,
}

function loadModelSort(): ModelSort | null {
  try {
    const saved = window.localStorage.getItem(MODEL_SORT_STORAGE_KEY)
    if (!saved) return null
    const parsed = JSON.parse(saved) as { field?: unknown; direction?: unknown }
    if (
      typeof parsed.field !== 'string'
      || !MODEL_SORT_FIELDS.has(parsed.field as ModelSortField)
      || (parsed.direction !== 'asc' && parsed.direction !== 'desc')
    ) return null
    return { field: parsed.field as ModelSortField, direction: parsed.direction }
  } catch {
    return null
  }
}

function modelStatus(model: ModelMetrics): ModelStatus {
  if (!model.enabled) return 'disabled'
  if (model.unavailable) return 'unavailable'
  if (model.cooldown_seconds > 0) return 'cooldown'
  if (model.in_flight > 0) return 'processing'
  return 'available'
}

function modelSortValue(model: ModelMetrics, field: ModelSortField): string | number {
  if (field === 'model') return model.id
  if (field === 'status') return MODEL_STATUS_RANKS[modelStatus(model)]
  if (field === 'total_tokens') return model.input_tokens + model.output_tokens
  return model[field]
}

function hasModelSortValue(model: ModelMetrics, field: ModelSortField) {
  if (field === 'adoption_rate') return model.attempts > 0
  if (field === 'hedge_win_rate' || field === 'hedge_wins') {
    return model.hedge_participations > 0
  }
  if (
    field === 'average_latency_ms'
    || field === 'p95_latency_ms'
    || field === 'last_latency_ms'
  ) return model.successes > 0
  return true
}

function sortModels(models: ModelMetrics[], sort: ModelSort | null) {
  if (!sort) return models
  const multiplier = sort.direction === 'asc' ? 1 : -1
  return models.toSorted((left, right) => {
    const leftHasValue = hasModelSortValue(left, sort.field)
    const rightHasValue = hasModelSortValue(right, sort.field)
    if (leftHasValue !== rightHasValue) return leftHasValue ? -1 : 1
    const leftValue = modelSortValue(left, sort.field)
    const rightValue = modelSortValue(right, sort.field)
    const comparison = typeof leftValue === 'string'
      ? leftValue.localeCompare(String(rightValue), 'en')
      : leftValue - Number(rightValue)
    return comparison * multiplier
  })
}

function formatInteger(value: number) {
  return new Intl.NumberFormat('zh-CN').format(value)
}

function formatLatency(value: number) {
  if (!value) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value)} ms`
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days) return `${days} 天 ${hours} 小时`
  if (hours) return `${hours} 小时 ${minutes} 分钟`
  return `${minutes} 分钟`
}

function modelState(model: ModelMetrics) {
  if (!model.enabled) return { label: '已禁用', color: 'gray' }
  if (model.unavailable) return { label: '不可用', color: 'red' }
  if (model.cooldown_seconds > 0) return { label: `冷却 ${Math.ceil(model.cooldown_seconds)}s`, color: 'orange' }
  if (model.in_flight > 0) return { label: `处理中 ${model.in_flight}`, color: 'blue' }
  return { label: '可用', color: 'teal' }
}

function MetricCard({ title, value, detail, icon: Icon, color }: MetricCardProps) {
  return (
    <Paper className="metric-card" radius="lg" p="lg" withBorder>
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Stack gap={5}>
          <Text c="dimmed" size="sm" fw={600}>{title}</Text>
          <Text className="metric-value" fw={750}>{value}</Text>
          <Text c="dimmed" size="xs">{detail}</Text>
        </Stack>
        <ThemeIcon color={color} variant="light" radius="md" size={42}>
          <Icon size={22} stroke={1.8} />
        </ThemeIcon>
      </Group>
    </Paper>
  )
}

type ModelsTableProps = {
  models: ModelMetrics[]
  pendingModel: string
  onToggle: (modelId: string, enabled: boolean) => void
}

function ModelsTable({ models, pendingModel, onToggle }: ModelsTableProps) {
  return (
    <ScrollArea>
      <Table className="models-table" verticalSpacing="sm" horizontalSpacing="md" miw={1550}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>模型</Table.Th>
            <Table.Th>状态</Table.Th>
            <Table.Th>采纳率</Table.Th>
            <Table.Th ta="right">采纳 / 参与</Table.Th>
            <Table.Th ta="right">竞速胜出</Table.Th>
            <Table.Th ta="right">丢弃</Table.Th>
            <Table.Th ta="right">成功 / 失败</Table.Th>
            <Table.Th ta="right">限流</Table.Th>
            <Table.Th ta="right">平均 / P95 / 最近</Table.Th>
            <Table.Th ta="right">近 1 分钟</Table.Th>
            <Table.Th ta="right">输入 / 输出 Token</Table.Th>
            <Table.Th ta="right" className="models-action-cell">操作</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {models.map((model) => {
            const state = modelState(model)
            const reason = model.unavailable_reason || model.cooldown_reason
            const nextEnabled = !model.enabled || model.unavailable
            const actionLabel = model.enabled && !model.unavailable ? '禁用' : '启用'
            return (
              <Table.Tr key={model.id}>
                <Table.Td>
                  <Stack gap={2}>
                    <Text fw={650} size="sm">{model.id}</Text>
                    <Text c="dimmed" size="xs">{model.role || '通用'} · {model.rpm} RPM</Text>
                  </Stack>
                </Table.Td>
                <Table.Td>
                  <Badge color={state.color} variant="light" title={reason}>{state.label}</Badge>
                </Table.Td>
                <Table.Td>
                  <Group gap="xs" wrap="nowrap">
                    <Progress value={model.adoption_rate} color="teal" size="sm" w={82} radius="xl" />
                    <Text size="xs" c="dimmed" w={42}>{model.adoption_rate.toFixed(1)}%</Text>
                  </Group>
                </Table.Td>
                <Table.Td ta="right">{model.adoptions} / {model.attempts}</Table.Td>
                <Table.Td ta="right">
                  {model.hedge_participations
                    ? `${model.hedge_wins} / ${model.hedge_participations} (${model.hedge_win_rate.toFixed(1)}%)`
                    : '—'}
                </Table.Td>
                <Table.Td ta="right">{model.discarded_responses}</Table.Td>
                <Table.Td ta="right">
                  <Text size="sm"><Text span c="teal" fw={650}>{model.successes}</Text> / <Text span c="red">{model.failures}</Text></Text>
                </Table.Td>
                <Table.Td ta="right">{model.throttles}</Table.Td>
                <Table.Td ta="right">
                  <Text size="sm">{formatLatency(model.average_latency_ms)} / {formatLatency(model.p95_latency_ms)} / {formatLatency(model.last_latency_ms)}</Text>
                </Table.Td>
                <Table.Td ta="right">{model.requests_last_minute}</Table.Td>
                <Table.Td ta="right">{formatInteger(model.input_tokens)} / {formatInteger(model.output_tokens)}</Table.Td>
                <Table.Td ta="right" className="models-action-cell">
                  <Button
                    type="button"
                    size="xs"
                    variant="light"
                    color={nextEnabled ? 'teal' : 'red'}
                    loading={pendingModel === model.id}
                    disabled={Boolean(pendingModel) && pendingModel !== model.id}
                    onClick={() => onToggle(model.id, nextEnabled)}
                  >
                    {actionLabel}
                  </Button>
                </Table.Td>
              </Table.Tr>
            )
          })}
        </Table.Tbody>
      </Table>
    </ScrollArea>
  )
}

function App() {
  useDocumentTitle('阿里云模型代理 · 运行统计')
  const [data, setData] = useState<DashboardData | null>(null)
  const [error, setError] = useState('')
  const [pendingModel, setPendingModel] = useState('')
  const [modelSort, setModelSort] = useState<ModelSort | null>(loadModelSort)

  useEffect(() => {
    let active = true
    let timer: number | undefined
    let controller: AbortController | undefined

    const refresh = async () => {
      controller = new AbortController()
      try {
        const response = await fetch('/v1/proxy/dashboard-data', {
          cache: 'no-store',
          signal: controller.signal,
        })
        if (!response.ok) throw new Error(`HTTP ${response.status}`)
        const next = await response.json() as DashboardData
        if (active) {
          setData(next)
          setError('')
        }
      } catch (caught) {
        if (active && !(caught instanceof DOMException && caught.name === 'AbortError')) {
          setError(caught instanceof Error ? caught.message : '未知错误')
        }
      } finally {
        if (active) timer = window.setTimeout(refresh, 2000)
      }
    }

    void refresh()
    return () => {
      active = false
      controller?.abort()
      if (timer) window.clearTimeout(timer)
    }
  }, [])

  const successRate = data?.client.requests
    ? (data.client.successes / data.client.requests) * 100
    : 0
  const sortedModels = useMemo(
    () => sortModels(data?.models ?? [], modelSort),
    [data?.models, modelSort],
  )

  const persistModelSort = (next: ModelSort | null) => {
    setModelSort(next)
    try {
      if (next) window.localStorage.setItem(MODEL_SORT_STORAGE_KEY, JSON.stringify(next))
      else window.localStorage.removeItem(MODEL_SORT_STORAGE_KEY)
    } catch {
      // Keep the session preference even when browser storage is unavailable.
    }
  }

  const updateModelSortField = (value: string | null) => {
    if (!value || !MODEL_SORT_FIELDS.has(value as ModelSortField)) {
      persistModelSort(null)
      return
    }
    const field = value as ModelSortField
    persistModelSort({
      field,
      direction: modelSort?.field === field
        ? modelSort.direction
        : DEFAULT_SORT_DIRECTIONS[field],
    })
  }

  const toggleSortDirection = () => {
    if (!modelSort) return
    persistModelSort({
      ...modelSort,
      direction: modelSort.direction === 'asc' ? 'desc' : 'asc',
    })
  }

  const toggleModel = async (modelId: string, enabled: boolean) => {
    setPendingModel(modelId)
    try {
      const response = await fetch('/v1/proxy/models/enabled', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Proxy-Dashboard': '1',
        },
        body: JSON.stringify({ model: modelId, enabled }),
      })
      const payload = await response.json() as {
        dashboard?: DashboardData
        probed?: boolean
        error?: { message?: string; upstream_code?: string }
      }
      if (!response.ok || !payload.dashboard) {
        if (payload.dashboard) setData(payload.dashboard)
        const message = payload.error?.upstream_code?.includes('AllocationQuota')
          ? '模型额度仍未恢复，当前状态保持不变。'
          : payload.error?.message || `HTTP ${response.status}`
        throw new Error(message)
      }
      setData(payload.dashboard)
      notifications.show({
        title: enabled ? '模型已启用' : '模型已禁用',
        message: payload.probed
          ? `${modelId} 检测通过，已恢复调度。`
          : `${modelId} 已更新。`,
        color: enabled ? 'teal' : 'orange',
      })
    } catch (caught) {
      notifications.show({
        title: enabled ? '模型检测失败' : '模型状态更新失败',
        message: caught instanceof Error ? caught.message : '未知错误',
        color: 'red',
      })
    } finally {
      setPendingModel('')
    }
  }

  return (
    <Box className="app-shell">
      <Container size="xl" py={{ base: 24, sm: 42 }}>
        <Stack gap="xl">
          <Group justify="space-between" align="flex-end">
            <Stack gap={5}>
              <Group gap="sm">
                <ThemeIcon color="blue" variant="gradient" gradient={{ from: 'blue', to: 'violet' }} size={38} radius="md">
                  <IconRouteAltLeft size={21} />
                </ThemeIcon>
                <Title order={1}>模型代理运行统计</Title>
              </Group>
              <Text c="dimmed">
                阿里云百炼多模型路由 · 数据每 2 秒刷新 · 累计统计每 {data?.metrics_persistence.flush_interval_seconds ?? 5} 秒写入 SQLite
              </Text>
            </Stack>
            <Group gap="xs">
              {data ? <Badge color="teal" variant="dot">代理运行中</Badge> : <Loader size="xs" />}
              <Text size="xs" c="dimmed">{data?.generated_at ?? '正在连接…'}</Text>
            </Group>
          </Group>

          {error && (
            <Alert icon={<IconAlertCircle size={18} />} color="red" title="暂时无法读取统计数据">
              {error}；页面会自动重试。
            </Alert>
          )}

          <SimpleGrid cols={{ base: 1, xs: 2, md: 3, lg: 4 }} spacing="md">
            <MetricCard
              title="客户端请求"
              value={formatInteger(data?.client.requests ?? 0)}
              detail={`成功 ${formatInteger(data?.client.successes ?? 0)} · 失败 ${formatInteger(data?.client.failures ?? 0)}`}
              icon={IconActivity}
              color="blue"
            />
            <MetricCard
              title="请求成功率"
              value={data?.client.requests ? `${successRate.toFixed(1)}%` : '—'}
              detail={`采纳 ${formatInteger(data?.totals.adoptions ?? 0)} · 上游尝试 ${formatInteger(data?.totals.upstream_attempts ?? 0)}`}
              icon={IconBrandSpeedtest}
              color="teal"
            />
            <MetricCard
              title="竞速救回"
              value={data?.totals.hedged_requests ? `${data.totals.hedge_wins} / ${data.totals.hedged_requests}` : '—'}
              detail={`胜率 ${(data?.totals.hedge_win_rate ?? 0).toFixed(1)}% · 丢弃 ${formatInteger(data?.totals.discarded_responses ?? 0)}`}
              icon={IconTrophy}
              color="yellow"
            />
            <MetricCard
              title="端到端延迟"
              value={formatLatency(data?.client.average_latency_ms ?? 0)}
              detail={`P95 ${formatLatency(data?.client.p95_latency_ms ?? 0)} · 最近 ${formatLatency(data?.client.last_latency_ms ?? 0)}`}
              icon={IconClock}
              color="violet"
            />
            <MetricCard
              title="Python 内存"
              value={data ? `${data.process.rss_mb.toFixed(1)} MB` : '—'}
              detail={`CPU ${data?.process.cpu_percent.toFixed(1) ?? '—'}%`}
              icon={IconCpu}
              color="cyan"
            />
            <MetricCard
              title="实时负载"
              value={formatInteger(data?.totals.in_flight ?? 0)}
              detail={`近 1 分钟 ${formatInteger(data?.totals.requests_last_minute ?? 0)} 次上游尝试`}
              icon={IconRouteAltLeft}
              color="orange"
            />
            <MetricCard
              title="Token 用量"
              value={formatInteger((data?.totals.input_tokens ?? 0) + (data?.totals.output_tokens ?? 0))}
              detail={`输入 ${formatInteger(data?.totals.input_tokens ?? 0)} · 输出 ${formatInteger(data?.totals.output_tokens ?? 0)}`}
              icon={IconDatabase}
              color="grape"
            />
          </SimpleGrid>

          <Paper radius="lg" p="lg" withBorder>
            <Stack gap="md">
              <Group justify="space-between" align="flex-end">
                <Box>
                  <Text fw={700}>模型明细</Text>
                  <Text size="sm" c="dimmed">状态、采纳率、竞速胜率、延迟、限流与 Token 用量</Text>
                </Box>
                <Group gap="sm" align="flex-end">
                  <NativeSelect
                    aria-label="选择模型排序指标"
                    data={MODEL_SORT_SELECT_OPTIONS}
                    value={modelSort?.field ?? ''}
                    onChange={(event) => updateModelSortField(event.currentTarget.value || null)}
                    size="xs"
                    w={180}
                  />
                  {modelSort ? (
                    <Button size="xs" variant="default" onClick={toggleSortDirection}>
                      {modelSort.direction === 'asc' ? '升序 ↑' : '降序 ↓'}
                    </Button>
                  ) : null}
                  {modelSort ? (
                    <Button size="xs" variant="subtle" color="gray" onClick={() => persistModelSort(null)}>
                      取消排序
                    </Button>
                  ) : null}
                  <Badge variant="light">{data?.models.length ?? 0} 个模型</Badge>
                </Group>
              </Group>
              <ModelsTable
                models={sortedModels}
                pendingModel={pendingModel}
                onToggle={toggleModel}
              />
            </Stack>
          </Paper>

          <Group justify="space-between" className="footer" gap="sm">
            <Text size="xs" c="dimmed">Base URL：{data?.base_url ?? 'http://127.0.0.1:39281/v1'}</Text>
            <Text size="xs" c="dimmed">运行时长：{data ? formatUptime(data.uptime_seconds) : '—'} · 页面不展示密钥、提示词或正文</Text>
          </Group>
        </Stack>
      </Container>
    </Box>
  )
}

export default App
