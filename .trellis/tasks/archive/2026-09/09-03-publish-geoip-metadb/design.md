# 发布 GeoIP MetaDB — 技术设计

## 1. 架构与边界

保持 `geoip.dat` 为唯一权威 GeoIP 语义源，新增产物只是从 Patch 后 DAT 派生：

```text
upstream geoip.dat
       │
       ▼
CSV semantic patch
       │
       ▼
final geoip.dat ──► MetaCubeX V2RayIPToMetaV0 ──► deterministic normalize
       │                                                   │
       ├──► per-category SRS                               ▼
       │                                             geoip.metadb
       └──────────────────────► validate + manifest + SHA256SUMS
                                                          │
                                                          ▼
                                          immutable GitHub Release
```

`geoip.metadb` 不进入 `release` 分支；该分支继续只含逐分类 SRS、manifest 和
分支内 checksum。这样延续已确定的“DAT 类资产走 GitHub Release，SRS latest
快照走 release 分支”边界。

## 2. 生成方式

新增 `tools/rule-patcher/internal/metadb`，职责限定为：

1. 从磁盘重新载入最终 `geoip.dat`，保证输入是已经写回并通过 protobuf reload
   的发布字节，而不是旁路使用内存中的 Patch 中间态。
2. 调用已锁定 `github.com/metacubex/geo/convert.V2RayIPToMetaV0` 写入同目录临时
   MetaDB，保留 MetaCubeX 官方的多分类/重叠 CIDR 语义。
3. 使用 `mmdbwriter.Load` 重新载入临时 MetaDB，并显式设置：
   `DatabaseType=Meta-geoip0`、`IPVersion=6`、`RecordSize=24`、
   `BuildEpoch=generated_at.Unix()`、禁用 IPv4 alias、允许保留网段。
4. 将规范化结果写入临时文件，关闭并校验后原子 rename 为
   `dist/geoip.metadb`；任何错误都让整个 staging build 失败。

选择“官方转换 + 确定性规范化”，而不是：

- **新增 `geo` CLI：** 需要增加第四个外部工具锁，且 CLI 同样使用墙钟
  `build_epoch`，不能直接满足可复现构建。
- **复制官方转换算法：** 代码更少一次 I/O，但会形成需要手工跟进上游的 fork。
- **生成 sing `geoip.db`：** 单值结果会丢失重叠网段的多分类成员关系。

`mmdbwriter` 和 `maxminddb-golang` 从间接依赖提升为直接依赖，但版本不升级；
`toolchain.lock.yaml` 不变。依赖版本由 `go.mod`/`go.sum` 锁定，并已纳入
`release_inputs_hash`。

## 3. 构建顺序

将当前 `generated_at` 解析提前到写完 final DAT 之后、所有派生产物之前：

1. 下载与校验上游 DAT；
2. 应用 GeoSite/GeoIP Patch并确定性写回；
3. 解析 `--generated-at` / `SOURCE_DATE_EPOCH`；
4. 从 final `geoip.dat` 生成 `geoip.metadb`；
5. 生成 SRS；
6. 生成 manifest、`SHA256SUMS` 与 SRS-only branch snapshot；
7. 完整 validate 后原子提交 dist。

生成步骤继续受 staging 目录、总体命令 context 和文件大小上限保护，不会留下半成品
dist。

## 4. Manifest 与兼容性

发布资产从五个文件增加为六个：

```text
geosite.dat
geoip.dat
geoip.metadb
srs.tar.gz
manifest.json
SHA256SUMS
```

`manifest.files` 加入 `geoip.metadb` 的 size 与 SHA-256；`SHA256SUMS` 从四条增为
五条，仍不包含自身以避免递归哈希。

required asset 集合发生变化，因此新构建写 `schema_version: 2`。读取与验证规则：

- schema 1：历史契约，允许缺少 `geoip.metadb`；
- schema 2：必须包含并完整校验 `geoip.metadb`；
- 其他 schema：拒绝。

不新增 manifest tool 字段：MetaDB 生成逻辑在本仓 rule-patcher 内，
`custom_commit` 锚定实现，`release_inputs_hash` 锚定包含 `go.mod`/`go.sum` 的依赖。

## 5. 验证契约

`internal/metadb` 提供生成和校验边界。schema 2 的 `validate` 至少执行：

- 文件为非空普通文件且大小受限；
- MaxMind DB 可以打开；metadata 必须为 `Meta-geoip0`、IPv6 tree、24-bit record，
  `build_epoch` 等于 manifest `generated_at`；
- 对 final DAT 中每个 CIDR，在前缀起点和末端查询 MetaDB，结果必须包含所属
  category；所有返回 code 必须属于 final DAT category 集；
- 重叠前缀 fixture 必须证明单个查询能同时返回多个 code；
- `tailscale` fixture/真实规则验证 IPv4 `100.64.0.0/10` 和 IPv6
  `fd7a:115c:a1e0::/48`；
- 锁定 Mihomo 以 `geodata-mode: false`、本地 `geoip.metadb` 和一条 GEOIP 规则
  执行配置加载 smoke test。

相同输入、相同 `generated_at` 的两次生成必须字节一致；不同墙钟执行时间不能影响
输出。schema 1 验证路径不要求 MetaDB，保障历史 Release 可审计。

## 6. 发布事务

`.github/workflows/release.yml` 的 draft upload 和 asset digest loop 都加入
`publish/dist/geoip.metadb`。文件上传或 size/digest 不匹配时触发现有 compensation，
不得公开 Release，也不得留下已更新的 `release` 分支。

快速退出 identity 不需要特殊字段：本次实现更改会改变 custom commit 与
`release_inputs_hash`，同一个上游 Release 会生成新的 tag 和完整六资产 Release。

## 7. 文档与使用方式

README 将 Release 清单增加 `geoip.metadb`，并说明：

```yaml
geodata-mode: false
geox-url:
  mmdb: https://github.com/fantasticliyin/customized-proxy-rules/releases/latest/download/geoip.metadb
```

DAT 用户继续使用 `geodata-mode: true` 与 `geox-url.geoip`。文档明确 MetaDB 和 DAT
是同一 Patch 结果的两种 Mihomo 消费格式，而 `release` 分支仍只承载 SRS。

## 8. 风险与回滚

- **MMDB 规范化产生语义漂移：** 通过 DAT 全量 CIDR endpoint 查询、重叠 CIDR
  fixture 和 Mihomo smoke 阻断。
- **非确定性 metadata 回归：** 固定 epoch 并保留双构建 HashTree 测试。
- **发布清单遗漏：** artifact/validator/workflow 三层均以显式资产集合断言。
- **历史发布不可验证：** schema 1/2 双读取路径覆盖回归测试。

回滚实现提交即可；已经发布的历史 Release 不修改或删除。若新 Release 在公开前任一
MetaDB 门禁失败，沿用现有 draft Release 与精确 ref 补偿流程。
