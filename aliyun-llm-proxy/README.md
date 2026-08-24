# 阿里云多模型本地代理

一个监听 `127.0.0.1:39281` 的 OpenAI Chat Completions 兼容代理。它最初用于 Read Frog 网页翻译，也可以被其他兼容 OpenAI 接口的本地客户端使用。

代理会在多个阿里云百炼模型之间轮询分配请求。模型触发限流、暂时故障或额度耗尽时，会在响应开始前自动切换到下一模型；内置的 Mantine 页面用于查看请求量、模型分配、延迟、Token、限流、冷却状态以及 Python 进程内存。

## 功能

- OpenAI 兼容接口：`POST /v1/chat/completions`、`GET /v1/models`
- 分层调度：优先 Qwen-MT，其次使用现有 60 RPM 模型，再从 500 RPM 及以上模型池中随机选择当前负载较低的模型
- 保留原有模型，并追加经过实际翻译探测的 Qwen、DeepSeek、Kimi 与 MiniMax 稳定版模型；重试与 5 秒对冲复用同一随机调度
- 识别 429、部分 4xx 和 5xx，并按模型独立冷却或降级
- 请求超过 5 秒仍无有效结果时，启动一条备用调度通道并采用最先完成的响应
- 额度永久耗尽的模型写入本地状态，后续启动自动跳过
- 除 Qwen-MT 适配器外严格透明转发，只替换上游 `model`
- Qwen-MT 识别 Read Frog 目标语言，支持完整型号 92 种、Lite 31 种语言能力检查
- 本地 API Key 与阿里云 API Key 持久保存，权限为 `0600`
- 运行统计每 5 秒批量写入 SQLite，重启代理后继续累计
- 统计页支持人工禁用和启用模型，状态写回本地配置
- 运行统计不保存或展示网页正文、提示词和任何 API Key
- 停止代理时 Python 进程退出，不保留本地模型或额外常驻进程

## 要求

- Python 3.9 或更高版本；代理运行时只使用标准库
- Node.js 22 或更高版本与 pnpm 11；仅构建统计页面时需要
- 已开通阿里云百炼并拥有 DashScope API Key

## macOS 应用

项目提供轻量 macOS 菜单栏应用，不显示 Dock 图标或前台主窗口。启动后应用会拉起内置的静态 Go 后端并打开浏览器管理页，用于填写 DashScope API Key，以及展示可复制的 Base URL、客户端 API Key 和模型名称；不依赖系统 Python 或 Docker。

macOS 应用默认在 `39281` 端口监听局域网，OpenAI 接口仍要求客户端 Bearer Key；不要把该端口转发到公网。网页管理与密钥写入只允许从本机访问，Key 和配置保存到 `~/Library/Application Support/AliyunLLMProxy/`。菜单栏可随时打开管理页、复制连接信息、启动或停止服务。

GitHub Actions 会构建同时支持 Apple Silicon 和 Intel Mac 的 ZIP Artifact。本地构建方法和应用说明见 [`macos-app/README.md`](./macos-app/README.md)。原有 Python CLI、本地 Dashboard 和默认回环监听行为保持不变。

## 安装

```bash
git clone https://github.com/gmch1/junkyard.git
cd junkyard/aliyun-llm-proxy
pnpm --dir dashboard install --frozen-lockfile
pnpm --dir dashboard run build
python3 aliyun_proxy.py set-upstream-key
```

最后一条命令会隐藏输入内容。阿里云 Key 保存到：

```text
.aliyun-proxy/dashscope.key
```

也可以从环境变量一次性导入：

```bash
DASHSCOPE_API_KEY='你的 Key' python3 aliyun_proxy.py set-upstream-key --from-env
```

本地客户端 Key 保存到 `.aliyun-proxy/client.key`。这两个文件、配置覆盖、不可用模型状态、SQLite 指标库、PID 和日志都位于 `.aliyun-proxy/`，已被 Git 忽略，不会提交到仓库。

## 启动和管理

```bash
python3 aliyun_proxy.py start
python3 aliyun_proxy.py status
python3 aliyun_proxy.py logs
python3 aliyun_proxy.py stop
```

其他命令：

```bash
python3 aliyun_proxy.py key
python3 aliyun_proxy.py config
python3 aliyun_proxy.py unavailable
python3 aliyun_proxy.py reset-unavailable deepseek-v4-flash
python3 aliyun_proxy.py restart
```

统计页面：

```text
http://127.0.0.1:39281/v1
```

