# Focus-Go

独立的 Focus Go 原型仓库，当前处于设计评审阶段，尚未实现运行程序。

第一阶段目标：固定版本的 Context → Agent Run → 新的 Context Revision。

本项目使用独立的 Go 数据与检查点格式，通过产品适配层连接 go-forest-agent。现有 Python [Focus](https://github.com/Leeminjing/Focus) 保持不变；不迁移 Patrol、Kernel、Coordinator 或桌面前端。

实施基线计划固定为 go-forest v0.3.0 和 go-forest-agent v0.1.0。

Spec/Design 只保存在开发者本地，不进入 Git 历史。获得设计批准后开始实现。
