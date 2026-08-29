# GeoData 规则构造与发布系统 — 技术设计

## 1. 设计目标与边界

系统只拥有两类数据：仓库内的 CSV overlay，以及一次构建中下载的上游
V2Ray GeoData。最终 DAT 是唯一权威输出；SRS 只能从同次构建的最终 DAT
派生。系统不复制上游规则源码、不重做上游聚合，也不实现 SRS 编码。

完整数据流：

```text
GitHub Release API       rules/**/*.csv       toolchain.lock.yaml
        │                       │                        │
        ▼                       ▼                        ▼
  upstream resolver ──► canonical validation ◄── tool verification
        │                       │
        ▼                       ▼
  geosite/geoip DAT ──► semantic overlay ──► deterministic final DAT
                                                │
                           ┌────────────────────┴───────────────────┐
                           ▼                                        ▼
                    Mihomo smoke test               MetaCubeX meta-rules-converter
                                                                    │
                                                       per-category sing-box JSON
                                                                    │
                                                        sing-box rule-set compile
                                                                    │
                                                                    ▼
                                                 full SRS + manifest + checksums
                                                                    │
                                                    validate complete dist
                                                                    │
                                  ┌─────────────────────────────────┴──────────┐
                                  ▼                                            ▼
                        immutable GitHub Release                    `release` snapshot
```

边界原则：CSV 解析层负责用户输入合法性；DAT 层负责 protobuf 与上游语义；
转换层只编排锁定的现成工具；发布层只接收已经通过完整 `validate` 的 dist。

## 2. Main 分支布局

```text
.
├── .github/
│   └── workflows/
│       └── release.yml
├── rules/
│   ├── geosite/
│   │   ├── new/
│   │   └── patch/
│   └── geoip/
│       ├── new/
│       └── patch/
├── tools/
│   └── rule-patcher/
│       ├── cmd/rule-patcher/
│       ├── internal/
│       │   ├── artifact/
│       │   ├── config/
│       │   ├── geodat/
│       │   ├── overlay/
│       │   ├── rulescsv/
│       │   ├── srs/
│       │   ├── toolchain/
│       │   ├── upstream/
│       │   └── validate/
│       ├── testdata/
│       ├── docs/
│       ├── config.yaml
│       ├── toolchain.lock.yaml
│       ├── go.mod
│       └── go.sum
├── .gitignore
├── LICENSE
└── README.md
```

空正式目录用 `.gitkeep` 保留；规则扫描器只读取扩展名为 `.csv` 的普通文件。
构建工作目录、工具缓存和 `dist/` 均位于 gitignored 路径。

## 3. 核心数据契约

### 3.1 CSV 边界

使用 Go `encoding/csv`，要求 UTF-8、固定表头、固定列数。允许标准 CSV 的
quoted comma、quoted newline 与双引号转义；不支持自定义注释或宽松表头。
错误必须携带相对文件名与逻辑记录号。

内部只保留规范化后的 typed operation：

```go
type Operation string // "+" | "-"
type Dataset string   // "geosite" | "geoip"

type SiteRule struct {
    Type  SiteRuleType
    Value string
    Attrs []string // canonical, sorted boolean names without duplicated @
}

type IPRule struct {
    Prefix netip.Prefix // always Masked()
}

type PatchRecord[T any] struct {
    Dataset  Dataset
    Mode     string // "new" | "patch"
    Category string
    Op       Operation
    Rule     T
    Source   SourceLocation
    Note     string // display only
}
```

规则 identity 不含 `note`：GeoSite 为规范化 `type + value + 完整 attrs`，
GeoIP 为 `netip.Prefix.Masked().String()`。不过无 attrs 的 GeoSite Del 是一个
显式 wildcard delete 操作：匹配相同 `type + value` 的全部 attribute 变体，
不能误当作“删除无 attrs identity”。同文件同一 exact identity 同时出现
`+`/`-` 时失败；wildcard Del 与任何相同 `type + value` 的 Add 也属于冲突。
相同操作的重复记录按第一次执行，后续记录产生 `add_exists` 或
`delete_missing` warning，不额外扩大 fatal 输入范围。

### 3.2 Category

从去掉 `.csv` 的文件名得出，规范形式为小写。合法名称建议约束为
`^[a-z0-9][a-z0-9_-]*$`；拒绝 `@`、路径分隔符、空白、`.`/`..` 和大小写
碰撞。`@` 由转换工具保留给 attribute view。跨 NEW/PATCH、GeoSite/GeoIP
的同名并非天然冲突；只在同一 dataset 与同一 mode 中禁止重复文件映射。

