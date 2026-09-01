# ADR-0001: Golang Mutation Testing 工具（gomut）架构决策

- 状态: 已决策
- 日期: 2026-08-26
- 决策者: oliveagle (owner) + agent
- 参考: `submodules/pitest` @ tag 1.25.9 (commit 23f22e12, hcoles/pitest)
- 参考文档: `submodules/pitest/so_you_want_to_build_mutation_testing_system.md`（pitest 作者写给"为其他语言实现 mutation testing 系统"的人的经验总结）

## 1. 背景

- gobco（本仓库）测量 Go 代码的**条件/分支覆盖**：某条件被测试执行了多少次、真假两侧是否都走到。
- 覆盖度只回答"代码被执行了吗"，不回答"测试真的能抓住 bug 吗"。Mutation testing 是更严格的度量：**给代码注入细小变异（mutant），看测试套件能否杀死它**。
- pitest 是 JVM 生态工业级的 mutation testing 工具（Maven/Gradle 插件、HTML 报告、CI 集成），其设计沉淀在 `so_you_want_to_build_mutation_testing_system.md` 中。
- 目标：借鉴 pitest 的思路，构建 Go 版 mutation testing 工具 **gomut**，与 gobco 形成互补（gobco=分支覆盖，gomut=变异测试）。

## 2. pitest 思路摘要（学习所得）

| 主题 | pitest 的做法 | 要点 |
|------|--------------|------|
| 总体架构 | 主进程分析 + minion 子进程执行 | 被测代码永不加载进主进程；子进程互相隔离状态 |
| 变异生成 | 两阶段：主进程扫描 bytecode 生成**轻量变异 ID 列表**（算子+类/方法+指令索引）；minion 内再生成 bytecode | ID 列表内存占用极小，可容纳大型项目 |
| 变异插入 | JVM instrumentation API 热插 | 不落盘 IO、一个 minion 可分析多个 mutant |
| 测试选择 | **coverage-based**：先插探针跑全量测试 → 建立"行→测试"映射 → 每个 mutant 只跑覆盖它的测试 | 比命名约定/静态分析高效且无需约定（pitest 试过别的方案，coverage-based 胜出） |
| 死循环/挂起 | 基于耗时（正常耗时×因子+fudge）kill 子进程；JVM 无法可靠 kill 线程，kill 进程是唯一可靠手段 | 独立进程同时保证状态隔离 |
| 提前退出 | 一个测试失败即停止该 mutant 的后续测试 | ~50% 性能提升 |
| 测试切分 | 把测试拆到最小可执行单元（单个 test method） | 避免"一个类全跑" |
| 结果分类 | KILLED / SURVIVED / TIMED_OUT / NON_VIABLE / MEMORY_ERROR / NO_COVERAGE / EQUIVALENT / RUN_ERROR | `detected` 布尔位决定计分 |
| 分数 | `mutationScore = totalDetected / totalMutations`，按 mutator 分组统计 | NO_COVERAGE 计入分母（覆盖率低的惩罚） |
| 算子 | ConditionalsBoundary / Increments / InvertNegs / Math / NegateConditionals / VoidMethodCall / Returns(Boolean/Primitive/Null/Empty) / RemoveConditional / InlineConstant / MethodCall | 默认集 = InvertNegs+Math+VoidMethodCall+NegateConditionals+ConditionalsBoundary+Increments+Returns 组 |
| 反模式 | bytecode 级变异产生 **junk mutation**（映射不到程序员真实会犯的错） | 文档明确：对非 JVM 语言，**AST 方式更可取**（可用 diff 解释、无 junk） |

## 3. Go 生态约束分析（与 JVM 的差异）

