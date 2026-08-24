# macOS 发布签名

GitHub Actions 使用固定的自签名代码签名证书
`AliyunLLMProxy-Release.pem`。SHA-256 指纹为：

```text
9933920ab5b4208e62bab6260afc8360632524d87bd90d8fe996ab272f286256
```

加密后的 PKCS#12 文件和密码分别保存在仓库的
`MACOS_SIGNING_CERTIFICATE_P12` 与
`MACOS_SIGNING_CERTIFICATE_PASSWORD` Secrets 中，私钥不得提交。

CI 会拒绝缺少签名 Secret、证书与公钥不匹配、ad-hoc 签名以及包含
不稳定代码哈希 designated requirement 的发布包。固定证书、Bundle ID 和
designated requirement 让后续构建保持同一个代码身份，减少覆盖安装后再次授予
本地网络或防火墙权限的情况。

该证书不是 Apple Developer ID，不能用于 Apple 公证，也不能消除一台新 Mac
首次打开下载应用时的 Gatekeeper 提示。