### 3.3 GeoSite protobuf

使用锁定版本 `github.com/metacubex/geo/encoding/v2raygeo` 的生成类型。映射：

| CSV | V2Ray Domain type |
| --- | --- |
| `DOMAIN` | `Full` |
| `DOMAIN-SUFFIX` | `RootDomain` |
| `DOMAIN-KEYWORD` | `Plain` |
| `DOMAIN-REGEX` | `Regex` |

普通值 trim、转小写、去尾部点并拒绝空值；Regex 字段逐字保留（包括有意义的
首尾空白），使用 Go RE2 语法预编译验证。自定义 attrs 接受空值或由空格分隔的
`@name`，名称小写、去重、排序，v1 只构造布尔 `true` attribute。

上游 entry 的未知 protobuf 字段、布尔/整数 attribute 以及原顺序必须保留。
实现应尽量 clone 原消息，只对命中的 entry slice 做最小修改。带 attrs 的 Del
只匹配完整布尔集合，且不匹配整数属性；无 attrs Del 匹配相同 type/value 的
所有消息。Category code 比较不区分大小写，输出沿用上游原 code；新增使用
小写 code。

### 3.4 GeoIP protobuf

地址通过 `netip.ParsePrefix` 或由裸 IP 补 `/32`、`/128` 后解析，再调用
`Masked()`。写回时转换为 address bytes + prefix length。只执行 exact CIDR
Add/Del；保留重叠、包含与相邻网段。Category 的 `reverse_match` 以及未知字段
原样保留。

### 3.5 Overlay reducer

一个 reducer 统一处理 NEW/PATCH 与 `+`/`-`，避免各命令重复语义。处理顺序：

1. 载入并校验全部 CSV，先发现跨记录冲突；
2. 按 dataset、mode、category、source location 建立确定性输入序列；
3. PATCH category 不存在：整文件 skip，并逐文件产生 warning；
4. NEW 与上游碰撞：转换为同名 category 的 Add 集合并产生 warning；
5. 删除时保留未命中消息的原始相对顺序；
6. 新 category 按 code 排序追加；每个 category 的新增规则按 canonical identity
   排序追加；
7. Add existing / Del missing 为 no-op warning。

Warning 是稳定机器契约：

```go
type Warning struct {
    Code     string         `json:"code"`
    Dataset string         `json:"dataset"`
    Mode     string         `json:"mode"`
    Category string         `json:"category"`
    Operation string       `json:"operation,omitempty"`
    Rule     map[string]any `json:"rule,omitempty"`
    Source   SourceLocation `json:"source"`
    Reason   string         `json:"reason"`
}
```

稳定 code 至少有 `add_exists`、`delete_missing`、`patch_category_missing`、
`new_category_collision`。文本仅用于人读，自动化只依赖 code 和结构字段。

## 4. DAT 编解码与可复现性

- DAT 下载采用流式临时文件并同步计算 SHA-256；protobuf 解析与 Patch 不采用
  自定义 wire-level streaming，而是按 dataset 依次完整载入内存。GeoSite 完成
  patch、deterministic marshal、落盘和 reload 后释放模型，再处理 GeoIP，避免
  同时长期持有两套数据。
- 单个 DAT 默认最大输入为 128 MiB，读取前后都检查实际大小；超过上限直接失败。
  该上限可由严格配置下调，但正式构建不得关闭。真正的 protobuf 流式 patch
  需要自定义 wire parser 和多遍 spool，会显著增加 unknown-field、顺序与 wildcard
  Del 的保真风险，v1 不采用。
- 读取后校验 code 唯一性（case-insensitive）、entry type、IP prefix 范围。
- 使用 `proto.Clone` 保存未修改字段，写出采用
  `proto.MarshalOptions{Deterministic: true}`。
- 写出后立即重新读取并做 semantic index/数量/保留字段检查。
- JSON 固定缩进、字段模型和有序 slice；禁止将 map 的迭代顺序写入 artifact。
- `generated_at` 不是墙钟时间，而是由 `SOURCE_DATE_EPOCH` 得出的 UTC 时间；
  CI 将其固定为 main commit 的 committer timestamp。因此相同 Release ID、commit
  和 lock 能生成相同 manifest。
- `srs.tar.gz` 使用文件名字典序、固定 mtime、uid/gid 0、空用户名/组名和无
  gzip 时间戳。
- `SHA256SUMS` 按相对路径排序，使用两个空格分隔 hash 与路径；不包含自身。

