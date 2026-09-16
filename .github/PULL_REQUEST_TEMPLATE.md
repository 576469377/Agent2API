## 变更说明

<!-- 这个 PR 做了什么？为什么？ -->

## 关联 Issue

<!-- 如 Fixes #123 / Closes #123 -->

## 自查清单

- [ ] `make check` 通过（fmt + vet + test）
- [ ] 涉及协议转换的改动已补/更新测试（断言 **IR 事件序列**，参考 `sse_test.go` 的 `replayFixture`）
- [ ] **未提交任何凭证**：`config.json` / `*.session.json` / `auths/` / `*.info` / `agent2api-metrics.json` / `.workbuddy/`
- [ ] 未用 `git add -f` 绕过 `.gitignore`
- [ ] 若更新了 `fixtures/`，已做**脱敏处理**（上游 ID、时间戳），且 `go test ./internal/adapter/workbuddy/ -run TestReplay` 通过

## 测试方式

<!-- 你是怎么验证这个改动有效的？贴出命令或步骤 -->

## 补充说明

<!-- 截图、权衡取舍、已知不足等 -->
