# Focus Context 执行

Context 的版本化历史、针对确定版本的 Run，以及执行结束后可追溯的结算事实。

## Language

**Context**：具有稳定身份、可演化历史与当前版本的工作上下文。现有 Focus 数据中的 task_id 可作为该身份的字段名，不等于执行线程身份。
_Avoid_: 将 Context 当作一个 prompt 字符串。

**Context Revision**：某个 Context 的不可变历史版本，具有明确来源、投影状态与内容身份。
_Avoid_: 用“最新消息列表”代替确定版本。

**Context Revision Ref**：同时指明 Context、Revision、Generation 及其执行历史地址的精确引用。
_Avoid_: 只凭 revision_id 或 checkpoint_id 推断完整身份。

**Authored History**：作者明确编写的定义性内容和输入，来源与角色分别标识。
_Avoid_: 把所有 assistant、tool 或 runtime 内容都归入作者定义。

**Execution History**：某个版本保留的有序实际执行记录，包含模型响应与工具交换。
_Avoid_: 将临时流式片段当作已完成执行记录。

**Semantic View**：依据版本化选择规则形成的语义消费视图，不能自动替代执行输入。
_Avoid_: 把检索用语义投影直接当作完整模型历史。

**Main Run**：针对一个确定 Context Revision 的一次主执行，具有独立身份、输入来源、进展和结果。
_Avoid_: 将 Run 身份等同于 Context、执行线程或运行时检查点。

**Run Settlement**：Run 执行与结果发布之间的结算事实，区分执行结果与 Context 是否成功推进。
_Avoid_: 用“模型调用返回”代替完成结算。

**Superseded**：Run 基于的输入版本已不再是允许推进的 current，已有执行结果不能覆盖新的 current。
_Avoid_: 将执行成功等同于拥有覆盖当前版本的权限。