## 5. 配置与工具链锁

`config.yaml` 只保存产品配置，路径相对配置文件解析：

```yaml
upstream:
  repository: MetaCubeX/meta-rules-dat
  assets:
    geosite: geosite.dat
    geosite_checksum: geosite.dat.sha256sum
    geoip: geoip.dat
    geoip_checksum: geoip.dat.sha256sum
rules_dir: ../../rules
```

`toolchain.lock.yaml` 是唯一工具版本来源，至少记录 schema version、目标平台、
sing-box 与 Mihomo 的版本/asset URL/SHA-256，以及 MetaCubeX
`meta-rules-converter` 的 module、pseudo-version、commit 和构建参数。v1 发布
环境锁为 Linux amd64；单元测试可在
其他平台运行，但正式 build 必须拒绝未锁定平台或未验证二进制。

外部工具进入按 lock hash 分区的本地 cache。下载必须先写临时文件，校验后
原子 rename；converter 从锁定 pseudo-version 以 `-trimpath` 与空 buildid
构建，并把 module provenance 写入 manifest。不得用 `@main`、`@master` 或
`latest` 执行发布。

初始锁建议采用调研文档中的 sing-box `v1.13.19`、Mihomo `v1.19.30` 与
meta-rules-converter `v0.0.0-20251201061744-7dea27841a35`。实现时在 PR 中再次
验证发布资产 SHA/commit，不能仅照抄规划快照。

## 6. SRS 转换

### 6.1 已排除的 sing DB 路线

`sing-box geosite/geoip` 不能直接读取 V2Ray DAT。更重要的是 sing-geoip MMDB 对
每个网络只有一个结果，无法无损表示 V2Ray DAT 中同一/重叠 CIDR 同时属于国家与
服务商等多个 category。虽然 `geo convert ip -o sing` 能生成数据库，但它不能
作为“完整 SRS 集合”的权威中间层。该路线只可用于兼容性对照测试，不能发布。

### 6.2 推荐的无损现成工具路线

锁定 MetaCubeX 官方 `meta-rules-converter`，由它直接逐 category 将 final DAT
导出成 sing-box JSON；再使用单独锁定的当前 sing-box 官方编译器生成 SRS：

```bash
meta-rules-converter geosite -f final/geosite.dat -o staging/geosite -t sing-box
meta-rules-converter geoip   -f final/geoip.dat   -o staging/geoip   -t sing-box

sing-box rule-set compile staging/geosite/<category>.json \
  -o dist/geosite/<category>.srs
sing-box rule-set compile staging/geoip/<category>.json \
  -o dist/geoip/<category>.srs
```

converter 自带的 `.srs` 由其编译期 sing-box library 生成，全部丢弃；发布 SRS
只接受上面由 toolchain lock 中 sing-box CLI 重新编译的结果。converter 当前对
部分逐文件错误只打印而不返回非零，因此其退出码不是充分成功条件：编排器必须
根据 final DAT 独立计算期望 index，拒绝缺失/额外/重复 JSON，并逐个解析 JSON。

GeoSite 期望 index 是所有基础 category 加每个出现过的 attribute key 对应的
`category@attribute` view；整数 attribute 也按 key 形成 view。GeoIP 期望 index
等于 final DAT category 集。converter 不经过单值 MMDB，因此同一 CIDR 可同时
保留在多个 SRS。所有 JSON 按名称排序后调用 sing-box 编译并最终排序汇总。

临时 JSON/DB 不进入发布物。每个 SRS 用锁定 sing-box 执行读取级验证；此外生成
最小 sing-box 配置引用代表性的基础/attribute/IPv4/IPv6 SRS。Mihomo smoke test
用本地 HTTP server 提供 final DAT，并运行锁定版本的配置检查/短时启动，验证
自定义 category 能按普通 `GEOSITE`/`GEOIP` 路径解析。

## 7. CLI 合约

所有命令遵守：成功退出 0；输入/校验失败退出 2；上游、工具或 I/O 失败退出 1。
错误写 stderr；机器输出写 stdout；不得把 token、下载签名 URL 写入日志。

### `build`

```text
rule-patcher build
  --config <path>
  --lock <path>
  --output <dist-dir>
  [--upstream-release-id <database-id>]
  [--github-token-env GITHUB_TOKEN]
```

解析 immutable upstream release、执行快速退出所需的 identity 检查之外，完整
build 包含下载、patch、DAT reload、工具准备、SRS、manifest、checksum 和
`validate`。输出目录先在同父目录 staging，成功后原子替换；已存在目标目录默认
拒绝覆盖，CI 使用新的空路径。

