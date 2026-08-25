# macOS 菜单栏应用

这是 `aliyun-llm-proxy` 的 macOS 启动器。App 本身只负责菜单栏生命周期、启动统一的 Go 后端和打开默认浏览器；代理、Dashboard API、路由和指标全部来自项目根目录同一份 Go module。

## 功能

- 启动后自动运行代理并打开 `http://127.0.0.1:39281/`。
- 菜单栏“打开管理页面”会使用默认浏览器重新打开 Dashboard。
- Dashboard 配置 DashScope API Key、复制连接信息并展示完整模型列表与统计。
- 后端异常退出时自动恢复；退出 App 时停止后端。
- App 不依赖系统 Python、Go、Node.js 或 Docker。

状态和凭证保存到 `~/Library/Application Support/AliyunLLMProxy/`。管理页与控制接口只允许本机回环访问；局域网只开放需要客户端 Bearer Key 的 OpenAI 兼容接口。

## 构建

仓库包含已构建的 Dashboard 资源。在安装 Xcode Command Line Tools 和 Go 1.22+ 的 Mac 上运行：

```bash
./macos-app/build.sh
```

产物为 `macos-app/build/AliyunLLMProxy-macOS-universal.zip`，同时支持 Apple Silicon 和 Intel Mac。GitHub Actions 会重新构建 Dashboard、执行 Go race test，并构建与验证 Universal App。

发行构建使用仓库固定的长期自签名证书和稳定 designated requirement，覆盖安装时保持相同代码身份。该证书不是 Apple Developer ID，不能进行 Apple 公证；新 Mac 首次打开下载应用时仍需在 Finder 中右键应用并选择“打开”。

签名公钥、固定指纹和 Secret 名称见 [`signing/README.md`](signing/README.md)。
