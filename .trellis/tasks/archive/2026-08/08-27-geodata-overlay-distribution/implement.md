# GeoData 规则构造与发布系统 — 实施计划

## 实施原则

- 每一阶段完成自己的单元/集成测试后再进入下一阶段。
- 产品语义只有一个 owner：CSV 在 `rulescsv` 规范化，overlay 在 `overlay`
  reducer 执行，artifact 完整性在 `validate` 复验；CLI 和 workflow 不复制规则。
- 所有外部格式先以 fixture 固化契约，再接入真实网络与工具。
- 实现期间不提交真实构建产物、下载工具或虚构正式规则。
- 工具版本变更、CSV schema 变更、manifest schema 变更均作为显式 review
  checkpoint，不能顺手升级。

## Phase 1 — 仓库骨架、许可与静态契约

### 1.1 创建规则主体布局

- 创建四个正式空规则目录与必要 `.gitkeep`。
- 创建 `tools/rule-patcher/` Go module、`cmd/rule-patcher`、`internal/*`、
  `docs/`、`testdata/`。
- 添加根 `.gitignore`，覆盖 dist、临时目录、工具 cache 与本地二进制。
- 添加 GPL-3.0-only `LICENSE`、README 上游 attribution 和项目数据流说明。

### 1.2 固化配置 schema

- 编写 `config.yaml` 与 strict decoder，路径相对配置文件解析。
- 编写 `toolchain.lock.yaml` schema 与 strict decoder。
- 实现 category/path 安全校验和 schema version 检查。
- 在实现 PR 中通过官方 Release/API 再次核验 design 推荐的版本、asset 名与
  SHA-256 后写 lock；保存核验依据到测试或文档。

### 1.3 验证

```bash
go -C tools/rule-patcher test ./internal/config/...
go -C tools/rule-patcher vet ./internal/config/...
```

Checkpoint：目录和配置必须保持“rules-first”；根目录不能新增 patcher 配置或
docs。若工具 hash 无法从可信来源复核，暂停工具接入，不使用未锁版本替代。

## Phase 2 — CSV、规范化与共享领域模型

### 2.1 `internal/rulescsv`

- 实现两个 exact header reader，错误包含相对文件与 logical record。
- 扫描四个目录，仅接受安全 category 的 `.csv` 普通文件。
- 实现 GeoSite type mapping、普通域名规范化、Regex 原文编译验证。
- 实现 attrs parse/小写/去重/排序，只接受布尔 `@name`。
- 实现 GeoIP IP/CIDR parse、family 自动识别、`Masked()` canonicalization。
- 实现 NEW op 限制与跨记录冲突预检。

### 2.2 测试

- table tests 覆盖 quoted comma/newline、BOM/错误编码策略、空记录、错误列数、
  大小写碰撞、路径字符、所有 op/type/value/attrs 边界。
- fuzz CSV reader、domain canonicalizer、attrs parser、CIDR parser，断言不 panic
  且 canonicalization idempotent。
- fixtures 全部位于 `tools/rule-patcher/testdata`，不进入正式 rules。

### 2.3 验证

```bash
go -C tools/rule-patcher test ./internal/rulescsv/... -run .
go -C tools/rule-patcher test ./internal/rulescsv/... -fuzz Fuzz -fuzztime 20s
```

Checkpoint：对照 PRD AC-01；任何“宽松接受”必须删除或先更新需求，不在 parser
中隐藏兼容分支。

## Phase 3 — DAT 模型、semantic overlay 与本地诊断

### 3.1 `internal/geodat`

- 使用锁定 MetaCubeX geo protobuf 类型读写 GeoSite/GeoIP。
- 按 dataset 顺序执行有界内存 load/patch/write/reload/release，不实现自定义 protobuf
  wire streaming；默认拒绝超过 128 MiB 的单个 DAT。
- clone 原消息，保留 unknown fields、整数 attrs、GeoIP `reverse_match`。
- 实现 deterministic marshal、reload 和 semantic index。
- 提供共享 inspect projection 与 diff projection；CLI 只负责格式化 projection。