页面每 2 秒在上一请求完成后继续刷新，不会堆积轮询请求。它显示模型采纳率、竞速参与与胜出、丢弃响应、请求延迟和资源用量，并提供模型的禁用和启用按钮。点击模型列表的表头可按状态、采纳率、成功/失败、限流、各项延迟、Token 等指标排列，再次点击切换升降序；排序字段和方向会保存在浏览器 `localStorage`，点击“取消排序”后恢复后端原始顺序。禁用只停止后续调度，不删除模型或历史统计。对额度或权限原因导致的不可用模型点击“启用”时，代理会先直连该模型进行一次轻量检测；只有检测成功才解除不可用状态，失败会以 Toast 显示原因且状态保持不变。

累计指标保存在 `.aliyun-proxy/metrics.sqlite3`，默认每 5 秒批量提交一次，正常停止服务时还会执行最后一次提交。因此异常退出最多损失约 5 秒的新增指标，重启不会清空已落库的数据。数据库只保存计数、Token 汇总和最近延迟样本，不保存请求正文、提示词或任何 API Key。刷新间隔可通过 `.aliyun-proxy/proxy.json` 的 `metrics_flush_interval_seconds` 调整。

`GET /v1/proxy/dashboard-data` 是页面使用的无敏感信息接口；原始状态接口 `GET /v1/proxy/status` 仍要求本地 Bearer Key。模型控制接口只接受同源统计页面带有专用标记的请求，且不允许禁用最后一个可用模型。

## Read Frog 设置

在 `Custom Chat Complete` Provider 中填写：

- Base URL：`http://127.0.0.1:39281/v1`
- API Key：`python3 aliyun_proxy.py key` 的输出
- Custom model：可以填写任意字符串，建议用 `aliyun-translate-auto`
- Temperature：`0`

建议从以下翻译队列参数开始：

- 最大突发请求数：`10`
- 每秒请求数：`10`
- 最大字符数：`1000`
- 最大段落数：`4`
- 预翻译距离：`1000`
- 可见比例：`0.1`

确认没有频繁限流后，再逐步提高到每秒 `15` 或 `20`。不建议直接使用 30 RPS，因为百炼还可能检查瞬时增速并返回 `Throttling.BurstRate`。

旧名称 `translategemma-4b-it` 作为兼容别名保留。代理不根据调用方填写的模型名选择上游，而是使用自己的模型池。

## 请求透明性与 Qwen-MT 例外

除 Qwen-MT 外，代理只把调用方的 `model` 替换为路由选中的阿里云模型；`messages`、提示词、`temperature`、`max_tokens`、`stream` 和其他参数均不新增、不删除、不合并、不改写，因此不会与 Read Frog 的自定义提示词冲突。

Qwen-MT 是唯一例外。适配器会从系统提示词和最后一条 User Message 识别目标语言，把 Read Frog 的 `Translate to 目标语言: 正文` 包装剥离为正文，再生成 Qwen-MT 所需的单条 User Message 和 `translation_options`。调用方目标语言优先，不叠加另一套翻译提示词。