| 约束 | 影响 | 结论 |
|------|------|------|
| Go 无稳定 bytecode、无运行时 instrumentation API | pitest 的"主进程+minion+热插"不能直接照搬 | 必须换插入机制 |
| `go build -overlay` / `go test -overlay`（Go 1.16+） | 可在不改动磁盘源码的前提下，把某个文件的构建内容替换为内存/临时文件 | **overlay = Go 版的 instrumentation 插入**，已实验验证：`go test -overlay=map.json .` 能正确跑变异代码（变异被杀/存活） |
| `go test -run '^T$' -coverprofile` | 可拿到**单个测试**的 statement 级覆盖 | 支持 pitest 式 coverage-based 测试选择；profile 路径 = 包 import path + 文件名（已验证） |
| `go/types` + `importer.Default()`（gc export data，模块模式，先 `go build`） | 可拿到类型信息 | 支持"类型感知算子"（ReturnVals 需要知道返回类型给 0/""/nil/T{}）；已实验验证 |
| `go test -failfast` | 一个测试失败即停 | 内置 pitest 式提前退出 |
| `go test -timeout` + 子进程 kill | 挂起/死循环检测 | 用 `exec.CommandContext` + 进程组 kill（Go 子进程树需要 setpgid + kill(-pid)） |
| 编译快、build cache 强 | "每个 mutant 一个 go test 子进程"的启动成本可接受 | 不需要 pitest 的"一个 minion 多个 mutant"优化；**简单性优先**（pitest 文档说 naive 方案在 JVM 上不可行只因 JVM 启动慢，Go 没有这个问题） |
| Go 是静态类型 + 编译期严格 | 变异若破坏类型/未用变量 → 编译失败 | 需要"变异修复"（如 `return 0` 导致变量未用 → 自动补 `_ = x`）或把编译失败的 mutant 记为 COMPILE_ERROR 并剔除 |

## 4. 备选方案

### 方案 A：照搬 pitest（主进程 + 子进程 minion + 字节码级变异）
- 需要给 Go 加 instrumentation → 不存在该机制，需改编译器或走 SSA（`go/ssa`）→ 工程复杂度极高，且 `go/ssa` 变异后仍需重新编译。
- 否决：Go 生态无此基础设施。

### 方案 B：AST 源码级变异 + overlay 执行（pitest 文档推荐的方向）
- 用 `go/parser` + `go/ast` 定位变异点，对源码做**最小文本手术**（不重打印整文件，保持格式），生成变异文件 → overlay 给 `go test`。
- 优点：无 junk mutation（每个变异可用 diff 解释）；复用 Go 工具链（编译/测试/覆盖）；实现边界清晰。
- 缺点：变异点需基于 AST 精确计算偏移；类型感知算子需要 type-check。
- **采纳**（本文档主体）。

### 方案 C：mutant schemata（flag 开关式变异）
- 单二进制内用 bool 开关切换所有 mutant。
- 优点：一次编译、插入便宜。
- 缺点：侵入被测代码、类膨胀、与 pitest 文档一致地"结果类很大"。Go 的测试模型也不适合。
- 否决：v1 不采用（留作未来优化备注）。

### 方案 D：纯"naive"——整模块拷贝到临时目录 + 逐 mutant 编译测试
- pitest 文档中"被放弃的项目大多走的路"。
- 对大仓库拷贝+重编译太慢；但作为**调试/兜底模式**有用。
- 结论：不作为主路径；overlay 为主路径。

## 5. 决策

### D1 — 变异插入机制：`go test -overlay`
每个 mutant：把"原文件 → 变异文件"的映射写进 overlay JSON，`go test -overlay=... <pkg>`。不落盘整模块，不拷贝。

### D2 — 变异生成：AST 定位 + 最小文本手术
- 阶段1（扫描，主进程）：`go/parser` 解析 + 算子遍历，产出**轻量变异记录**（算子 + 文件 + 行/列 + 描述 + 变异片段），内存占用小（对齐 pitest 两阶段设计）。
- 阶段2（执行前）：按记录对源文件做单点文本替换，得到变异文件。
- 不做整文件 `go/printer` 重排（保留原始格式，diff 干净）。