### 3.2 `internal/overlay`

- 单一 reducer 实现 NEW/PATCH、Add/Del 与四类 warning。
- 实现 GeoSite exact attr Del、无 attrs wildcard Del。
- 实现 GeoIP exact canonical CIDR Del，不合并/不包含消除。
- 保留原始顺序，按设计确定性追加 new category 与 added entries。
- 汇总每 dataset/category 的 add/delete/no-op/retained 数量。

### 3.3 单元和 fixture integration

- 构造含 bool/int attrs、unknown fields、大小写 code 和 `reverse_match` 的小型 DAT。
- GeoSite 覆盖 NEW、collision downgrade、Add existing、Del missing、missing PATCH、
  exact attrs、wildcard attrs、整数 attrs 保留。
- GeoIP 覆盖裸 IP、IPv4/IPv6 host bits、重叠/相邻/包含、exact Del。
- 同一 fixture 连续运行两次，比较 DAT bytes、projection JSON 与 warning JSON。
- round-trip 前后比较未修改字段和 entry 相对顺序。
- 覆盖刚好位于大小上限、超过上限和声明 size 与实际读取 size 不一致的失败路径；
  集成测试确认 GeoSite 模型释放后才进入 GeoIP 主处理阶段。

### 3.4 验证

```bash
go -C tools/rule-patcher test ./internal/geodat/... ./internal/overlay/...
go -C tools/rule-patcher test -race ./internal/geodat/... ./internal/overlay/...
```

Checkpoint：对照 AC-02/03/04。出现 protobuf 字段丢失时先修领域模型，不允许由
manifest 忽略差异。

## Phase 4 — 上游解析、工具锁与 SRS pipeline

### 4.1 `internal/upstream`

- 实现 GitHub Release database ID 与 latest 两种解析路径。
- 正确处理分页、404、403/429 rate limit、5xx、超时与响应 size limit。
- 精确选择四个预期 assets，禁止模糊或前缀匹配。
- 流式下载到临时文件，同时计算 SHA-256；验证 API digest 与 checksum asset。
- 输出 immutable provenance object，后续层不得重新按 latest 取数据。

### 4.2 `internal/toolchain`

- lock hash 分区 cache，下载/构建到 temp 后校验并 atomic rename。
- 下载锁定 sing-box/Mihomo asset；构建锁定 meta-rules-converter
  pseudo-version。
- 验证平台、hash、可执行权限和可记录 provenance。
- 命令执行统一 context timeout、stdout/stderr size limit 与脱敏错误。

### 4.3 `internal/srs`

- 编排 meta-rules-converter 从 final DAT 生成逐 category sing-box JSON；忽略并
  删除 converter 自带 SRS。
- 从 final DAT 独立计算期望 index，安全验证 converter JSON 文件名、集合和内容；
  GeoSite 包含全部基础 category 与 bool/int attribute views，GeoIP 保留跨 category
  的相同/重叠 CIDR。
- 对每个 JSON 使用锁定 sing-box CLI 执行 `rule-set compile`。
- 比较 DAT expected index、JSON index 与最终 SRS file index。
- 对产出 SRS 执行读取验证与代表性最小配置 smoke test。

### 4.4 测试

- `httptest.Server` 覆盖上游响应、校验和与断流错误。
- fake executables 覆盖命令参数、超时、坏 list、重复/path traversal category、
  export/compile 失败和 index mismatch。
- 锁定真实工具运行小 DAT fixture，验证 bool/int attribute views、IPv4/IPv6 以及
  同一 CIDR 同属多个 GeoIP category 不丢失。

### 4.5 验证

```bash
go -C tools/rule-patcher test ./internal/upstream/... ./internal/toolchain/... ./internal/srs/...
go -C tools/rule-patcher test -race ./internal/upstream/... ./internal/toolchain/... ./internal/srs/...
```

