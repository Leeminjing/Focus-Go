# 第一阶段交付验收

日期：2026-10-25。依据用户已批准、仅本地保存的行为与实现基线逐项验收。公开记录只保存结果、测试入口和适用边界，不发布私有文档。

## 执行结果

- Windows 本机：`go vet ./...`、`go test ./... -count=1 -timeout 90s` 通过。
- Linux Go 1.23 Docker：`go test -race ./... -count=3 -timeout 120s` 通过，包含真实子进程终止与恢复测试。
- 实际 CLI：`BASE=40 / ADD=2` 得到 `answer=42`、`verified=true`、`published`、Generation 2；同一目录重复命令返回相同 Run、Revision、事件身份。
- 独立数据目录的 `BASE=7 / ADD=5` 得到 12，验证历史参与实际模型请求与工具计算。
- 依赖为正式版本 `go-forest v0.3.0`、`go-forest-agent v0.1.0`，没有本地 replace 或新增第三方运行库。

CI 配置在 `.github/workflows/ci.yml`，在 Windows/Linux 执行 vet、完整测试和 CLI，Linux 额外运行三轮 race。远程执行记录见仓库 [Actions](https://github.com/Leeminjing/Focus-Go/actions)。

## 行为与测试对应

| 验收项 | 可检查的证据 |
| --- | --- |
| 精确读取 Context Revision，不回退 current | `TestBadContractsAndAnyReferenceMismatchFailBeforeTools`；`TestLatestCatalogOrReferencedSnapshotCorruptionDoesNotFallBack` |
| Main Run 准入、幂等及同 Context 排他 | `TestRepositoryBindsExactRevisionAndAdmitsOneMainRun`；`TestWorkspaceAndIdempotencyKeyRemainDistinctWithEmbeddedSeparators` |
| 冻结历史真正参与执行 | `TestContextRunPublishesVerifiableRevisionAndReplaysSettlement`；`TestClosedHistoricalToolEvidenceIsInputAndCannotBeReplayedOrReused` |
| 新 Revision、来源、authored/execution、hash 与结算一致 | `TestContextRunPublishesVerifiableRevisionAndReplaysSettlement` |
| current 改变后保留结果并 superseded，不覆盖 | `TestFrozenHistorySurvivesCurrentChangeWithoutOverwritingNewRevision` |
| 重复结算不重跑工具、不新增事件或版本 | 完整闭环及发布恢复测试；CLI 重复命令测试 |
| 审批、unknown、失败、取消、超时不发布成功版本 | `TestApprovalAndUnknownRemainUnsettledAndNeverPublishSuccess`；`TestFailureCancellationAndTimeoutDoNotAdvanceContext` |
| 发布中断与独立结果恢复 | `TestPublicationFailureAfterToolCompletionResumesWithoutReexecution`；`TestMissingAgentStorageNeverReplaysCompletedTools` |
| Go/Python 检查点边界及严格 JSON | fixture、canonical、负面契约测试；独立 Python 生成的 hash golden |
| 可观察的版本、Run、结算与结果 | `TestDemoCommandProducesAndReusesARealContextRevision`；App 查询 API |
| 并发 Context 隔离 | `TestConcurrentContextsAreIsolatedAndDuplicateAdmissionIsSafe`；Repository 并发准入测试 |

## 真实进程恢复

`TestRealProcessPublicationBoundariesRecoverWithoutDuplicateEffects` 真正启动测试进程，等待持久边界后强制终止，再启动另一进程恢复。四个边界为：准入后 Agent 尚未创建；Agent 已完成但未封存；结果已封存但未提交 Catalog；Catalog 已提交但未响应。

每次恢复检查：工具副作用日志恰好一条、两个 Context Revision、一个 Run、一个结算事件，输出 42 且可验证。测试没有使用模拟网络或用返回错误代替这些进程终止。

额外数据丢失回归：已封存结果可在 Agent 私有目录不可用时完成校验和结算；未封存且已经观察到 Agent checkpoint 的 Run 遇到记录丢失时明确失败，不重新 Create 或重放工具。

## 交付边界

验收限于基础文本、安全整数及来源正确、已闭合的普通工具历史。模型为可替换的离线确定性实现，没有线上模型供应商适配器。产品当前在创建应用或重启时选择同 profile 实现，没有运行中模型替换 API。

本地文件存储要求原子硬链接；支持上述进程崩溃恢复，不承诺 Windows 断电耐久性、跨存储全局事务、分布式执行租约或任意 Python Focus 数据兼容。unknown 工具结果仍须可信宿主显式核实，不能通过自动重试推断副作用。

Python Focus、Forest Core、已发布 Agent Runtime 均未修改。Spec/Design 和 `.scratch/` 被忽略，未加入本仓库任何可达 Git 历史。