### `inspect`

```text
rule-patcher inspect --dataset geosite|geoip --dat <path>
  [--category <name>] [--attribute <name>] [--format text|json]
```

GeoSite integer attrs 输出 `@name=<value>`；JSON 使用稳定 schema。attribute
过滤只适用于 GeoSite，GeoIP 传入则失败。

### `diff`

```text
rule-patcher diff --dataset geosite|geoip --before <dat> --after <dat>
  [--format text|json]
```

按 category 与 canonical identity 报 add/delete；category 新增/删除由同一结构
表达。顺序变化单列，不伪装成语义增删。

### `validate`

```text
rule-patcher validate --dist <path> --config <path> --lock <path>
  [--format text|json]
```

从磁盘重新读取完整 dist，不能复用 build 内存对象，保证 CI 与本地复验相同。

## 8. Dist、Manifest 与 Release 布局

构建 staging/dist：

```text
dist/
├── geosite.dat
├── geoip.dat
├── geosite/*.srs
├── geoip/*.srs
├── srs.tar.gz
├── manifest.json
└── SHA256SUMS
```

`manifest.json` 使用显式 `schema_version: 1`，包含：

- `version`: tag、upstream release ID、custom commit/full+short SHA；
- `upstream`: repository、release ID/tag/published_at、每个 DAT 的 asset ID、
  size、API digest、checksum-file digest 与最终 SHA-256；
- `toolchain`: sing-box/Mihomo 版本、asset hash，meta-rules-converter
  module/version/commit；
- `build`: deterministic generated_at、config hash、rules tree hash、lock hash；
- `datasets`: DAT hash/size、base category count、attribute view count、rule count，
  SRS file count；
- `patch`: add/delete/no-op 数量和完整结构化 warnings；
- `files`: DAT、SRS 与 tar 内容文件的 path、size、SHA-256（不包含会造成循环
  引用的 manifest 与 checksum 文件本身）；
- `attribution`: 上游 URL 与许可证标识。

Release 上传五个根资产：两个 DAT、`srs.tar.gz`、manifest、checksums。
`srs.tar.gz` 内仅有 `geosite/`、`geoip/`。`release` 分支 snapshot 不含 DAT 和
tar，只含两个 SRS 目录、manifest 与 checksums；分支 checksums 为该 snapshot
重新生成并在 manifest 中区分 release-asset checksum set 与 branch checksum set。
这样不会出现 checksum 文件引用分支中不存在的 DAT。两个 `SHA256SUMS` 都可以
包含同一份 `manifest.json`，但 manifest 不反向记录 checksum 文件的 hash。

## 9. GitHub Actions 与快速退出

单一 `.github/workflows/release.yml`：

- `schedule: '53 * * * *'`；
- `workflow_dispatch.inputs.upstream_release_id` 可空；
- push paths 严格等于 PRD R8 列表；
- `permissions: contents: write`，其余最小权限；第三方 action 固定完整 commit SHA；
- `concurrency.group: geodata-release` 且 `cancel-in-progress: false`。

第一个 job 只 checkout main 并调用 GitHub API：解析指定 ID 或 latest、取得 main
full SHA，生成 `u<ID>-c<12位shortSHA>`，并对 R8 的精确发布输入路径计算
`release_inputs_hash`。若最新成功 manifest 的 upstream Release ID 与该 hash
都相同，则成功快速退出；这保证 docs/testdata-only commit 不会被下一次 schedule
间接发布。若目标 tag 已存在且其 manifest identity 一致也快速退出；tag 存在但
identity 不一致则失败，不得覆盖。两种快速退出都发生在 setup Go、下载工具和
DAT 之前。

完整 job 使用 release ID 的 API URL重新获取资产，避免 mutable latest 漂移。
下载阶段若 release 被删除/资产变化则失败。Workflow 产出的中间 artifact 只用于
同一 run 内 job 传递，最终发布前再次 `validate`。

## 10. 发布事务与补偿

GitHub 不提供跨 Release/branch 的原子事务。采用以下可补偿协议：

