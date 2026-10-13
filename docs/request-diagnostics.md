# 定位 ZCode 的 400 参数错误

[文档导航](README.md) · [故障排查](troubleshooting.md)

`the request parameters were rejected by the model provider` 表示上游模型供应端拒绝了这次请求，但这句通用错误没有指出具体字段。它不能单独证明某个模型不可用，也不能证明是输出上限、思考参数或历史工具调用导致。

如果问题只在旧对话继续执行、调用工具或切换模型后出现，需要对比失败请求的历史结构和参数。其他机器上短请求成功，不能证明原对话已经修复。控制台里的输入 / 输出 Token 为 0 表示尚未取得用量，不表示请求里没有输入；失败详情的账号为 `—` 也不能据此判断账号没登录。

## 在出错机器取得结构摘要

无需停止已有反代。需要 Python 3.9+，在 Agent2API 项目目录运行：

```powershell
python scripts/diagnose-proxy.py --upstream http://127.0.0.1:8787 --port 8790
```

1. 暂时将 ZCode 对应供应商的 Base URL 改为 `http://127.0.0.1:8790/v1`，保留原 API Key 和协议。
2. 等待客户端配置生效，在原来失败的对话里重试一次。每次只检查一个模型，方便对应摘要。
3. 若仍返回 HTTP 400，终端会输出一行 JSON 结构摘要，可以复制这行和 ZCode 的错误详情进行排查。
4. 完成后，将 Base URL 恢复原值，再关闭诊断程序。

此脚本是临时的本机转发入口。只允许回环地址，转发请求正文和必要请求头，每次请求只发送一次，没有自动重试；流式数据边读边转发。原反代、账号池和凭证文件继续由原进程管理。

摘要只包含已知的数值参数、受限枚举、消息 / 内容块数量及字节数、工具调用配对计数。不会记录模型或会话标识、请求头、密钥、正文、图片、工具名称、工具参数、工具结果正文或思考签名，也不会保存原始请求 / 响应文件。上游错误正文同样不会写入诊断日志。脚本针对 HTTP 错误生成摘要；HTTP 200 流内报错仍以控制台调用详情为准。

## 如何理解摘要

| 字段 | 用途 |
|---|---|
| `max_tokens`、`thinking_type`、`reasoning_effort` | 检查实际请求的输出、思考配置，而非只看客户端模型配置 |
| `message_count`、`roles`、`content_blocks` | 检查旧对话包含多少历史消息和哪些内容类型 |
| `body_bytes`、`text_bytes`、`system_bytes`、`tool_schema_bytes` | 比较失败请求和短请求的体积；**字节数不是 Token 数** |
| `tool_call_count` / `tool_result_count` | 检查调用与结果的数量 |
| `orphan_tool_results`、`unanswered_tool_calls` | 检查缺少前置调用或结果的历史结构 |
| `duplicate_tool_calls`、`duplicate_tool_results`、`missing_tool_ids` | 检查重复或缺失的工具标识 |
| `mixed_tool_result_user_messages` | 检查工具结果和普通用户内容混合的消息 |

这些计数是结构观察，不会据此自动删除历史、填造结果、关闭思考或改写输出预算，也不能直接认定某个计数就是 400 的原因。需要结合出错机器的实际参数、版本以及一次复现来确认。
