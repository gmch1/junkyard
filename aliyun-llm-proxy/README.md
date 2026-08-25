# 阿里云百炼多模型代理

这是一个使用 Go 实现的 OpenAI Chat Completions 兼容代理。它面向 Read Frog 等并发翻译客户端，在多个阿里云百炼模型之间调度请求，并提供本机 Dashboard 与 macOS 菜单栏应用。

项目只有一份后端实现：命令行、Linux/macOS 服务、Dashboard API 和 macOS App 内置服务都编译自根目录的 Go module。仓库不再维护独立的 Python 代理或 macOS 专用代理逻辑。

## 功能

- 提供 `POST /v1/chat/completions` 与 `GET /v1/models`。
- 调用方模型名统一映射为本地别名 `aliyun-translate-auto`。
- 按优先级、实时负载和随机策略在模型池中分配请求。
- 本地限制每个模型的 RPM、瞬时 RPS 和最小调用间隔。
- 遇到 `429`、可重试的 `5xx`、模型无权限或额度耗尽时自动降级。
- 请求超过默认 5 秒仍未完成时启动一条备用通道，采用最先完成的有效响应。
- Qwen-MT 自动识别 Read Frog 目标语言，并检查 Lite/完整型号的语言能力。
- 将永久不可用模型、人工启用状态和累计指标持久保存。
- Dashboard 展示请求、延迟、Token、对冲、限流、资源使用和完整模型列表。
- Dashboard 可以禁用/启用模型；恢复不可用模型前会先执行轻量探测。
- Dashboard 同时管理 DashScope API Key，并展示可复制的客户端连接信息。
- 不记录请求正文、提示词或任何 API Key。

## 要求

- Go 1.22 或更高版本。
- Node.js 22 与 pnpm 11 仅用于修改和重新构建 Dashboard。
- 已开通阿里云百炼并拥有 DashScope API Key。

仓库包含已构建的 Dashboard 静态资源，因此正常编译代理不需要 Node.js。

## 构建与启动

```bash
git clone https://github.com/gmch1/junkyard.git
cd junkyard/aliyun-llm-proxy
go build -o build/aliyun-llm-proxy .
./build/aliyun-llm-proxy start
```

首次启动后访问 `http://127.0.0.1:39281/`，可以直接在管理页填写 DashScope API Key。也可以通过命令行安全写入：

```bash
./build/aliyun-llm-proxy set-upstream-key
DASHSCOPE_API_KEY='你的 Key' ./build/aliyun-llm-proxy set-upstream-key --from-env
```

代理未配置上游 Key 时仍会启动，以便打开本机管理页；Chat Completions 请求会返回明确的 `upstream_not_configured` 错误。

## 管理命令

```bash
./build/aliyun-llm-proxy start
./build/aliyun-llm-proxy status
./build/aliyun-llm-proxy logs
./build/aliyun-llm-proxy stop
./build/aliyun-llm-proxy restart
./build/aliyun-llm-proxy key
./build/aliyun-llm-proxy config
./build/aliyun-llm-proxy unavailable
./build/aliyun-llm-proxy reset-unavailable deepseek-v4-flash
./build/aliyun-llm-proxy reload-key
./build/aliyun-llm-proxy probe
```

默认状态目录为执行命令时当前目录下的 `.aliyun-proxy/`：

- `proxy.json`：监听、路由、对冲与模型配置。
- `client.key`：客户端 Bearer Key。
- `dashscope.key`：阿里云 DashScope Key。
- `unavailable_models.json`：永久不可用模型状态。
- `metrics.sqlite3`：累计请求与模型指标。
- `proxy.pid` / `proxy.log`：后台进程状态和日志。

凭证与配置文件权限为 `0600`，状态目录权限为 `0700`。可通过 `ALIYUN_PROXY_STATE_DIR` 改变状态目录。

## Read Frog 设置

- Base URL：`http://127.0.0.1:39281/v1`
- API Key：Dashboard 中显示的客户端 Key，或 `./build/aliyun-llm-proxy key` 输出。
- Custom model：`aliyun-translate-auto`
- Temperature：`0`

建议从最大突发请求数 `10`、每秒请求数 `10`、最大字符数 `1000`、最大段落数 `4` 开始，确认没有频繁限流后再逐步提高。旧名称 `translategemma-4b-it` 继续作为兼容别名返回。

## Dashboard 与指标

Dashboard 每 2 秒在上一请求完成后刷新，展示：

- 客户端请求量、成功率与端到端平均/P95/最近延迟。
- 上游参与、采纳率、模型成功/失败和限流次数。
- 对冲请求、备用通道胜出和已丢弃响应。
- 每个模型的状态、近一分钟请求、Token、延迟和冷却原因。
- Go 服务 RSS 与 CPU 使用。