1. 记录发布前 `refs/heads/release` 的精确 SHA（不存在也记录）；
2. 创建 draft GitHub Release，上传并校验全部五个资产；draft 对用户不可见；
3. 在临时本地仓库创建无父提交的 orphan snapshot，内容只来自已验证 dist；
4. force-with-lease 更新 `release` 分支到新 snapshot；lease 目标是步骤 1 的 SHA；
5. 将 draft Release 发布为 immutable；禁止覆盖既有 tag/assets；
6. 再次比较 branch manifest 与 Release manifest 的 version/hash；
7. 任一步失败时：删除未发布 draft；若 Release 已公开，则按 workflow 创建时
   保存的 Release database ID 与 tag/manifest identity 精确确认后删除本次失败
   Release 及新 tag（绝不删除历史成功 Release）；如果 branch 已更新则以
   force-with-lease 恢复旧 SHA，原先不存在则删除新分支；补偿失败需要 workflow
   以明确的 critical 状态失败并输出人工恢复命令。

步骤 4 到 5 之间可能短暂出现“新 latest SRS、旧 Release 列表”的窗口，但不会
出现新 DAT Release 配旧 SRS；步骤 5 失败会自动恢复 branch。步骤 5 已成功但
步骤 6 失败时，补偿先撤销本次失败 Release/tag，再恢复 branch。采用 branch-first
是因为反向顺序会公开新 DAT 而 latest SRS 尚未就绪，违背更关键的一致性要求。
历史 GitHub Releases 永不删除，可按旧 tag 回滚；`release` 分支可从任一历史
Release 的 `srs.tar.gz` 重建。

## 11. 验证策略

### 单元与 property tests

- CSV quoted records、精确 header/列数、所有非法 op/type/value/attrs；
- domain/regex/CIDR canonicalization 的表驱动与 fuzz tests；
- identity、wildcard Del、warning code、冲突检测；
- protobuf bool/int attrs、unknown fields、GeoIP reverse_match round trip；
- 确定性 marshal、JSON、tar 与 checksum；
- sing-box list parser 的格式漂移与恶意路径输入。

### Fixture integration

- GeoSite NEW、PATCH Add/Del、attrs 精确 Del、无 attrs 全变体 Del、整数 attrs
  保留、missing/existing warnings；
- GeoIP IPv4/IPv6 host-bit 清理、重叠保留、exact Del；
- 空四目录构建；
- fake GitHub server 覆盖 ID/latest、分页、404、限流、截断下载、digest/checksum
  不一致与 mutable response；
- fake external tools 覆盖非零退出、超时、缺 category、额外 category、坏 JSON/SRS。

### Locked-tool end-to-end

- 固定一个真实 upstream Release ID 作为非每日单元测试的集成基线；
- 执行两次 clean build 并比较全部 artifact hash；
- Mihomo 和 sing-box smoke tests；
- 注入 draft/upload/branch push/publish/compensation 各阶段失败，验证不会留下
  可见错配；
- 验证相同 tag，以及 upstream ID + `release_inputs_hash` 相同的 docs-only commit，
  都能快速退出且没有 setup/download/build 步骤。

## 12. 安全、资源与兼容性

- GitHub token 仅通过环境变量读取；HTTP 设置超时、有限重试和最大响应/资产
  size；重试 404/校验失败没有意义，429/5xx 按退避重试。
- 所有 category 都先经过路径安全校验，再用于临时文件；不将外部名称直接拼接
  shell，外部命令用 `exec.CommandContext` 参数数组并有超时。
- 临时目录权限最小化并在成功/失败后清理；构建不执行上游下载内容。
- 全量 SRS 数量较多，v1 采用可配置但有上限的 worker pool；DAT patch 保持内存中
  单个 dataset 的一份主模型。耗时只写 CI 日志，不进入要求可复现的 manifest。
- manifest/CSV/lock/config 都有 schema version 或严格未知字段策略。v1 对 config
  与 lock 使用 `KnownFields(true)`；破坏性变化提升 schema version。

## 13. 已知风险与决策

- sing-box 的 Geo 命令属于 legacy/tooling surface，未来可能移除；锁定版本可保持
  v1 可构建，升级必须跑真实 DAT E2E。若未来工具消失，另开设计迁移，不在 v1
  静默换成自研转换。
- MetaCubeX meta-rules-converter 的 attribute 展开与逐分类 JSON 是关键外部契约；
  通过锁版本、独立期望 index、JSON parse 和含 bool/int attrs/重叠 GeoIP fixture
  控制风险。其自带 SRS 不进入发布。
- Release/branch 无法真原子；使用 draft、branch-first、force-with-lease 和补偿，
  并在 manifest identity 上做最终一致性断言。
- 每小时调度可能延迟，产品承诺是周期性检查而非准点 SLA；手动 dispatch 是恢复
  通道。