Checkpoint：对照 AC-05/06/08。只能使用 design 中的 converter chain；若某个锁定
工具实际 CLI 与研究不符，更新 research/design/lock 并 review，不能加入未经说明
的自研 fallback。

## Phase 5 — Artifact、manifest、CLI 与完整 validate

### 5.1 `internal/artifact`

- 定义 versioned manifest Go types，禁止各模块自行拼 map。
- 生成 deterministic JSON、SRS tar.gz、Release checksum set 和 branch checksum
  set。
- 使用同父目录 staging + rename 形成 dist，失败不留半成品目标。
- 实现 rules tree hash、config hash、lock hash 与稳定 `SOURCE_DATE_EPOCH`。
- 实现 `release_inputs_hash`；docs/testdata 不得进入该 hash。由于 GitHub Actions
  不能在 trigger 与脚本间复用同一动态列表，明确维护 trigger/hash 两份清单，并
  用自动 trigger/hash matrix 测试阻止漂移。

### 5.2 `internal/validate`

- 只从 dist 磁盘重读 DAT、manifest、tar、SRS 与 checksums。
- 校验 provenance 完整性、hash/size、tar path/metadata、安全路径、index 与计数。
- 重跑锁定工具加载/smoke 验证；输出 versioned JSON report。

### 5.3 `cmd/rule-patcher`

- 实现 `build`、`inspect`、`diff`、`validate`，共享 config/decoder/projection。
- 稳定退出码 0/1/2，stdout/stderr 分离；JSON schema fixtures 做 golden tests。
- `build` 在成功返回前无条件调用完整 disk-backed `validate`。

### 5.4 测试与验证

```bash
go -C tools/rule-patcher test ./internal/artifact/... ./internal/validate/... ./cmd/rule-patcher/...
go -C tools/rule-patcher test -race ./...
go -C tools/rule-patcher vet ./...
test -z "$(gofmt -l tools/rule-patcher)"
```

另外执行：

- 两个 clean temp directory 使用同一 fixture build，递归比较所有 hash；
- `inspect` integer attrs、`diff --format text/json` 与四种 warning golden；
- 人工破坏每一类 artifact，确认 `validate` 非零且错误定位准确。

Checkpoint：对照 AC-04/07。manifest 字段如引入墙钟/耗时导致 hash 漂移，必须改为
日志字段，不能弱化可复现验收。

## Phase 6 — 真实上游 E2E 与使用文档

### 6.1 真实固定 Release 构建

- 选择并记录一个 immutable `MetaCubeX/meta-rules-dat` Release database ID。
- 在正式四目录为空的状态运行完整 build 两次。
- 核对上游 asset digest/checksum、DAT reload、全量 category/attribute SRS、Mihomo
  与 sing-box smoke test、完整 manifest attribution。
- 记录测试命令和预期资源占用；大产物不提交 git。

### 6.2 文档

- 根 README：用途、公开资产、Mihomo/sing-box 使用方式、版本/tag 语义、上游
  attribution、schedule 可能延迟/闲置停用说明。
- patcher docs：两个 CSV schema、quoted examples、attrs/Del/warning 语义、CLI、
  toolchain update 流程、手工复验和回滚手册。
- 示例仅放 docs/testdata，并明确不会进入正式产物。

Checkpoint：对照 AC-08/11/12；首版必须以空正式规则完成真实 E2E，不能用小 fixture
代替全量验收。

## Phase 7 — GitHub Actions 与发布事务

### 7.1 Workflow build path

- 添加 schedule、workflow_dispatch、严格 push paths、最小 permissions、固定 action
  SHA 与 `cancel-in-progress: false` concurrency。
- 第一阶段只解析 identity/tag 与 `release_inputs_hash`；目标 tag 已存在且 manifest
  一致，或 upstream ID + input hash 与最新成功 manifest 相同时快速退出。
- setup/download/build 分支设置明确 job outputs，测试快速退出不进入昂贵步骤。
- 上传临时 run artifact，在发布 job 下载后重跑 `validate`。

