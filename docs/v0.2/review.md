# 第二阶段独立审查

日期：2026-10-25。固定比较基点：Focus-Go `61ac50c`，Agent `20698e3`。行为符合性与工程规范由两个独立审查者并行完成；修复后分别复核。

## Spec

初审发现 3 项，均有公开接口复现并已修复：

- Linux 特殊文件先打开后检查，FIFO 可阻塞并忽略取消。现打开前验证末端为常规文件，并在 Linux 使用非阻塞读取；真实 Linux FIFO 回归立即拒绝。
- unknown 阶段 CLI 隐藏 prepared 文件收据。现通过独立只读入口查询阶段、前后字节/hash 和授权，工作区不可用或没有模型凭据也可查看，不自动 Resolve。
- 原生模型装备可用离线实现执行，并以空模型工件数组通过验证。现禁止该装备替换，要求原生记录与模型步骤完整对应；管理装配不能执行 Run。

三项原复现及 App、Workspace、CLI 测试复核通过；未发现剩余实质偏差。

## Standards

初审发现 2 项，修复复核另发现 1 项 guard 回归，均已修复：

- JSON 转义后合法收据超过原 4 MiB 读取上限。现保存/读取采用同一编码预算，写文件前检查最终收据可保存；1 MiB 文本对应的 4,195,642 字节收据可重读。
- SSE headers 之后的配置超时被误报协议错误。现使用明确的请求截止 Context，并保留 body 超时的 DeadlineExceeded 分类。
- 超限日志的 guard 曾返回 nil 错误，隐藏损坏。现 32 MiB+1 字节日志明确返回 ErrCorrupt。

原复现均已转绿，针对性回归各重复 3 次通过；未发现剩余行动项。

## 最终验证

最终完整测试、vet、Linux race 及远程 CI 证据见 [validation.md](validation.md)。真实模型验收为用户指定的 DeepSeek Responses 兼容端点，不冒充 OpenAI 托管验收。没有修改 Forest Core、Python Focus，或提交私有 Spec/Design、配置及 scratch。
