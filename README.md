# Focus-Go

独立的 Focus Go 原型仓库，Go 1.25+。Context → Agent Run → 可验证的新 Context Revision；增加真实 Responses 模型与受治理的代码工作区，不依赖 Python 或 LangGraph。

## 执行代码任务

使用 OpenAI Responses 协议（HTTP/SSE），不回退到 Chat Completions。密钥只从执行进程的 `OPENAI_API_KEY` 读取。模型 ID 必须由宿主提供；以下目录、任务和模型均需换成实际值，状态目录应在代码工作区之外。

```sh
go run ./cmd/focus-go workspace bind -data /tmp/focus-state -id project -root /path/to/project -read . -write calc.go
go run ./cmd/focus-go context create -data /tmp/focus-state -workspace project -id context-1 -text-file context.txt > base.json
go run ./cmd/focus-go run start -data /tmp/focus-state -context context-1 -base-ref base.json -task-file task.txt -key fix-1 -model YOUR_MODEL_ID
```

三个独立 Tool Tree 分别提供搜索、分段读取和精确编辑。编辑需要当前 Run 的读取收据、完整文件 SHA256 和唯一匹配的旧文本。只修改已有 UTF-8 文件，不执行 Shell、创建/删除文件或让模型运行测试。

默认编辑需要逐次审批，`run start` 会输出 Run ID 和暂停状态；通过查询查看调用与版本，再批准：

```sh
go run ./cmd/focus-go run show -data /tmp/focus-state -id RUN_ID
go run ./cmd/focus-go run approve -data /tmp/focus-state -id RUN_ID -call CALL_ID -expected-agent-revision REVISION
go run ./cmd/focus-go run resume -data /tmp/focus-state -id RUN_ID
go run ./cmd/focus-go run evidence -data /tmp/focus-state -id RUN_ID -check-workspace
```

审批命令输出具体编辑预览；旧 Agent Revision 或文件 hash 不符时拒绝。可信宿主可在绑定时使用 `-preapprove-writes`，仅预授权原写范围。读写授权支持精确相对文件名、以 `/` 结尾的目录前缀和 `.` 根范围；deny 优先，不支持任意 glob。`.git`、`.env*`、凭据名、链接、设备路径及歧义路径会被拒绝。授权范围内的代码会发送给所配置模型，请使用可披露的独立工作区。

查询、已结算请求重放、已封存结果的重新结算均无需密钥。执行中的恢复需要相同装备与模型配置。`-endpoint` 只接受宿主明确配置的 Responses 端点，凭据不会自动发送给另一个地址；本次真实验收使用用户授权的 DeepSeek Responses 兼容端点，记录供应商为 DeepSeek。

## 证据与恢复

工作区组合根管理资源，三个工具是其子树；Agent 从外部通过普通 Link 消费能力。模型适配、续接记录、审批、检查点和事件仍为普通树。Forest Core 与 Python Focus 没有产品专用变更。

Run 固定工作区身份、权限、工具声明、模型 profile 和输入版本；同一实体代码工作区不同时接纳两个未结算写作 Run。文件修改和 Context 发布是不同副作用，发布 superseded 或后续执行失败不会自动还原已改文件。

每次编辑先保存意图和前后字节，替换后读回验证，再保存收据；Agent 最后提交其工具结果。真实进程中断后不得盲目重写：

```sh
go run ./cmd/focus-go run recover -data /tmp/focus-state -id RUN_ID -old-executor-stopped
go run ./cmd/focus-go run reconcile -data /tmp/focus-state -id RUN_ID -call CALL_ID -expected-agent-revision REVISION
```

只有完整收据可直接核实；仅有 prepared 意图时保持 unknown。可信操作者独立核对现场后，可用 `-confirm-postimage-by OPERATOR_ID` 明确记录人工确认，工具会检查现场确为预期 postimage，并标注 operator_reconciled；它不重新修改文件，也不接受任意字符串冒充工具结果。

`run evidence` 分别输出 Revision 校验、证据校验、文件 drift 和 `test_status=not_run`。模型声称测试通过不能改变该状态；代码测试由独立流程执行。Responses 的原生续接记录单独持久化，完整结果封存后不依赖供应商存储、Agent 私有目录或再调用模型。临时 `model.delta` 仅供展示；`run events -after N [-follow]` 从检查点读取持久事件。

文件工具目前支持 Windows/Linux，采用 `os.Root` 限制目录访问。版本校验与独占针对本数据目录管理的编辑者，不提供抵御同权限恶意进程、管理员挂载或任意外部编辑者并发写入的文件系统 CAS。非 Unix rename 不被描述为断电原子操作；不确定结果保留为 unknown。新 v2 数据可与 v1 记录共存，旧数据不原地重写；旧二进制不保证能读取新数据。

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

固定依赖 [go-forest v0.3.0](https://github.com/Leeminjing/go-forest/releases/tag/v0.3.0) 和 [go-forest-agent v0.2.0](https://github.com/Leeminjing/go-forest-agent/releases/tag/v0.2.0)，没有本地 replace 或 @main。原第一阶段基线使用的 v0.1.0 标签保持不变。

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
- 已封存的产品快照经完整校验后可以独立结算；未封存的既有执行丢失 Agent 检查点时明确报错，不重新创建并重放工具。

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

逐项验收与独立审查结果见 [validation.md](docs/validation.md) 和 [review.md](docs/review.md)。

Spec/Design 已获用户批准，仅保存在开发者本地，由 .gitignore 排除，不进入 Git 历史；.scratch 也不上传。
