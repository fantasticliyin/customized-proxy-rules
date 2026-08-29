# GeoData 规则构造与发布系统

## Goal

建立一个以规则文件为主体的独立 GeoData 仓库：持续消费 Mihomo 上游发布的 GeoSite / GeoIP DAT，在不复制上游源码与构建链的前提下应用少量 NEW / PATCH 自定义规则，并向 Mihomo 与 sing-box 发布来自同一最终数据基准的可追踪产物。

## User Value

- 规则维护者只维护自定义 CSV，不承担上游完整数据工程。
- Mihomo 只配置本仓库发布的一套 GeoData，无需为每个自定义分类增加 rule-provider。
- sing-box 的基础、已修改与自定义分类全部从本仓库获取，不混用多个来源。
- 上游更新与本地规则变化可自动构建、校验、发布和回滚。

## Requirement Sources

- `context.md` 是主要需求依据。
- `context_detailed.md` 仅作为实现细节参考；只有本 PRD 明确记录的讨论结论属于已确认需求。

## Requirements

### R1 — 上游与单向数据架构

- 上游固定为 `MetaCubeX/meta-rules-dat` GitHub Release 的 `geosite.dat` 与 `geoip.dat`。
- 不 checkout、复制或重新运行上游完整规则源码与构建流程。
- v1 同时交付 GeoSite 与 GeoIP。
- 固定使用单向数据链：上游 DAT → 语义 Patch → final DAT → 全量 SRS。
- final `geosite.dat` / `geoip.dat` 是唯一权威数据；Mihomo 直接使用 DAT，sing-box SRS 必须从同版本 final DAT 派生。

### R2 — 仓库与 CSV 规则模型

- Main 分支以以下正式规则目录为主体：
  - `rules/geosite/new/`
  - `rules/geosite/patch/`
  - `rules/geoip/new/`
  - `rules/geoip/patch/`
- Category 由 CSV 文件名确定，identity 统一小写；NEW category 文件名必须是小写安全名称。
- GeoSite CSV 固定表头和列顺序为 `op,type,value,attrs,note`。
- GeoIP CSV 固定表头和列顺序为 `op,type,value,note`，不包含 attribute 占位字段。
- CSV 使用标准引号和转义；`note` 不参与规则 identity，不支持非标准注释行。
- `op` 只允许 `+` / `-`；NEW 文件只允许 `+`，PATCH 文件允许二者。
- GeoSite `type` 只允许 Mihomo classical 类型 `DOMAIN`、`DOMAIN-SUFFIX`、`DOMAIN-KEYWORD`、`DOMAIN-REGEX`。
- GeoIP `type` 只允许 `IP-CIDR`，根据 value 自动识别 IPv4 / IPv6，不接受冗余的 `IP-CIDR6`。
- Main 根目录不放工具代码、工具配置或 docs；所有 patcher 内容放在 `tools/rule-patcher/`，包括：
  - `config.yaml`、`toolchain.lock.yaml`、`docs/`
  - `cmd/`、`internal/`、`testdata/`
  - `go.mod`、`go.sum`
- `dist/`、缓存和下载的外部工具不得提交到 main。

### R3 — GeoSite 语义

- `DOMAIN` 映射 GeoSite Full；`DOMAIN-SUFFIX` 映射 RootDomain；`DOMAIN-KEYWORD` 映射 Plain；`DOMAIN-REGEX` 映射 Regex。
- `DOMAIN` / `DOMAIN-SUFFIX` / `DOMAIN-KEYWORD` trim、转小写并去尾部点；Regex 保持原文并验证可编译。
- `attrs` 使用空格分隔的 `@name`；自定义规则 v1 只支持布尔 attribute，统一小写、去重和排序。
- 上游整数 attribute 原样保留；`inspect` 显示为 `@name=<value>`，但 v1 不能通过带 attrs 的 CSV 精确 Add / Del 整数 attribute。
- Add 带 attrs 时添加具有该完整布尔 attribute 集合的规则；不带 attrs 时添加无 attribute 规则。
- Del 带 attrs 时只删除 attribute 集合完全相同的规则；不带 attrs 时删除相同 `type + value` 的全部变体，包括整数 attribute 变体。
- final GeoSite DAT 中的每个 attribute view 都必须生成对应 SRS，例如 `geosite/google@ads.srs`。

