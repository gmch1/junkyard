# macOS 应用

这是 `aliyun-llm-proxy` 的 macOS 菜单栏应用。App 本身没有前台主窗口：启动后拉起应用包内的静态 Go 后端并在默认浏览器打开本机管理页，不依赖系统 Python 或 Docker。

## 功能

- 从网页管理页保存并热加载阿里云百炼 DashScope API Key。
- 从菜单栏启动、停止服务或重新打开管理页。
- 从网页或菜单栏复制局域网 Base URL、客户端 API Key 和模型名称。
- 提供 `POST /v1/chat/completions`、`GET /v1/models` 和带鉴权的状态接口。
- 上游模型限流或暂时故障时，在响应开始前自动尝试下一个模型。
- 配置和 Key 保存在 `~/Library/Application Support/AliyunLLMProxy/`，文件权限为 `0600`。

管理页与凭证写入接口仅允许本机回环地址访问，局域网只开放需要客户端 Key 鉴权的 OpenAI 兼容接口。选择菜单栏中的“停止服务并退出”会同时结束后台服务。

## 构建

在安装了 Xcode Command Line Tools 和 Go 1.22+ 的 Mac 上运行：

```bash
./macos-app/build.sh
```

产物为 `macos-app/build/AliyunLLMProxy-macOS-universal.zip`，同时支持 Apple Silicon 和 Intel Mac。GitHub Actions 也会构建并上传同名 Artifact。

GitHub Actions 使用仓库固定的长期自签名证书和稳定的 designated requirement，后续版本保持同一个代码身份，避免每次覆盖安装都被当成完全不同的应用。该证书不是 Apple Developer ID，不能进行 Apple 公证；一台新 Mac 首次打开下载应用时仍需在 Finder 中右键应用并选择“打开”。应用运行时不需要安装 Go、Python 或 Docker。

签名公钥、固定指纹和 Secret 名称见 [`signing/README.md`](signing/README.md)。
