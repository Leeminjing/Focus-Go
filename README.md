# Focus-Go

独立的 Focus Go 原型仓库，Go 1.23+。第一阶段实现确定版本的 Context → Agent Run → 可验证的新 Context Revision，不依赖 Python 或 LangGraph。

## 运行最小闭环

```sh
go run ./cmd/focus-go demo -data .scratch/demo
```

固定数据包含 `BASE=40` 的历史和本轮 `ADD=2`，确定性模型从实际消息中读取参数，通过普通 Tool 计算，再由模型返回 42。输出 JSON 包含完整输入/输出 Ref、Run ID、执行阶段、`context_publication=published`、事件身份、`answer=42` 和 `verified=true`。新 Revision 的 Generation 为 2，第 0 项来源精确指向输入版本，旧版本不变。

同一命令再次运行会返回原结算，不重复执行工具或生成版本。另一个固定数据使用独立目录：

```sh
go run ./cmd/focus-go demo -data .scratch/variant -fixture testdata/base-7.json -input ADD=5
```

结果为 12。模型与工具中没有硬编码 40、42 或 12。演示是实际 Go Agent 执行循环，使用离线确定性模型，不宣称接入线上模型。

本项目使用独立的 Go 数据与检查点格式，通过产品适配层连接 go-forest-agent。现有 Python [Focus](https://github.com/Leeminjing/Focus) 保持不变；不迁移 Patrol、Kernel、Coordinator 或桌面前端。

实施依赖基线为 [go-forest v0.3.0](https://github.com/Leeminjing/go-forest/releases/tag/v0.3.0) 和 [go-forest-agent v0.1.0](https://github.com/Leeminjing/go-forest-agent/releases/tag/v0.1.0)。

领域术语见 CONTEXT.md；固定源码版本及行为证据见 docs/source-audit.md。

## 组合与版本

Focus Workflow、FocusRepository、HistoryModel 适配层、Agent Runtime、Model、Tool、Checkpoint、Events、Approval 都由 Forest 普通树组合。产品逻辑位于本仓库，两个通用模块未增加 Focus 分支。Agent 的 Create 只接本轮输入；HistoryModel 在每步按持久 Run 绑定装配冻结历史和增量消息，不把历史压成一个 prompt，也不随 current 的改变切换输入。

Context Generation、Forest Generation 和 Agent checkpoint Revision 是不同序列。宿主在 Scope 外驱动 agent.Continue，每步独立进入 Scope；暂停和重启不保留进程内 Scope。

## 数据及结果语义

- 保留完整七字段 Context Revision Ref，authored/execution 分区、来源顺序、run_settled 因果和 current 的 expected-base 检查。
- 新结果保留旧 execution，并追加本轮完整工具闭环；authored 只追加新用户输入。
- current 已变化时，结果仍保存，但结算为 superseded，不覆盖 current，也不重跑 Agent。
- Revision、current、Run Settlement 和 outbox 一起提交。重复结算返回同一事实，事件消费者按稳定身份去重。
- 审批/unknown 保持未结算并阻止同 Context 新 Main Run；失败、取消或超时不发布成功 Revision。
- Recover/Resolve/Decide 仅面向可信宿主，恢复必须先确认旧执行者停止，不自动推断工具结果或重放工具。

存储采用本地不可变 Catalog 修订和结果快照，以文件 fsync 后原子硬链接发布，要求支持硬链接的文件系统。最高记录或被引用快照损坏会报错，不回退旧状态。支持实际进程终止恢复，不承诺 Windows 断电持久性、跨存储全局事务或分布式租约；数据目录按可信本地宿主管理，不提供加密或用户沙箱。

## 支持范围

首阶段仅支持基础文本和来源可确认、已闭合的普通工具记录；JSON 数值为 ±(2^53-1) 内整数。浮点数、未知字段/版本、重复键、非法 UTF-8/Unicode、不可表达的历史类型和 Python checkpoint 后端会明确拒绝，不静默丢弃或转换。

HistoryPayload V2 与执行器私有检查点是不同数据。Python _lc/opaque continuation、图片、自定义工具协议、复杂指令或治理项不直接运行；手写工具示例不提升为真实执行证明。哈希对支持集合匹配 Python canonical JSON，静态 golden 覆盖 Unicode 与特殊字符。不声称完整兼容任意 Python Focus 数据。

## 验证

```sh
go vet ./...
go test ./... -timeout 90s
go test -race ./... -count=3 -timeout 120s
```

测试覆盖完整闭环、变体、精确 Ref、历史输入、已闭合工具证据不重放、审批/unknown/取消/失败、并发 Context、幂等/冲突、存储损坏及四个真实进程终止恢复窗口。

Spec/Design 已获用户批准，仅保存在开发者本地，由 .gitignore 排除，不进入 Git 历史；.scratch 也不上传。