### R4 — GeoIP 语义

- CIDR 自动清除主机位并规范化 IPv4 / IPv6 文本；规范 CIDR 是 Add / Del identity。
- 不合并相邻网段，不按包含关系删除冗余网段。
- Del 只匹配完全相同的规范 CIDR。
- 非法 CIDR 是输入错误，必须阻止构建。

### R5 — Overlay、顺序与失败语义

- 支持新增独立 category 和对既有 category 逐条 Add / Del；v1 不支持 REPLACE。
- Add 已存在、Del 不存在、PATCH 目标 category 不存在均为 no-op + warning，不阻止构建。
- NEW 与上游重名时自动降级为向同名上游 category 执行 Add，并产生 warning。
- 每个 no-op / 降级都必须进入机器可读摘要或 manifest，包含 category、操作、规则和原因。
- 同一 CSV 对同一 identity 同时声明 `+` 与 `-` 是输入冲突，必须失败。
- 保留上游 category 和未删除规则的原始顺序；NEW category 按名称排序后追加，本地新增规则按 `type + value + attrs` 排序后追加。
- 使用 deterministic Protobuf serialization；不对全部上游内容全局重排。

### R6 — SRS 转换与产物

- 不由本项目实现 SRS 二进制编码，也不自行维护平行的 DAT → sing-box JSON 转换器。
- 固定版本的 MetaCubeX `meta-rules-converter` 从 final V2Ray GeoSite / GeoIP DAT
  逐分类导出 sing-box JSON，固定版本的 sing-box `rule-set compile` 生成 SRS。
- converter 自带编译的 SRS 不进入发布；公开 SRS 必须全部由 toolchain lock 中的
  sing-box CLI 从 JSON 重新编译。
- 不使用 sing-geoip MMDB 作为完整 SRS 的中间格式，因为单值 MMDB 不能无损表达
  V2Ray DAT 中同一/重叠 CIDR 同时属于多个 category 的关系。
- 必须生成 final DAT 中所有基础 category 与 GeoSite attribute view 的 SRS。
- `release` 分支只保存 latest 成功快照：
  - `geosite/<category>.srs`
  - `geoip/<category>.srs`
  - 根目录 `manifest.json`、`SHA256SUMS`
- `release` 是干净 orphan 快照，每次成功发布 force-push，不累计全量 SRS 历史。
- 不可变 GitHub Release 包含 `geosite.dat`、`geoip.dat`、`srs.tar.gz`、`manifest.json`、`SHA256SUMS`，承担审计和回滚职责。

### R7 — CLI

- `rule-patcher build` 完成上游解析/下载、Patch、转换、校验和 artifact 生成，并支持指定 `upstream_release_id`。
- `rule-patcher inspect` 按 DAT、category 与可选 attribute 显示规范化规则。
- `rule-patcher diff` 对两个 DAT 做语义比较，至少支持 text 与 JSON。
- `rule-patcher validate` 独立校验完整 dist；CI 与本地复用同一逻辑。

### R8 — GitHub Actions 与版本

- Schedule 使用 `53 * * * *`，每小时第 53 分钟检查上游；无新组合时快速退出。
- Push 仅监听：
  - `rules/**`
  - patcher 的 config、toolchain lock、cmd、internal、go.mod、go.sum
  - `.github/workflows/release.yml`
- 单独修改 patcher docs 或 testdata 不触发正式发布。
- Schedule 检查时也必须以相同发布输入路径计算 `release_inputs_hash`；当上游
  Release ID 与该 hash 均未变化时快速退出，避免 docs/testdata commit 被下一次
  小时调度间接发布。
- `workflow_dispatch` 提供可选 `upstream_release_id`；为空解析 latest，填写时构建指定 Release。
- 不可变 Release tag 为 `u<upstream-release-id>-c<custom-shortsha>`；相同上游
  Release ID 与 main commit 组合只发布一次；`custom-shortsha` 固定取 12 位。