模型列表支持按表头排序，排序偏好保存在浏览器 `localStorage`。禁用模型只影响后续调度，不删除历史指标；不允许禁用最后一个启用的模型。

累计指标每 5 秒批量写入 `.aliyun-proxy/metrics.sqlite3`，正常停止时执行最后一次提交。数据库结构与原实现兼容，不保存正文、提示词或凭证。

## Qwen-MT 适配

除 Qwen-MT 外，代理仅替换上游 `model`，不改写 `messages`、提示词、`temperature`、`max_tokens`、`stream` 或其他参数。

Qwen-MT 会从系统提示词和最后一条 User Message 推断目标语言，移除 `Translate to 目标语言: 正文` 包装，并生成阿里云所需的 `translation_options`。Read Frog 的语言名称会映射为阿里云语言代码，例如：

- `Simplified Mandarin Chinese → zh`
- `Traditional Mandarin Chinese → zh_tw`
- `Standard Arabic → ar`

当前 MT 型号不支持目标语言时会尝试其他 MT 型号；所有 MT 型号都不支持或阿里云返回语言参数错误时，自动降级到通用模型。

## 自动降级

| HTTP / 错误码 | 行为 |
| --- | --- |
| `429 Throttling.*` | 当前模型冷却并切换下一模型 |
| `500/502/503/504` | 短暂冷却并切换下一模型 |
| `403/404 ModelNotFound/Model.AccessDenied` | 暂停当前模型并切换 |
| `403 AllocationQuota.FreeTierOnly` | 持久禁用当前模型并切换 |
| Qwen-MT 语种参数错误 | 跳过剩余 MT，切换通用模型 |
| 其他 `400` | 原样返回，不掩盖请求问题 |
| `401 InvalidApiKey` | 原样返回，不重复发送 |

若响应包含 `Retry-After`，优先使用上游提供的冷却时间。流式请求在内容发给客户端后不会拼接另一模型的输出。

## 5 秒延迟对冲

主请求超过配置的 `hedging.delay_seconds` 后仍未得到有效响应，代理会从同一调度器启动一个备用请求，并排除已经在途或尝试过的模型。最先得到有效响应的通道被采纳，较慢的成功响应完成后计为丢弃。

```json
{
  "hedging": {
    "enabled": true,
    "delay_seconds": 5,
    "max_concurrent_backups": 4
  }
}
```

对冲可能产生额外的上游调用。Dashboard 会分别展示对冲请求、胜出和丢弃次数。

## macOS 应用

`macos-app` 是这份 Go 服务的菜单栏启动器。App 不维护另一套代理实现：构建时直接把根目录 Go module 编译成 Universal 后端并放入应用包。

App 启动后自动保证服务运行，并在默认浏览器打开本机 Dashboard；菜单栏“打开管理页面”可以再次打开。退出 App 会同时停止后端。状态保存在 `~/Library/Application Support/AliyunLLMProxy/`。

构建方法见 [`macos-app/README.md`](./macos-app/README.md)。

## 开发与验证

```bash
go test -race ./...
go vet ./...
pnpm --dir dashboard install --frozen-lockfile
pnpm --dir dashboard run lint
pnpm --dir dashboard run build
```

修改 Dashboard 后必须提交重新生成的 `dashboard/dist/`，保证普通 Go 构建不依赖 Node.js。GitHub Actions 会重新构建前端、执行 Go race test，并在 macOS 上构建和验证 Universal App。

## 安全边界

- 根目录 CLI 默认只监听 `127.0.0.1`。
- OpenAI 接口和完整状态接口要求客户端 Bearer Key。
- Dashboard、Dashboard 数据、凭证写入和模型控制仅接受回环连接。
- macOS App 可在可信局域网监听 OpenAI 接口，但管理端点仍仅限本机。
- 不要把代理端口转发到公网。
- 日志和 SQLite 指标不保存请求正文、提示词或任何 API Key。

### 运行时环境变量

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ALIYUN_PROXY_STATE_DIR` | 当前目录 `.aliyun-proxy/` | 状态、凭证与日志目录 |
| `ALIYUN_PROXY_HOST` | 配置中的 `127.0.0.1` | 本次进程监听地址 |
| `ALIYUN_PROXY_PORT` | 配置中的 `39281` | 本次进程监听端口 |
| `ALIYUN_PROXY_ALLOW_LAN` | 配置值 | 显式允许非回环监听 |
| `ALIYUN_PROXY_DASHBOARD_ENABLED` | `true` | 是否提供仅限本机的 Dashboard |

环境变量只影响当前进程，不用于保存凭证。