### 7.2 Publisher

- 实现 draft Release 创建与五资产上传/复核。
- 由 dist 创建 clean orphan snapshot；检查只含允许的 SRS、manifest、checksums。
- 保存旧 branch SHA，以 force-with-lease 更新，发布 draft，再做 identity 一致性检查。
- trap/finalizer 执行 draft 删除、对本次已公开失败 Release/tag 的精确删除与 branch
  恢复；补偿操作同样使用 ID/identity 检查和 lease，避免删除历史成功版本或覆盖
  并发外部修改。
- Release notes 自动包含 provenance、warning 摘要、许可证 attribution 和 checksum
  验证方法。

### 7.3 发布故障测试

在 fork/测试仓库或 mock GitHub API 中逐点注入：

1. draft create/upload/asset verify 失败；
2. orphan push 失败或 lease 不匹配；
3. Release publish 失败；
4. final identity check 失败；
5. Release 已发布后的 final identity check 失败；
6. compensation 本身遇到并发 ref 变化。

每例断言：不存在公开半成品 Release；`release` 要么是旧成功 snapshot，要么新成功
snapshot；若自动补偿因并发安全拒绝执行，日志给出旧/new/ref 和精确人工恢复步骤。

### 7.4 验证

- 用 `actionlint` 校验 workflow；
- 验证 push path matrix：rules/code/config/lock/workflow 触发，docs/testdata 不触发；
- 验证 schedule 为 `53 * * * *`，dispatch ID 空/指定两条路径；
- 验证同 tag 第二次运行，以及 docs/testdata-only commit 后的 schedule，都在工具
  和 DAT 下载前退出；
- 对照 GitHub Release manifest 与 `release` branch manifest/version/hash。

Checkpoint：对照 AC-09/10。正式启用 schedule 前进行一次手工 dry-run build 和一次
测试发布；未经 compensation 故障测试不得打开自动 publication。

## Phase 8 — 总质量门禁与交付

### 8.1 自动门禁

```bash
test -z "$(gofmt -l tools/rule-patcher)"
go -C tools/rule-patcher vet ./...
go -C tools/rule-patcher test -race ./...
actionlint .github/workflows/release.yml
```

再运行固定真实 Release 的两次 clean E2E，比较：

- `geosite.dat`、`geoip.dat` bytes；
- 每个 `.srs`、`srs.tar.gz`；
- `manifest.json`、两种 checksum set；
- DAT base index、GeoSite attribute view index、SRS index。

### 8.2 人工验收映射

| PRD AC | 主要证据 |
| --- | --- |
| AC-01 | Phase 2 parser table/fuzz tests |
| AC-02 | Phase 3 GeoSite fixtures + DAT inspect |
| AC-03 | Phase 3 GeoIP fixtures + DAT inspect |
| AC-04 | Phase 3/5/8 deterministic comparisons |
| AC-05 | Phase 4/6 Mihomo smoke |
| AC-06 | Phase 4/6 index equality + sing-box smoke |
| AC-07 | Phase 5 CLI tests/golden/exit codes |
| AC-08 | Phase 6 fixed real Release E2E |
| AC-09 | Phase 7 trigger matrix + fast exit trace |
| AC-10 | Phase 7 publication failure injection |
| AC-11 | Phase 6 empty official rules E2E |
| AC-12 | Phase 1/6 license and attribution review |

### 8.3 回退条件

- 实现未进入正式发布前：删除测试仓库的 draft/tag/branch，main 通过普通 revert 回退。
- 已有正式成功版本后：停止 schedule，把 `release` 分支恢复至所选历史 Release 的
  SRS snapshot；历史 Release 不改写/删除；修复通过全门禁后发布新 tag。
- 任何 toolchain 升级失败：回退 lock 与相应 go module version，不修改 CSV 或
  manifest schema 来迁就工具差异。

交付前运行 Trellis check，逐项回填证据；所有 blocker 清零后才可将任务标记完成。
