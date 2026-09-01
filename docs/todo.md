# gobco 工作项（分类视图）

> 决议完整内容见 `docs/adr/`，本文件是分类索引 + 任务清单。
> 格式: `| 编号 | 标题 | 描述 | 涉及方/工作量 |`

## ✅ 已决策

| 编号 | 决议 | 摘要 | 来源 |
|------|------|------|------|
| ADR-0001-D1 | 变异插入机制 | `go test -overlay`，每 mutant 单文件映射，不拷贝整模块 | ADR-0001 |
| ADR-0001-D2 | 变异生成 | AST 定位 + 最小文本手术（非 go/printer 重排），两阶段（扫描→轻量 ID，执行前生成变异文件） | ADR-0001 |
| ADR-0001-D3 | 测试选择 | coverage-based：基线全量 coverprofile 筛 NO_COVERAGE + per-test coverprofile 建"行→测试"映射，只跑覆盖测试，`-failfast` 提前退出 | ADR-0001 |
| ADR-0001-D4 | 算子集 v1 | ConditionalsBoundary / NegateConditionals / InvertNegs / BooleanSwap / Math / Increments / Constant / ReturnVals（类型感知，type-check 失败降级纯语法） | ADR-0001 |
| ADR-0001-D5 | 结果分类 | KILLED/SURVIVED/NO_COVERAGE/TIMED_OUT/COMPILE_ERROR/RUN_ERROR；detected=KILLED+TIMED_OUT；主分数 = detected/(total-NO_COVERAGE) | ADR-0001 |
| ADR-0001-D6 | 执行模型 | 每 mutant 一个 go test 子进程，setpgid + kill(-pgid) 超时终止，并发 -p，-failfast | ADR-0001 |
| ADR-0001-D7 | 缓存 | 源+测试+算子集+go版本 hash → .gomut-cache/，-no-cache 关闭 | ADR-0001 |
| ADR-0001-D8 | 项目结构 | 嵌套独立 module `gomut/`（github.com/oliveagle/gomut），cmd→engine→{mutate,cover,exec,report} 单向依赖，纯 stdlib | ADR-0001 |
| ADR-0001-D9 | CLI | flags 与退出码约定（见 ADR-0001 §5-D9） | ADR-0001 |
| ADR-0001-D10 | 范围 | v1 仅 go.mod 模块、只变异生产代码、无 HTML 报告 | ADR-0001 |
| SUB-1 | pitest submodule | `submodules/pitest` @ tag 1.25.9 (23f22e12)，URL 用 SSH（本机 https TLS 受限） | 2026-08-26 |

## 🚧 改造中

| 编号 | 任务 | 描述 | 工作量 |
|------|------|------|--------|
| R-1 | gomut v1 实现 | 按 ADR-0001 搭建 `gomut/` module 全部分层 | 本会话 |

## 📋 待开发

| 编号 | 任务 | 描述 | 依赖 |
|------|------|------|------|
| T-1 | internal/mutant 模型 | Mutant 记录（算子/文件/行列/描述/变异片段）+ Operator 接口 + 状态枚举 | — |
| T-2 | internal/mutate 算子 | 8 个算子的 AST 遍历 + 文本手术 + type-check 降级 + 未用变量修复 | T-1 |
| T-3 | internal/cover 覆盖选择 | 基线 coverprofile 解析 + per-test coverprofile + 行→测试映射 | T-1 |
| T-4 | internal/exec 执行 | overlay 生成 + go test 子进程（setpgid/超时/-failfast）+ 结果分类 | T-1 |
| T-5 | internal/engine 编排 | 扫描→基线→选择→并行执行→汇总，缓存键计算 | T-2/3/4 |
| T-6 | internal/report 输出 | text（对齐 gobco 风格）+ json，分数表 + 存活 mutant 明细 | T-1 |
| T-7 | cmd/gomut CLI | flags/退出码/进度打印 | T-5 | **DONE 2026-09-01** — 6 CLI 测试通过；go build/vet/gofmt 全绿
| T-8 | 单元测试 | 算子 table-driven（每算子边界 case）+ cover 解析 + exec 分类 | T-2/3/4 |
| T-9 | 集成测试 | testdata 内小型模块端到端：已知 mutant 的杀/活断言 | T-5 |
| T-10 | README + 示例 | gomut/README.md（安装/用法/输出解释/与 gobco 的关系） | T-7 | **DONE 2026-09-01**
| T-11 | HTML 报告 | v2：对齐 pitest-html-report（按文件/包/算子钻取 + 变异 diff 高亮） | T-6 |
| T-12 | 测试代码变异 | -cover-test 的完整实现（变异 _test.go 需同时处理 test 文件的覆盖语义） | T-2 |

## 💬 待讨论

| 编号 | 议题 | 备注 |
|------|------|------|
| D-1 | mutant schemata 优化 | 一次编译多 mutant 开关（ADR-0001 方案 C），大项目性能优化方向，v2 再议 |
| D-2 | 等价 mutant（EQUIVALENT）判定 | pitest 1.25.1 引入；Go 侧可先人工标记，自动判定需程序语义分析，v2+ |
| D-3 | 多 mutant 共享 go test 进程 | 对齐 pitest "一个 minion 多个 mutant"，Go 的 overlay 每次变一个文件，共享进程需 -overlay 动态切换，可行性待验证 |
| D-4 | 与 gobco 合并输出 | 分支覆盖 + 变异分数合并成一份"测试质量报告"，CLI 层面聚合 |
| D-5 | CI 集成示例 | GitHub Action / 与现有 .travis.yml 的互操作 |

## 备注

- 本仓库当前有未提交改动（main.go / main_test.go / util.go，gobco 的 /... 递归包支持，owner 本地开发中）——**不在本次 ADR 范围内**，勿动。
- 网络环境：github https TLS 受限，git 操作用 SSH（`git@github.com:`）；curl 可用。

## ✅ gomut v1 完成

| 范围 | T-1..T-10（internal 五层 + cmd/gomut CLI + 单测/集成测试 + README） |
|------|------|
| 完成日期 | 2026-09-01 |
| 验证 | `go build ./...` 通过；`gofmt -l` 空；`go vet ./...` 干净；
`go test -count=1 ./...` 全绿（含 `-short` 之外的 TestEndToEnd 端到端杀/活断言，main=87.5%）；
`./gomut` 对 testdata/sample 端到端运行正常 |
| 未做（v2） | T-11 HTML 报告；T-12 `-cover-test` 变异测试代码 |

