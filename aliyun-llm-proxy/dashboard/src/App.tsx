import { useEffect, useState } from 'react'
import {
  Alert,
  Badge,
  Box,
  Container,
  Group,
  Loader,
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

function ModelsTable({ models }: { models: ModelMetrics[] }) {
  return (
    <ScrollArea>
      <Table className="models-table" verticalSpacing="sm" horizontalSpacing="md" miw={1450}>
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
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {models.map((model) => {
            const state = modelState(model)
            const reason = model.unavailable_reason || model.cooldown_reason
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
              <Text c="dimmed">阿里云百炼多模型路由 · 数据每 2 秒刷新 · 仅统计本次启动</Text>
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
              <Group justify="space-between">
                <Box>
                  <Text fw={700}>模型明细</Text>
                  <Text size="sm" c="dimmed">状态、采纳率、竞速胜率、延迟、限流与 Token 用量</Text>
                </Box>
                <Badge variant="light">{data?.models.length ?? 0} 个模型</Badge>
              </Group>
              <ModelsTable models={data?.models ?? []} />
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
