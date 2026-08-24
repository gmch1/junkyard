# macOS 应用

这是 `aliyun-llm-proxy` 的原生 macOS 管理应用。Swift AppKit 前端负责配置和状态展示，应用包内的静态 Go 后端负责局域网 HTTP 服务，不依赖系统 Python 或 Docker。

## 功能

- 保存并热加载阿里云百炼 DashScope API Key。
- 一键启动和停止后台代理。
- 展示并复制局域网 Base URL、客户端 API Key 和模型名称。
- 提供 `POST /v1/chat/completions`、`GET /v1/models` 和带鉴权的状态接口。
- 上游模型限流或暂时故障时，在响应开始前自动尝试下一个模型。
- 配置和 Key 保存在 `~/Library/Application Support/AliyunLLMProxy/`，文件权限为 `0600`。

关闭管理窗口不会自动停止已经启动的代理；需要停止时请先点击“停止服务”。

## 构建

在安装了 Xcode Command Line Tools 和 Go 1.22+ 的 Mac 上运行：

```bash
./macos-app/build.sh
```

产物为 `macos-app/build/AliyunLLMProxy-macOS-universal.zip`，同时支持 Apple Silicon 和 Intel Mac。GitHub Actions 也会构建并上传同名 Artifact。

CI 产物使用 ad-hoc 签名，不包含付费 Apple Developer 证书和公证票据。首次打开下载的应用时，请在 Finder 中右键应用并选择“打开”。应用运行时不需要安装 Go、Python 或 Docker。