### D3 — 测试选择：coverage-based（忠实移植 pitest 核心）
1. 基线：`go test -covermode=count -coverprofile=cover-all.out <pkgs>`（全量、无变异）→ 得到"行→被任何测试覆盖"，未覆盖行的 mutant 直接记 **NO_COVERAGE**（不跑）。
2. 选择：对每个测试函数 T 跑 `go test -run '^T$' -covermode=count -coverprofile=...` → 建"行→测试集合"映射。
3. 每个 mutant 只跑覆盖其行的测试（`-run '^(T1|T2)$' -failfast`）。
4. 提供 `-all-tests` 开关退回"全量测试"（调试/小项目）。
- per-test coverage 代价：N 个测试 = N 次 go test（有 build cache，秒级）。可接受，且是 pitest 验证过的高效路径。

### D4 — 变异算子集（v1，Go 化移植 pitest 默认集）
| Go 算子名 | 移植自 pitest | 说明 |
|-----------|--------------|------|
| ConditionalsBoundary | ConditionalsBoundaryMutator | `>→>=` `<→<=` `==→!=` `!=→==` |
| NegateConditionals | NegateConditionalsMutator | `if c` / `for c` / `switch c` 条件取反（`c → !c`） |
| InvertNegs | InvertNegsMutator | `!x → x` |
| BooleanSwap | (JVM 层 IFEQ/IFNE 组合) | `&& → \|\|`、`\|\| → &&` |
| Math | MathMutator | `+→-` `-→+` `*→/` `/→*` `%→/`（仅数值类型） |
| Increments | IncrementsMutator | `i++ → i--`、`i-- → i++` |
| Constant | InlineConstantMutator | 整型常量 `n → n+1` |
| ReturnVals | ReturnsMutatorGroup(Primitive/Null/Empty/Boolean) | 按返回类型：数值→`0`、string→`""`、bool→`true/false`、nilable→`nil`、struct→`T{}` |
- 类型感知算子（Math/ReturnVals/Constant）依赖 D2 的 type-check；type-check 失败时自动降级为纯语法算子并告警。
- 每个算子可独立开关（`-mutators`），默认全开。

### D5 — 结果分类（对齐 pitest DetectionStatus，Go 化）
`KILLED` / `SURVIVED` / `NO_COVERAGE` / `TIMED_OUT` / `COMPILE_ERROR`(≈NON_VIABLE，Go 特有：变异导致编译失败) / `RUN_ERROR`(go test 进程异常)。
- `detected`（计入分子）：KILLED、TIMED_OUT。
- NO_COVERAGE、COMPILE_ERROR、RUN_ERROR 计入分母或单独列出（对齐 pitest：NO_COVERAGE 进分母惩罚低覆盖）。
- 分数：`mutationScore = detected / (total - NO_COVERAGE)`（主指标，覆盖率归一）；同时输出 `totalDetected/totalMutations`（pitest 原始口径）与按算子分组表。

### D6 — 执行模型：每 mutant 一个 `go test` 子进程
- `exec.CommandContext` + `syscall.Setpgid`，超时时 `kill(-pgid, SIGKILL)`（终止整个测试子树，含死循环）。
- 并发度 `-p`（默认 = CPU 核数，上限 8），每 worker 独立 overlay + 独立临时变异目录 → 天然状态隔离（对齐 pitest minion 隔离）。
- 提前退出：`-failfast`。
- 超时：`-timeout`（默认 30s/mutant，对齐 pitest 默认 3000ms×10 的保守值）。

### D7 — 缓存
- 对"源文件内容 + 测试文件内容 + 算子集 + go 版本"取 hash 作为缓存键，结果存 `.gomut-cache/`。
- 未变化时跳过重跑（对齐 pitest 的 history/cache 思路，降低 CI 重复成本）。
- `-no-cache` 关闭。

### D8 — 项目结构
- 新建**嵌套 Go module** `gomut/`（`module github.com/oliveagle/gomut`），独立于根 gobco module，避免两个工具互相污染依赖。
- 分层：`cmd/gomut`（CLI 入口）→ `internal/engine`（主循环：扫描→选择→执行→汇总）→ `internal/{mutate,cover,exec,report}`（各自单一职责）。
- 与根模块无 import 依赖（gomut 不 import gobco），纯 stdlib（go/parser, go/ast, go/types, go/importer, encoding/json, os/exec, sync）。

