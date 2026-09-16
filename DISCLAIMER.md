# 免责声明

> 最后更新：2026-09-16
> 适用范围：Agent2API 项目的全部源码、文档、配置示例与二进制产物

**请在使用本项目前完整阅读本声明。下载、编译、部署或以任何方式使用本项目，即表示你已知悉并接受以下全部条款。**

---

## 一、合规使用边界

### 1. 仅限你本人已授权的账号

本项目的工作原理是：读取**你本机 WorkBuddy / CodeBuddy 桌面客户端已登录的凭证**，将其复用到本机其他 LLM 客户端。

因此你必须确保：

- 只使用**你本人拥有合法使用权**的账号；
- **不得**使用他人账号、共享账号、租用账号，或任何你无权访问的凭证；
- **不得**将本项目用于绕过任何访问控制或权限校验。

### 2. 仅限本机或你控制的私有环境

本项目默认仅监听 `127.0.0.1`（只有本机能访问）。

如果你修改为监听 `0.0.0.0`，或将服务部署到可被他人访问的环境，**你必须自行配置访问控制**（至少设置 `-api-key`，并在前置反向代理上做鉴权）。因未设鉴权导致账号额度被他人消耗的，责任由部署者承担。

**不得**将本项目作为公网服务、多租户服务或对外 API 开放。

### 3. 遵守上游服务条款

本项目的运作方式（复用桌面客户端凭证、直接调用上游私有接口）**可能不符合**上游平台的服务条款。

你有责任在使用前自行阅读并遵守上游平台的用户协议、订阅条款、用量政策与开发者规范。**是否使用本项目，由你自行判断并承担全部后果。**

### 4. 遵守当地法律法规

你应确保使用行为符合你所在国家/地区的法律法规、行业规定，以及你所属组织的信息安全政策。

### 5. 不得用于未授权访问或商业转售

明确禁止：

- 将本项目用于绕过访问控制、权限校验或计费；
- 批量账号池运营、代他人调用、账号出租；
- 将上游能力包装为付费服务转售；
- 移除、篡改或规避上游的用量限制与权限限制。

---

## 二、风险自担

### 6. 使用风险由你自行承担

**使用本项目的全部风险由使用者自行承担**，包括但不限于：

- 上游账号被限速、降级、**暂停或封禁**；
- 订阅、额度、积分被消耗或失效；
- 本机凭证文件被读取、复制或泄露；
- 请求内容被上游记录、审核或留存；
- 因上游协议变更导致的功能失效或数据异常。

### 7. 上游协议可能随时变更

上游为**私有协议，无任何稳定性承诺**。上游可能在未提前通知的情况下变更、限制或关闭相关接口，导致本项目部分或全部功能失效。

本项目由个人维护，**不承诺跟进上游变更，也不承诺修复由此产生的问题**。

### 8. 不保证适用性与可用性

本项目按**现状（AS-IS）**提供，不承诺其适用于任何特定目的，不承诺可用性、正确性、完整性或数据准确性。

**已知缺陷清单并不完整**——详见 README 的「已知限制」章节。该章节如实列出了当前代码中已确认存在、尚未修复的问题，请在使用前阅读。

---

## 三、关于「内容脱敏」功能的说明

### 9. 该功能会改写发往上游的文本

本项目默认开启的**内容脱敏**，会在提示词的敏感词中插入零宽字符、并压缩客户端过长的固定模板。

**它的目的是**：让客户端（如 Claude Code / Codex CLI）自带的**固定 system 模板**不被上游关键词审核误判——这些模板里含有大量「拒绝协助攻击、拒绝协助凭证测试」之类的**安全声明用语**，会被关键词机制误伤，导致正常请求被拦截。

**你应当知悉**：

- 该功能**确实会改变**实际发送给上游的文本内容；
- 它**不是**用于规避针对违法、有害内容的审核的工具，也**不应**被如此使用；
- 是否开启由你决定（`-no-sanitize` 可关闭）；
- 该功能可能与上游服务条款冲突，**由此产生的一切后果由使用者承担**。

---

## 四、责任限制

### 10. 作者不承担责任

在适用法律允许的最大范围内，本项目作者与贡献者**不对因使用或无法使用本项目而产生的任何直接、间接、附带、特殊或后果性损失承担责任**，包括但不限于账号损失、额度损失、数据丢失、业务中断或第三方索赔。

---

## English Summary

Agent2API is a **local-only** reverse proxy that reuses the credential of the WorkBuddy / CodeBuddy desktop client already logged in on your own machine.

- **Use it only with accounts you are authorized to use**, and **only on your own machine or a private deployment you control.** Do not expose it publicly, do not use it as a multi-tenant service, and do not use it to bypass access controls or billing.
- **Its operation may conflict with the upstream platform's Terms of Service.** You are responsible for reading and complying with those terms and with your local law.
- It must not be used for unauthorized access, account pooling, or commercial resale.
- The upstream protocol is private and **may change without notice**; no guarantee of functionality, availability, or correctness is given. The known-issues list in the README is **not exhaustive**.
- The default content-sanitization feature **does modify the text sent upstream** — its purpose is to avoid false-positive keyword moderation on client boilerplate, **not** to evade review of unlawful or harmful content.
- **You bear all risk**, including account suspension, credit loss, and credential exposure. The authors accept no liability.

*This summary is provided for convenience. The Chinese text above is the governing version.*

---

## 相关文档

- [README.md](README.md) — 项目说明、安全须知与已知限制
- [SECURITY.md](SECURITY.md) — 漏洞报告方式与威胁模型
