# smart-log
***smart-log 是一个为 Kubernetes 设计的轻量级、云原生的实时日志告警系统。***

## 🌟 项目理念
***传统的日志解决方案（如 ELK, Loki）功能强大，但通常过于庞大、资源消耗高且配置复杂。smart-log 旨在解决一个非常具体且常见的痛点：我只想在我的应用打印出某条特定错误日志时，立即收到通知。***

***为此，smart-log 遵循以下设计哲学：***

- 🚫 无日志存储: 它不汇集、不存储任何日志。这使得它极为轻量，资源占用极低，并且没有持续的存储成本。
- ⚡️ 实时监听: 它直接通过 Kubernetes API 实时监听 Pod 的标准输出流 (stdout/stderr)，实现真正的实时告警。
- ☁️ 云原生设计: 所有配置（监控目标、匹配规则、告警渠道）均通过 CRD (Custom Resource Definitions) 以声明方式定义，完美融入 GitOps 和 IaC (Infrastructure as Code) 的工作流。
- 🎯 精准告警: 通过强大的正则表达式匹配和灵活的告警模板，实现高度定制化的、精准的告警通知。

## ✨ 核心功能
- [X] 实时日志监控: 实时监听指定 Pod 的标准输出日志。
- [X] 正则匹配规则: 基于 Go 的正则表达式，精准匹配你关心的任何日志内容。
- [X] 声明式配置: 通过 MonitorPod, Alert, AlertGroup 三种 CRD 来声明你的监控和告警需求。
- [X] 灵活的告警渠道: 目前支持通用的 Webhook 渠道，可轻松与企业微信、钉钉、飞书或任何自定义系统集成。
- [X] 告警组: 通过 AlertGroup 将多个告警渠道聚合，实现一次触发、多方通知。
- [X] 告警风暴抑制: 内置强大的频率限制功能（rateLimit），通过“漏桶”算法平滑告警速率，有效防止因日志风暴而打爆下游服务。
- [X] 模板优先级: 支持在 MonitorPod（具体事件）和 Alert（通用渠道）两个层级定义告警模板，并拥有清晰的覆盖规则。

## 🏗️ 架构概览
***smart-log 遵循标准的 Kubernetes Operator 模式，包含三个核心控制器：***
- 配置层 (Configuration Layer)
  - Alert Controller: 负责验证单个告警渠道（Alert 资源）的配置是否有效，并更新其状态。
  - AlertGroup Controller: 负责验证告警组（AlertGroup 资源）的成员是否存在且可用，并更新其状态。
- 执行层 (Execution Layer)-
  - MonitorPod Controller: 这是系统的核心引擎。它负责监控 MonitorPod 资源，根据其 selector 找到目标 Pods 并监听日志流。当日志匹配到规则时，它负责执行完整的告警派发流程，包括频率限制、模板渲染，并最终使用 Alert/AlertGroup 的配置将告警发送出去。

## 🛠️ 快速开始
[快速开始](./quick-start/README.md)

## 🔮 未来计划
- [ ] 更多告警渠道: 支持邮件、企业微信、飞书、钉钉等。
- [ ] 多行日志处理: 支持 Java 堆栈跟踪等多行日志的合并与匹配。
- [ ] 告警静默: 支持通过 Silence CRD 临时屏蔽告警。
- [ ] Prometheus Metrics: 暴露 Controller 自身的工作指标。
- [ ] Validation Webhooks: 在创建资源时提供即时配置校验。

## 🤝 贡献
***欢迎任何形式的贡献！请随时提交 Pull Request 或创建 Issue。***

### 📄 许可证
***本项目基于 [Apache License 2.0]***