### D9 — CLI
```
gomut [flags] [packages...]
  -p int               并发度（默认 CPU 核数，上限 8）
  -timeout duration    单 mutant 超时（默认 30s）
  -mutators list       算子集（默认全开；-mutators=none 关闭）
  -all-tests           每个 mutant 跑全量测试（不做覆盖选择）
  -threshold int       变异分数低于该值则退出码 2（CI 门禁）
  -report dir          输出目录（默认 .gomut-report/）
  -format list         text,json,html（v1 支持 text,json）
  -no-cache
  -cover-test          也变异 _test.go（默认否，对齐 pitest 默认只测生产代码）
  -v                   打印每个 mutant 明细
```
- 退出码：0=正常；1=参数/环境错误；2=分数低于 threshold。

### D10 — 范围与非目标（v1）
- 非目标：HTML 报告（留 v2，pitest-html-report 单独立项）；Java 式多 minion 复用；mutant schemata；对测试代码的变异（默认关闭）。
- 仅支持 Go 模块（go.mod）。不支持 GOPATH 模式。
- 只变异**生产代码**（非 `_test.go`），默认。

## 6. 约束边界（Constraints）

### 架构隔离约束声明

| 约束 | 本决议的立场 | 说明 |
|------|------------|------|
| 1. 无循环依赖 | ✅ 遵守 | 依赖方向单向：`cmd/gomut → internal/engine → internal/{mutate,cover,exec,report}`；四个 internal 包互不 import，只共享 `internal/mutant`（纯数据模型 + 算子接口，无逻辑） |
| 2. 分层向下依赖 | ✅ 遵守 | CLI 层不直接碰 AST/执行；engine 只做编排不含算子细节；算子（mutate）不感知执行（exec） |
| 3. God package 阈值 | ✅ 遵守 | 每包单一职责：mutate=算子、cover=覆盖选择、exec=overlay+子进程、report=输出；单文件 ≤500 行，按算子拆文件 |
| 4. 主题域边界清晰 | ✅ 遵守 | gomut 是独立 module，与 gobco（分支覆盖）无 import 耦合；二者仅在文档层面互补 |
| 5. bridge/adapter 显式化 | ✅ 遵守 | 与 Go 工具链的边界集中在 `internal/exec`（overlay + `go test` 子进程封装），与类型系统的边界集中在 `internal/mutate`（type-check 降级策略），不散落到各算子 |
| 6. 测试跟随生产代码 | ✅ 遵守 | 每 internal 包配 `*_test.go` 同目录；算子用 table-driven 测试；集成测试用 `testdata/` 内的小型 Go 模块做端到端（真跑 go test） |

## 7. 后果（Consequences）

- 正面：复用 Go 官方工具链（编译/测试/覆盖），无第三方依赖，每个 mutant 可用 diff 解释（无 junk mutation），coverage-based 选择对齐 pitest 验证过的最优路径，子进程隔离天然防状态污染。
- 负面/风险：
  - per-test coverage 对测试数多的包有 N 次 go test 的开销 → 用 D7 缓存 + D3 的 NO_COVERAGE 预筛缓解。
  - 文本手术需精确偏移计算 → 用 AST 的 `fset.Position` 边界，算子测试覆盖边界 case。
  - type-check 依赖 `go build` 预热 → engine 在扫描前对目标包执行一次 `go build`（复用 build cache）。
  - 变异破坏编译（未用变量等）→ 记 COMPILE_ERROR 并尝试简单修复（补 `_ = x`），修不好则剔除该 mutant 不计入分母。
- 后续任务：见 `docs/todo.md`。

## 8. 参考

- pitest 设计文档: `submodules/pitest/so_you_want_to_build_mutation_testing_system.md`
- pitest 算子: `submodules/pitest/pitest/src/main/java/org/pitest/mutationtest/engine/gregor/mutators/`
- pitest 结果模型: `DetectionStatus.java` / `MutationStatisticsPrecursor.java`
- gobco（分支覆盖，互补工具）: 仓库根
