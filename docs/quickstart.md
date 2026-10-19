# 快速开始

[文档导航](README.md) · [平台支持](platforms.md) · [客户端接入](clients.md) · [故障排查](troubleshooting.md)

先选择需要的上游，再构建同一个网关。无需同时拥有所有平台账号。

## 1. 准备环境

- Git 与 Go 1.23+。
- 至少一种本人已授权的上游访问方式：

| 使用平台 | 启动前准备 | 启用方式 |
|---|---|---|
| WorkBuddy 国内 / 国际 | 已有桌面登录态，或启动后在控制台首次登录 | 不带配置默认启用两区 |
| OpenRouter | 自己的 `OPENROUTER_API_KEY` | `-platform openrouter` 或平台列表 |
| Cursor | 同一系统用户登录个人 Free 账号，关闭按量付费 | `-platform cursor` 或平台列表 |

完整能力与限制见[平台支持](platforms.md)。下方第 3 步以 WorkBuddy 为例；其他平台构建后按 [OpenRouter 配置](configuration.md#openrouter仅免费模型)或 [Cursor 配置](configuration.md#cursor个人-free-额度)启动，再进行第 4 步验证。

普通网关不需要 Node.js 或前端构建工具。ZCode 同步脚本需要 Python 3.9+；Cursor 在 macOS / Linux 读取桌面登录态需要 Python 3，Windows 使用系统 SQLite。Windows 已验证国际桌面加密凭证；macOS / Linux 可构建网关，但这一凭证路径尚未端到端验证。

## 2. 获取并构建

```powershell
# Windows PowerShell
git clone https://github.com/576469377/Agent2API.git
cd Agent2API
go build -trimpath -o bin/agent2api.exe ./cmd/agent2api
```

```bash
# macOS / Linux
git clone https://github.com/576469377/Agent2API.git
cd Agent2API
go build -trimpath -o bin/agent2api ./cmd/agent2api
```

也可以执行 `go install github.com/576469377/Agent2API/cmd/agent2api@main`，安装默认分支当前代码。需将 Go 的安装目录加入 PATH，后续命令中的 `./bin/agent2api.exe` 或 `./bin/agent2api` 可替换为 `agent2api`。`@latest` 按 Go 模块版本规则选择版本，可能与 `main` 的新功能不同。

## 3. 登录与启动

已有桌面登录态时直接启动。以下使用 Windows 产物；macOS / Linux 将可执行文件换成 `./bin/agent2api`。

```powershell
./bin/agent2api.exe
```

默认监听 `127.0.0.1:8787`，自动集成两个区域。即使从未登录过某个区域，也会保留该平台与空账号池；在「账号管理 → 添加账号」中选择国内版或国际版，完成授权后账号自动加入对应账号池。登录前可以使用控制台，模型调用会返回 503，直到加入可用账号。

也可以使用命令行登录，完成输出链接中的授权：

```powershell
./bin/agent2api.exe login -region cn
./bin/agent2api.exe login -region global
```

只启用某一区域：

```powershell
./bin/agent2api.exe -platform workbuddy
# 或
./bin/agent2api.exe -platform workbuddy-global
```

国际桌面客户端安装在自定义位置时，可设置 `AGENT2API_WORKBUDDY_EXECUTABLE` 为其完整可执行文件路径，见[国际版说明](research/04-WorkBuddy国际版接入.md)。该变量用于本机加密凭证读取，不是上游 API 地址。

## 4. 验证实际回复

1. 打开 [http://127.0.0.1:8787](http://127.0.0.1:8787)。
2. 在「平台连接」确认期望的平台存在。
3. 从「模型目录」复制完整 ID，例如 `workbuddy-global/模型名`。
4. 在「模型测试」发送一条短消息，确认真实回复。
5. 按[客户端接入](clients.md)配置 ZCode 或其他客户端。

Windows 可用 `Invoke-RestMethod http://127.0.0.1:8787/health` 检查进程连接；macOS / Linux 可用 `curl http://127.0.0.1:8787/health`。**健康检查不调用上游**，返回 `ok` 不代表账号有额度或所有模型可用。

另开终端执行 `./bin/agent2api.exe models` 可列出当前账号目录；`models -platform workbuddy-global` 只查看国际版。

## 5. 使用本地配置

需要模型别名、明确平台顺序或访问密钥时，首次创建本地配置：

```powershell
# Windows；已有 config.json 时不要覆盖
Copy-Item config.example.json config.json
./bin/agent2api.exe -config config.json
```

macOS / Linux 使用 `cp config.example.json config.json`，再执行 `./bin/agent2api -config config.json`。示例不包含账号凭证，包含 WorkBuddy 两区、OpenRouter、Cursor 四个平台。删除不使用的平台；保留 OpenRouter 时配置自己的密钥，保留 Cursor 时准备桌面 Free 登录态。按需修改别名指向目录中的真实模型。

配置项见[配置与模型路由](configuration.md)。启动命令没有 `-config` 时，不会自动加载当前目录的 `config.json`。更改配置文件通常需要下次启动时读取；控制台只热更新明确支持的项目。

## 已有网关正在运行

不要再启动一个相同端口的网关。更新前端可以运行[独立控制台](console.md#不重启正在运行的网关)，默认使用 `8788` 并连接现有 `8787`。客户端继续使用原网关地址。

更新源码和编译产物不会修改已经运行进程中的嵌入资源。网关自身升级需要安排重启；单独启动控制台可先使用新界面。
