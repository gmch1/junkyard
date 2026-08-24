import { memo, useMemo } from 'react'
import { Box, Paper, SimpleGrid, Stack, Text } from '@mantine/core'
import { IconActivity } from '@tabler/icons-react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { ModelMetrics } from './App'

function shortenModelName(name: string) {
  return name.length > 27 ? `${name.slice(0, 25)}…` : name
}

function EmptyChart() {
  return (
    <Box className="empty-chart">
      <Stack align="center" gap={8}>
        <IconActivity size={28} stroke={1.5} />
        <Text size="sm" c="dimmed">等待第一批请求</Text>
      </Stack>
    </Box>
  )
}

const RequestDistributionChart = memo(function RequestDistributionChart({ models }: { models: ModelMetrics[] }) {
  const data = useMemo(
    () => models
      .filter((model) => model.successes || model.failures)
      .sort((a, b) => (b.successes + b.failures) - (a.successes + a.failures))
      .map((model) => ({
        name: shortenModelName(model.id),
        成功: model.successes,
        失败: model.failures,
      })),
    [models],
  )

  if (!data.length) return <EmptyChart />

  return (
    <Box h={Math.max(280, data.length * 37)}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} layout="vertical" margin={{ left: 12, right: 20 }}>
          <CartesianGrid strokeDasharray="3 3" horizontal={false} stroke="var(--chart-grid)" />
          <XAxis type="number" allowDecimals={false} tick={{ fill: 'var(--chart-label)', fontSize: 12 }} />
          <YAxis dataKey="name" type="category" width={185} tick={{ fill: 'var(--chart-label)', fontSize: 12 }} />
          <Tooltip cursor={{ fill: 'var(--chart-hover)' }} />
          <Legend />
          <Bar dataKey="成功" stackId="requests" fill="#12b886" radius={[0, 4, 4, 0]} isAnimationActive={false} />
          <Bar dataKey="失败" stackId="requests" fill="#fa5252" radius={[0, 4, 4, 0]} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </Box>
  )
})

const LatencyChart = memo(function LatencyChart({ models }: { models: ModelMetrics[] }) {
  const data = useMemo(
    () => models
      .filter((model) => model.successes)
      .sort((a, b) => b.average_latency_ms - a.average_latency_ms)
      .map((model) => ({
        name: shortenModelName(model.id),
        平均延迟: Math.round(model.average_latency_ms),
        P95延迟: Math.round(model.p95_latency_ms),
      })),
    [models],
  )

  if (!data.length) return <EmptyChart />

  return (
    <Box h={Math.max(280, data.length * 37)}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} layout="vertical" margin={{ left: 12, right: 20 }}>
          <CartesianGrid strokeDasharray="3 3" horizontal={false} stroke="var(--chart-grid)" />
          <XAxis type="number" unit=" ms" tick={{ fill: 'var(--chart-label)', fontSize: 12 }} />
          <YAxis dataKey="name" type="category" width={185} tick={{ fill: 'var(--chart-label)', fontSize: 12 }} />
          <Tooltip cursor={{ fill: 'var(--chart-hover)' }} />
          <Legend />
          <Bar dataKey="平均延迟" fill="#228be6" radius={[0, 4, 4, 0]} isAnimationActive={false} />
          <Bar dataKey="P95延迟" fill="#845ef7" radius={[0, 4, 4, 0]} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </Box>
  )
})

function ChartPanels({ models }: { models: ModelMetrics[] }) {
  return (
    <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="lg">
      <Paper radius="lg" p="lg" withBorder>
        <Stack gap="md">
          <Box>
            <Text fw={700}>请求分配</Text>
            <Text size="sm" c="dimmed">每个上游模型处理成功或失败的次数</Text>
          </Box>
          <RequestDistributionChart models={models} />
        </Stack>
      </Paper>
      <Paper radius="lg" p="lg" withBorder>
        <Stack gap="md">
          <Box>
            <Text fw={700}>模型响应延迟</Text>
            <Text size="sm" c="dimmed">最近 200 次成功调用的平均值与 P95</Text>
          </Box>
          <LatencyChart models={models} />
        </Stack>
      </Paper>
    </SimpleGrid>
  )
}

export default ChartPanels
