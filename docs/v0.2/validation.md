# 第二阶段验收记录

日期：2026-10-25。输入基线 Focus-Go `61ac50c`、go-forest-agent `20698e3`；本阶段实现最初提交 `e276133` 与 `8372d3d`，后续修复以最终 main 为准。规格与实现设计已获用户批准，仅本地保存。

## 实际执行

- Windows：两仓库完整测试与 go vet 通过。
- Linux Docker：Agent 使用 Go 1.23，Focus-Go 使用 Go 1.26，各运行 `go test -race ./... -count=3` 通过。
- Focus-Go 的 32 MiB 格式限制、v1 hash golden 与第一阶段回归保留；新增 v2 装备、文件收据及自包含模型工件。
- Forest Core 及 Python Focus 未修改。开发中的临时 go.work 位于被忽略的 .scratch；正式交付依赖以 go.mod 固定发布版本。

CI 对 Windows/Linux 运行 vet、完整测试和原 demo，Linux 额外三轮 race。联网验收不放入无密钥 CI。

## 场景证据

| 范围 | 自动化入口 |
| --- | --- |
| 三工具真实组合及独立 Go 测试 | TestCodingRunUsesThreeTreesAndPublishesIndependentFileEvidence |
| 同工作区独占与冻结装备 | TestFrozenCodingEquipmentExcludesAnotherContextInSameWorkspace |
| 路径、权限、链接、文件版本与重复编辑 | internal/workspace 的 Tool Interface 测试 |
| 批准与预览、陈旧 Agent Revision | TestCodingApprovalIsBoundToPreviewAndAgentRevision |
| 文件副作用崩溃恢复 | TestRealCodingProcessesNeverReplayUnknownOrCompletedFileWrites |
| 原生 Responses、字段顺序及自包含封存恢复 | TestNativeResponsesEvidenceSurvivesCanonicalSealingAndPublicationRetry |
| 无凭据工作区/Context 命令 | TestWorkspaceAndContextCommandsNeedNoModelCredentials |
| unknown、工作区不可用时查询原始收据 | TestUnknownFileEvidenceRemainsReadableWithoutWorkspaceOrCredentials |
| 原生装备不能替换为离线执行 | TestNativeEquipmentCannotBeExecutedWithOfflineModel |
| 转义后大收据、超限损坏、Linux FIFO | Workspace 新增针对性回归 |
| Responses 流、原生续接、失败/取消及真实进程 | go-forest-agent/trees/openairesponses 测试 |

文件进程测试真的终止执行者，在 prepared、file-replaced、receipt-saved、agent-completed 四个边界分别恢复。Prepared 保持 unknown 且零写入；file-replaced 需要操作者核对 postimage；已有收据只提交原结果。已写情况的副作用日志均只有一条。

独立两轴审查发现的问题及修复见 [review.md](review.md)，最终无未解决行动项。

原生封存测试使用不按领域顺序排列的工具参数及带浮点元数据的原生项，证明业务参数按语义比较，供应商原始数据通过独立工件保留。私有 Agent 和 Responses 目录不可用后，只读装配仍可核实和结算既有结果。

## 真实 Responses API 验收

用户指定现有配置并明确确认采用 DeepSeek 的 Responses 兼容端点。本次供应商为 **DeepSeek**，模型 `deepseek-v4-flash`，目标 `https://api.deepseek.com/responses`，不是 OpenAI 托管服务测试。官方兼容行为见 [DeepSeek Responses](https://api-docs.deepseek.com/guides/responses_api/)。没有 Chat Completions 回退。

验收通过真实编译的 focus-go CLI，在独立临时 Go 项目中修复 Add 的减法错误：

```diff
-func Add(a, b int) int { return a - b }
+func Add(a, b int) int { return a + b }
```

保存了 5 个模型步骤、2 次搜索、2 次读取、1 次编辑的证据。生产文件只有这一处修改；go.mod 和 calc_test.go 字节不变。独立宿主执行 `go test ./... -count=1` 通过，模型没有测试执行能力。

完成后以相同请求、无 API key 的进程重复执行，返回同一 Run/Revision/事件和文件内容，无新增模型调用或文件写入。事件、完整收据、原生响应记录和独立测试原始输出仅保存在本地 ignored 验收目录，不上传密钥、用户配置或私有路径。

真实验收也发现了参数规范化边界：原生 JSON 字段顺序不应导致语义相同的封存参数被判不同。修复后使用原 Run 直接结算，没有重跑工具；对应自动化回归现已通过。

## 适用边界

只支持本地可信宿主管理的 Windows/Linux 工作区及已有 UTF-8 文件；不提供 Shell、文件创建/删除/重命名、跨文件事务、跨机器租约或对同权限恶意本地进程的沙箱保证。Windows rename 不被声称有断电原子性。

历史证据校验与现场文件 drift 独立；CLI 的 test_status 仍为 not_run，外部测试通过只属于本验收记录，不把模型回答升级为测试证明。未知写入不自动重试，不推断副作用未发生。
