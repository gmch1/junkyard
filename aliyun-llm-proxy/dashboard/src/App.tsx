import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Badge,
  Box,
  Button,
  Container,
  Group,
  Loader,
  Paper,
  PasswordInput,
  Progress,
  ScrollArea,
  SimpleGrid,
  Stack,
  Table,
  Text,
  ThemeIcon,
  Title,
  UnstyledButton,
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
  configured?: boolean
  client_key?: string
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
  | 'hedge_participations'
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
  { value: 'hedge_participations', label: '竞速参与数' },
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
  hedge_participations: 'desc',
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
  if (
    field === 'hedge_win_rate'
    || field === 'hedge_wins'
    || field === 'hedge_participations'
  ) {
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
  sort: ModelSort | null
  onToggle: (modelId: string, enabled: boolean) => void
  onSort: (field: ModelSortField) => void
}

type SortableTableHeaderProps = {
  options: { field: ModelSortField; label: string }[]
  sort: ModelSort | null
  onSort: (field: ModelSortField) => void
  align?: 'left' | 'right'
}

function SortableTableHeader({
  options,
  sort,
  onSort,
  align = 'left',
}: SortableTableHeaderProps) {
  const activeOption = options.find((option) => option.field === sort?.field)
  const ariaSort = activeOption
    ? sort?.direction === 'asc' ? 'ascending' : 'descending'
    : 'none'

  return (
    <Table.Th ta={align} aria-sort={ariaSort}>
      <Group
        className="sortable-header-options"
        data-align={align}
        gap={4}
        wrap="nowrap"
      >
        {options.map((option, index) => {
          const active = option.field === sort?.field
          const nextDirection = active
            ? sort.direction === 'asc' ? '降序' : '升序'
            : DEFAULT_SORT_DIRECTIONS[option.field] === 'asc' ? '升序' : '降序'
          return (
            <Group key={option.field} gap={4} wrap="nowrap">
              {index > 0 ? <span className="sortable-header-separator">/</span> : null}
              <UnstyledButton
                type="button"
                className="sortable-header-button"
                data-active={active || undefined}
                onClick={() => onSort(option.field)}
                aria-label={`按${option.label}${nextDirection}排列`}
                title={`按${option.label}${nextDirection}排列`}
              >
                <span>{option.label}</span>
                <span className="sortable-header-indicator" aria-hidden="true">
                  {active ? sort.direction === 'asc' ? '↑' : '↓' : '↕'}
                </span>
              </UnstyledButton>
            </Group>
          )
        })}
      </Group>
    </Table.Th>
  )
}

