# Bemfa Lamp Bridge

把巴法云中的小爱/米家灯设备命令转发给 BLE Lamp Remote Android 网关：

```text
小爱同学 / 米家
        │
        ▼
巴法云 MQTT（TLS 1.2）
        │  topic: xidingdeng002xidingdeng002
        ▼
188 上的 Bemfa Lamp Bridge
        │  LAN HTTP + Bearer Token
        ▼
Android BLE Lamp Remote
```

首版只接受两个完全匹配的 payload：

| 巴法消息 | Android 请求 |
| --- | --- |
| `on` | `POST /v1/light/on` |
| `off` | `POST /v1/light/off` |

`on#80`、颜色、色温、查询和其他消息都会被忽略。当前 Android API 只有相对调光动作，灯具也不回传物理状态，因此桥接不会猜测绝对亮度或向巴法云虚报开关状态。

当前巴法云设备使用 **MQTT** 协议，Topic 必须逐字匹配 `xidingdeng002xidingdeng002`。末尾的 `002` 是巴法/米家识别灯设备的类型标记；同名或近似的 TCP Topic 与这套桥接不互通。

## 安全约束

- 只允许 MQTT 3.1.1 的 `ssl://` 连接，默认 `ssl://bemfa.com:9503`；
- 只允许访问私有或 loopback IPv4/IPv6 上的 Android API；
- 巴法 UID 和 Android Token 只能从权限不宽于 `0600` 的文件读取；
- 默认 `BEMFA_BRIDGE_DRY_RUN=true`，dry-run 下不会构造或发送动作 HTTP 请求；
- 忽略 retained 和带 MQTT DUP 标记的消息，避免执行历史命令或 QoS 1 重投；
- 消息过载时只保留最新命令，不积压过时动作；
- 成功动作在短时间窗口内继续按内容去重；
- HTTP 动作失败或超时后不会自动重试；
- Android API 请求禁用环境代理且不跟随重定向，避免 Bearer Token 外泄；
- 日志不输出 UID、Token、Authorization 或未解析消息原文，只会记录白名单内的 `on`/`off` 名称。

## 配置

非秘密配置见 [`.env.example`](.env.example)。秘密文件内容分别为纯文本巴法 UID 和 Android API Token，末尾换行可选。

```bash
chmod 600 /secure/path/bemfa-uid /secure/path/lamp-api-token

export BEMFA_BRIDGE_UID_FILE=/secure/path/bemfa-uid
export BEMFA_BRIDGE_LAMP_TOKEN_FILE=/secure/path/lamp-api-token
export BEMFA_BRIDGE_TOPIC=xidingdeng002xidingdeng002
export BEMFA_BRIDGE_LAMP_API_URL=http://192.168.31.129:8791
export BEMFA_BRIDGE_DRY_RUN=true

go run ./cmd/bemfa-lamp-bridge --check-config
go run ./cmd/bemfa-lamp-bridge
```

不要把秘密值写进 shell 历史、环境示例、容器镜像或 Git。

## 验证

```bash
go fmt ./...
go vet ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/bemfa-lamp-bridge
```

测试使用内存 fake 和自定义 HTTP transport，不会连接巴法云、手机或灯具。

## 188 部署

生产沿用 188 上现有的 rootless Podman Quadlet 模式。示例位于 [`deploy/bemfa-lamp-bridge.container.example`](deploy/bemfa-lamp-bridge.container.example)。

1. CI 构建并验证 Linux/amd64 容器；`main` 推送提供 Actions artifact，只有 `ble-lamp-remote-v*` 标签构建才把 `bemfa-lamp-bridge-linux-amd64.image.tar` 加入 GitHub Release；
2. 在 188 用 `grep 'bemfa-lamp-bridge-linux-amd64.image.tar$' SHA256SUMS | sha256sum -c -` 校验归档，再执行 `podman load --input bemfa-lamp-bridge-linux-amd64.image.tar`；
3. 使用 `podman secret` 从权限为 `0600` 的本地文件创建两个 secret；
4. 复制并修改 Quadlet 示例中的不可变 Git SHA 镜像标签、Topic 和手机地址；
5. 首次保持 `BEMFA_BRIDGE_DRY_RUN=true`，只确认云消息能被识别；
6. 用户在现场确认后，再把 dry-run 显式改为 `false` 并逐条实测。

容器不开放端口，也不需要 249 或 Home Assistant 作为生产中转。

## 米家联调清单

1. Android 设置页启用“局域网 API”，授予附近设备和通知权限，复制地址与 Token；手机重启后需打开一次 App，并允许前台常驻、关闭严格省电限制。
2. 确认 188 可以访问手机的 `8791` 端口，再创建两个 Podman secret；先保持 dry-run，只验证 MQTT 订阅和命令识别。
3. 米家 App 进入“我的 → 其他平台设备 → 添加 → 巴法”，登录并授权；Topic 会按 `002` 类型同步为灯。
4. 用户在灯具现场确认后才关闭 dry-run，并分别实测开、关。桥接不回报物理状态，因此米家中的状态查询不能作为灯具真实状态。
