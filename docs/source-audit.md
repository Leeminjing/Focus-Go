# Focus 行为契约源码核对

日期：2026-10-25。读取的远程 main 固定为 [78df14e](https://github.com/Leeminjing/Focus/commit/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c)。后续引用均锁定该提交，避免混用不同版本。

本次仅做只读研究，没有修改现有 Python Focus。下载的源码副本位于被忽略的 .scratch，不随仓库上传。

| 源码 | 确认的行为 |
| --- | --- |
| [ContextRevisionRef / Contract](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/contexts/evolution/schemas.py) | 引用包含 context_id、revision_id、generation、execution_thread_id、checkpoint_ns、checkpoint_id、payload_mode。checkpoint 模式要求 checkpoint_id；来源有从零连续的顺序且不得重复。 |
| [ContextRevisionRepository](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/contexts/evolution/repository.py) | 按完整 Ref 校验身份；Revision 插入与 current 切换分离；switch_current 使用 expected base CAS，陈旧版本不得覆盖。 |
| [ContextRevisionReader](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/contexts/evolution/reader.py) | authored、execution、display、semantic 是不同视图；存在 V2 history_payload 时以其为权威，执行历史地址单独处理。 |
| [HistoryPayload / FocusItem](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/packages/harness/focus/history/contracts.py) | schema_version=2；来源、作用域和消息角色是不同维度；authored inventory 不允许 runtime 控制或 opaque continuity；原生工具项要求 call_id。 |
| [历史消息 codec](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/packages/harness/focus/history/codec.py) | 保留完整原始记录；_lc 与可见字段不得自相矛盾；类型化项可以保留 provider 数据，不能把旧消息桥误作检查点格式。 |
| [历史选择](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/packages/harness/focus/history/selection.py) | semantic selection 有独立版本；排除控制和 opaque 项，工具项只作证据，不自动成为直接用户事实。 |
| [工具交换协议](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/packages/harness/focus/history/protocol.py) | 调用身份唯一，输出必须对应先前唯一调用，闭合要求显式；中断修复不能推测工具执行成功。 |
| [RunService](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/runs/service.py) | 主 Run 准备时绑定 current Revision 与 checkpoint 地址，保存新输入和执行身份；身份、装备与模型选择由宿主冻结。 |
| [RunAdmissionService](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/runs/orchestration/admission.py) | 幂等请求重放已有 Run；同一 Context 的 Main Run 需检查活跃或未结算状态；准入不创建 Agent。 |
| [RunExecutionAssembler](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/runs/orchestration/assembler.py) | 根据已持久化 Run 重建输入，校验返回 Run identity；不领取调度、不启动执行。当前接口依赖 LangGraph。 |
| [Executor](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/runs/orchestration/executor.py) | 校验宿主治理身份、恢复输入、绑定执行地址后启动运行；当前底层依赖 LangGraph，不能直接替换检查点。 |
| [RunLifecycleFinalizer](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/runs/orchestration/lifecycle.py) | 结算读取最终 checkpoint；原子保存终态、发布合法 Context Revision 和 outbox；base 已过时则 superseded；重复 finalize 返回同一结算事实。 |
| [Revision 历史续接](https://github.com/Leeminjing/Focus/blob/78df14e5d48c853b03de728b8dd8d8b45f2a5a9c/backend/app/desktop/contexts/evolution/history.py) | 保留 execution items；authored 继承旧定义，只追加新的用户输入，不把全部模型或工具输出提升为作者定义。 |

这是现有行为的研究记录。Focus-Go 仍处于本地规格/设计评审阶段，没有 Go 实现，也不宣称已兼容全部 Python 数据或功能。