语言表按 Read Frog 使用的 `@read-frog/definitions@0.4.4` 与阿里云 [Qwen-MT 支持语言表](https://help.aliyun.com/zh/model-studio/machine-translation/) 映射，例如：

- `Simplified Mandarin Chinese → zh`
- `Traditional Mandarin Chinese → zh_tw`
- `Standard Arabic → ar`

某个 MT 型号不支持目标语言时会在本地跳过；所有 MT 型号都不支持，或阿里云返回“暂时不支持当前设置的语种”，则自动交给通用模型。

## 默认模型池

默认包含 Qwen Flash、Plus、Instruct 以及 Qwen-MT Flash/Lite/Plus/Turbo。完整配置位于首次启动生成的 `.aliyun-proxy/proxy.json`，筛选数据见 [`aliyun_models_600.json`](./aliyun_models_600.json)。

官方将部分模型标为 60 RPM，但实际还可能存在隐藏并发、瞬时增速或容量限制。因此这些模型额外设置 `min_interval_seconds: 30`：同一型号 30 秒内最多被选中一次。若其中一个返回 429，同次请求会跳过其他低频型号并直接使用高频池。

`deepseek-v4-flash` 被用作额度探针。账号返回 `403 AllocationQuota.FreeTierOnly` 后，代理将其写入 `.aliyun-proxy/unavailable_models.json` 并在后续启动中跳过。额度恢复后可用 `reset-unavailable` 清除状态。

## 5 秒延迟对冲

主请求发送 5 秒后仍没有有效响应，代理会开启一条备用通道。备用通道不会指定固定模型，而是再次调用同一个调度器，并排除已经在途或尝试过的型号，因此仍会遵守：

- 模型优先级与轮询游标
- 60 RPM 型号的 30 秒最小间隔
- RPM、瞬时 RPS、冷却和持久不可用状态
- 流式兼容性与 Qwen-MT 语种能力检查

两条通道中第一个得到有效 `2xx` 响应的模型被采纳，另一个响应完成后丢弃。即使备用通道已经启动，主通道先完成时仍然采纳主通道。`429` 和可重试的 `5xx` 仍会立即使用原有降级逻辑，不需要先等待 5 秒。

非流式请求以完整响应到达为准；流式请求会先读取并缓存第一个真实内容片段，以最先产出内容的模型为准，再把缓存片段和后续流原样写给客户端。每个客户端请求最多同时存在一条主通道和一条备用通道，全局默认最多运行 4 条备用通道。

配置位于 `.aliyun-proxy/proxy.json`：

```json
{
  "hedging": {
    "enabled": true,
    "delay_seconds": 5,
    "max_concurrent_backups": 4
  }
}
```

延迟对冲可能产生一次额外上游调用。Python 标准库无法保证阿里云在本地断开连接后立刻停止推理，因此统计页会分别展示采纳、竞速胜出和丢弃次数，便于根据实际收益判断是否调整阈值。

## 自动降级

规则依据百炼[错误码文档](https://help.aliyun.com/zh/model-studio/error-code/)：

| HTTP / 错误码 | 行为 |
| --- | --- |
| `429 Throttling.*` | 当前模型冷却，切换下一模型 |
| `500/502/503/504` | 当前模型短暂冷却，切换下一模型 |
| `403/404 ModelNotFound/Model.AccessDenied` | 暂停当前模型，切换下一模型 |
| `403 AllocationQuota.FreeTierOnly` | 持久禁用当前模型，切换下一模型 |
| Qwen-MT 语种参数错误 | 跳过剩余 MT，切换通用模型 |
| 其他 `400` | 原样返回，不掩盖请求问题 |
| `401 InvalidApiKey` | 原样返回，不重复发送 |

若响应包含 `Retry-After`，优先采用服务端给出的等待时间。非流式请求可以在返回内容前安全切换；流式请求一旦已经向客户端发送内容，就不会中途拼接另一模型的输出。

## 开发与验证

```bash
pnpm --dir dashboard run lint
pnpm --dir dashboard run build
python3 -m py_compile aliyun_proxy.py test_aliyun_proxy.py
python3 -m unittest -v test_aliyun_proxy.py
```

前端开发服务器：

```bash
pnpm --dir dashboard run dev
```

Vite 会把统计接口代理到正在运行的 `127.0.0.1:39281` 服务。

## 安全边界

- 服务默认仅绑定 `127.0.0.1`，不会监听局域网或公网地址。
- Chat Completions、模型列表和完整状态接口要求本地 Bearer Key。
- 统计页面与统计数据接口不要求 Key，但仅包含计数、延迟、Token 汇总和进程资源信息。
- 日志不记录请求正文、完整提示词或 API Key；Qwen-MT 只记录目标语言代码与正文字符数。
- 仓库不会提交 `.aliyun-proxy/`、`*.key`、日志、前端依赖或构建产物。

### 可选的进程环境变量

发行包或服务管理器可以显式覆盖运行位置与监听方式：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ALIYUN_PROXY_STATE_DIR` | 项目内 `.aliyun-proxy/` | Key、配置、日志和 SQLite 状态目录 |
| `ALIYUN_PROXY_HOST` | 配置文件中的 `127.0.0.1` | 进程本次运行使用的监听地址 |
| `ALIYUN_PROXY_PORT` | 配置文件中的 `39281` | 进程本次运行使用的端口 |
| `ALIYUN_PROXY_ALLOW_LAN` | `false` | 非回环监听的显式安全开关 |
| `ALIYUN_PROXY_DASHBOARD_ENABLED` | `true` | 是否在业务 HTTP 服务上提供 Dashboard |

这些覆盖只作用于当前进程，不回写 `proxy.json`。非回环地址必须同时设置 `ALIYUN_PROXY_ALLOW_LAN=1` 和 `ALIYUN_PROXY_DASHBOARD_ENABLED=0`，因此单独误设监听地址不会意外开放服务或管理页面。