function ModelsTable({ models, pendingModel, sort, onToggle, onSort }: ModelsTableProps) {
  return (
    <ScrollArea>
      <Table className="models-table" verticalSpacing="sm" horizontalSpacing="md" miw={1550}>
        <Table.Thead>
          <Table.Tr>
            <SortableTableHeader
              options={[{ field: 'model', label: '模型' }, { field: 'rpm', label: 'RPM' }]}
              sort={sort}
              onSort={onSort}
            />
            <SortableTableHeader options={[{ field: 'status', label: '状态' }]} sort={sort} onSort={onSort} />
            <SortableTableHeader options={[{ field: 'adoption_rate', label: '采纳率' }]} sort={sort} onSort={onSort} />
            <SortableTableHeader
              options={[{ field: 'adoptions', label: '采纳' }, { field: 'attempts', label: '参与' }]}
              sort={sort}
              onSort={onSort}
              align="right"
            />
            <SortableTableHeader
              options={[
                { field: 'hedge_wins', label: '胜出' },
                { field: 'hedge_participations', label: '参与' },
                { field: 'hedge_win_rate', label: '胜率' },
              ]}
              sort={sort}
              onSort={onSort}
              align="right"
            />
            <SortableTableHeader options={[{ field: 'discarded_responses', label: '丢弃' }]} sort={sort} onSort={onSort} align="right" />
            <SortableTableHeader
              options={[{ field: 'successes', label: '成功' }, { field: 'failures', label: '失败' }]}
              sort={sort}
              onSort={onSort}
              align="right"
            />
            <SortableTableHeader options={[{ field: 'throttles', label: '限流' }]} sort={sort} onSort={onSort} align="right" />
            <SortableTableHeader
              options={[
                { field: 'average_latency_ms', label: '平均' },
                { field: 'p95_latency_ms', label: 'P95' },
                { field: 'last_latency_ms', label: '最近' },
              ]}
              sort={sort}
              onSort={onSort}
              align="right"
            />
            <SortableTableHeader options={[{ field: 'requests_last_minute', label: '近 1 分钟' }]} sort={sort} onSort={onSort} align="right" />
            <SortableTableHeader
              options={[
                { field: 'input_tokens', label: '输入' },
                { field: 'output_tokens', label: '输出' },
                { field: 'total_tokens', label: '总 Token' },
              ]}
              sort={sort}
              onSort={onSort}
              align="right"
            />
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
  const [apiKey, setApiKey] = useState('')
  const [savingKey, setSavingKey] = useState(false)
  const [pendingModel, setPendingModel] = useState('')
  const [modelSort, setModelSort] = useState<ModelSort | null>(loadModelSort)

  useEffect(() => {
    let active = true
    let timer: number | undefined
    let controller: AbortController | undefined

    const refresh = async () => {
      controller = new AbortController()
      try {
        const response = await fetch('/admin/status', {
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

  const toggleModelSort = (field: ModelSortField) => {
    persistModelSort({
      field,
      direction: modelSort?.field === field
        ? modelSort.direction === 'asc' ? 'desc' : 'asc'
        : DEFAULT_SORT_DIRECTIONS[field],
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
        if (payload.dashboard) setData((current) => current ? { ...current, ...payload.dashboard } : payload.dashboard ?? null)
        const message = payload.error?.upstream_code?.includes('AllocationQuota')
          ? '模型额度仍未恢复，当前状态保持不变。'
          : payload.error?.message || `HTTP ${response.status}`
        throw new Error(message)
      }
      setData((current) => current ? { ...current, ...payload.dashboard } : payload.dashboard ?? null)
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

  const copyValue = async (label: string, value?: string) => {
    if (!value) return
    try {
      await navigator.clipboard.writeText(value)
      notifications.show({ title: `${label} 已复制`, message: value, color: 'teal' })
    } catch {
      notifications.show({ title: '复制失败', message: '请手动选择并复制。', color: 'red' })
    }
  }

  const saveUpstreamKey = async () => {
    const value = apiKey.trim()
    if (value.length < 8) {
      notifications.show({ title: 'API Key 格式不正确', message: '请检查后重新输入。', color: 'red' })
      return
    }
    setSavingKey(true)
    try {
      const response = await fetch('/admin/upstream-key', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Aliyun-Proxy-Admin': '1',
        },
        body: JSON.stringify({ api_key: value }),
      })
      const payload = await response.json() as DashboardData & { error?: { message?: string } }
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`)
      setData(payload)
      setApiKey('')
      notifications.show({ title: 'DashScope API Key 已保存', message: '新请求会立即使用新的密钥。', color: 'teal' })
    } catch (caught) {
      notifications.show({ title: '保存失败', message: caught instanceof Error ? caught.message : '未知错误', color: 'red' })
    } finally {
      setSavingKey(false)
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

          <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
            <Paper radius="lg" p="lg" withBorder>
              <Stack gap="md">
                <Group justify="space-between">
                  <Text fw={700}>客户端连接</Text>
                  <Badge color={data?.configured ? 'teal' : 'orange'} variant="light">
                    {data?.configured ? '上游已配置' : '等待配置'}
                  </Badge>
                </Group>
                <Stack gap="xs">
                  <Group justify="space-between" wrap="nowrap">
                    <Stack gap={1} className="connection-value">
                      <Text size="xs" c="dimmed">Base URL</Text>
                      <Text size="sm" ff="monospace">{data?.base_url ?? '正在获取…'}</Text>
                    </Stack>
                    <Button size="xs" variant="light" onClick={() => copyValue('Base URL', data?.base_url)}>复制</Button>
                  </Group>
                  <Group justify="space-between" wrap="nowrap">
                    <Stack gap={1} className="connection-value">
                      <Text size="xs" c="dimmed">客户端 API Key</Text>
                      <Text size="sm" ff="monospace">{data?.client_key ?? '正在获取…'}</Text>
                    </Stack>
                    <Button size="xs" variant="light" onClick={() => copyValue('客户端 API Key', data?.client_key)}>复制</Button>
                  </Group>
                  <Group justify="space-between" wrap="nowrap">
                    <Stack gap={1} className="connection-value">
                      <Text size="xs" c="dimmed">模型名称</Text>
                      <Text size="sm" ff="monospace">{data?.model_alias ?? 'aliyun-translate-auto'}</Text>
                    </Stack>
                    <Button size="xs" variant="light" onClick={() => copyValue('模型名称', data?.model_alias)}>复制</Button>
                  </Group>
                </Stack>
              </Stack>
            </Paper>

            <Paper radius="lg" p="lg" withBorder>
              <Stack gap="md">
                <Stack gap={2}>
                  <Text fw={700}>阿里云百炼凭证</Text>
                  <Text size="xs" c="dimmed">密钥仅写入本机状态目录，保存后不会再次显示。</Text>
                </Stack>
                <PasswordInput
                  label="DashScope API Key"
                  placeholder="sk-xxxxxxxxxxxxxxxx"
                  value={apiKey}
                  onChange={(event) => setApiKey(event.currentTarget.value)}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') void saveUpstreamKey()
                  }}
                />
                <Button loading={savingKey} onClick={() => void saveUpstreamKey()}>
                  {data?.configured ? '更新 API Key' : '保存 API Key'}
                </Button>
              </Stack>
            </Paper>
          </SimpleGrid>

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
              title="Go 服务内存"
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
              <Group justify="space-between" align="center">
                <Text fw={700}>模型明细</Text>
                <Group gap="xs">
                  {modelSort ? (
                    <Button size="xs" variant="subtle" color="gray" onClick={() => persistModelSort(null)}>
                      取消排序
                    </Button>
                  ) : null}
                </Group>
              </Group>
              <ModelsTable
                models={sortedModels}
                pendingModel={pendingModel}
                sort={modelSort}
                onToggle={toggleModel}
                onSort={toggleModelSort}
              />
            </Stack>
          </Paper>

          <Group justify="space-between" className="footer" gap="sm">
            <Text size="xs" c="dimmed">Base URL：{data?.base_url ?? 'http://127.0.0.1:39281/v1'}</Text>
            <Text size="xs" c="dimmed">运行时长：{data ? formatUptime(data.uptime_seconds) : '—'} · 管理页仅允许本机访问，不记录提示词或正文</Text>
          </Group>
        </Stack>
      </Container>
    </Box>
  )
}

export default App