- Release 标题包含上游发布时间与本仓库短 commit。
- Manifest 至少记录上游仓库、release ID、发布时间、输入 DAT SHA256、本仓库
  commit、sing-box/Mihomo 版本、meta-rules-converter module version 与 commit、
  生成时间、category 统计、Patch 摘要与 warnings。
- 任何构建或校验失败都不得更新公开 Release 或 `release` 分支；发布流程必须避免 DAT Release 与 latest SRS 指向不同版本，并具备失败补偿。

### R9 — 校验、许可与首版内容

- 以下错误必须阻止发布：CSV/规则值无效、category 名冲突、输入操作冲突、上游 API/下载/checksum、DAT 解析或重载、工具锁验证、外部转换、DAT↔SRS index、manifest/checksum、Mihomo/sing-box 最小加载测试失败。
- 首版不预置虚构的正式自定义规则；正式 NEW / PATCH 目录允许为空。
- CSV 示例只进入 patcher docs；NEW、Add、Del、attribute、GeoIP fixtures 只进入 patcher testdata。
- 无自定义规则的首次构建仍需使用真实上游 Release 完成 DAT、全量 SRS 与端到端发布校验。
- 整个仓库使用 `GPL-3.0-only`；根目录提供 LICENSE，README、manifest 与 Release 说明保留 `MetaCubeX/meta-rules-dat` 来源和许可证 attribution。

## Acceptance Criteria

- [ ] **AC-01 / CSV：** 两种固定 CSV schema 可解析合法 quoted CSV，并拒绝错误表头、列数、op、type、Regex、CIDR、attribute 和同 identity 冲突。
- [ ] **AC-02 / GeoSite：** Fixtures 覆盖 NEW、PATCH Add/Del、无 attrs Del 全变体、布尔 attrs 精确 Del、整数 attrs 保留及 no-op warnings；输出 DAT 语义符合 R3/R5。
- [ ] **AC-03 / GeoIP：** Fixtures 覆盖 IPv4/IPv6 规范化、NEW、PATCH Add/Del、重叠网段保留和 no-op warnings；输出 DAT 语义符合 R4/R5。
- [ ] **AC-04 / 可复现：** 相同输入、规则和 toolchain 连续构建得到相同 final DAT、manifest 语义、SRS 与 SHA256SUMS（允许 manifest 中明确排除哈希比较的运行时间字段）。
- [ ] **AC-05 / Mihomo：** final `geosite.dat` / `geoip.dat` 能被锁定版本 Mihomo 加载；新增或修改分类可按普通 GEOSITE / GEOIP 使用。
- [ ] **AC-06 / sing-box：** final DAT 的每个基础 category 与 GeoSite attribute view 在 `release` 布局中都有可被锁定版本 sing-box 加载的 SRS，DAT↔SRS index 一致。
- [ ] **AC-07 / CLI：** build、inspect、diff(text/JSON)、validate 的成功与失败路径有自动化测试，退出码和机器可读输出稳定。
- [ ] **AC-08 / 真实上游：** 给定固定的真实 `MetaCubeX/meta-rules-dat` Release ID，Workflow 能验证 checksum、生成全部产物并记录完整 provenance。
- [ ] **AC-09 / 触发与快速退出：** Schedule、受控 push paths 和 workflow_dispatch 行为符合 R8；已存在 tag 的组合不下载工具或执行完整构建。
- [ ] **AC-10 / 发布：** 成功后不可变 Release 与 `release` latest 快照指向同一 manifest/version；注入任一发布阶段失败时不会留下可见的错配状态，历史成功版本仍可回滚。
- [ ] **AC-11 / 空规则首版：** 四个正式规则目录为空时仍能完成真实上游端到端构建，docs/testdata 示例不进入公开 DAT 或 SRS。
- [ ] **AC-12 / 许可：** LICENSE、README attribution、manifest 与 Release 说明满足 R9。

## Out of Scope

- 复制或重新实现上游完整规则聚合、清洗和构建系统。
- 客户端运行时动态 Patch。
- 自研 SRS 二进制编码或平行 DAT → sing-box JSON 转换器。
- v1 完整分类 REPLACE。
- v1 自定义整数 attribute Add / 精确 Del。
- GeoIP 网段聚合、包含消除或其他自动优化。
- v1 `repository_dispatch` 或受控上游 fork 通知链。
