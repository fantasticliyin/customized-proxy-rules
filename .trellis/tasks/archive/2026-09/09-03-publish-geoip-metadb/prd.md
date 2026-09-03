# 发布 GeoIP MetaDB 产物

## Goal

在现有 Patch 后 GeoIP DAT 发布链中增加可供 Mihomo 直接使用的
`geoip.metadb`，避免使用者另外下载上游 MetaDB 而丢失本仓库新增的 GeoIP
分类（例如 `tailscale`）。

## Background

- `geoip.dat` 是 Patch 后 GeoIP 数据的唯一语义来源。
- 当前 GitHub Release 仅发布 `geosite.dat`、`geoip.dat`、`srs.tar.gz`、
  `manifest.json` 和 `SHA256SUMS`。
- 项目已锁定并构建 `meta-rules-converter`，但尚未生成或校验 MetaDB。
- 既有产品边界已确定：DAT 类产物由不可变 GitHub Release 发布，`release`
  分支只提供 latest-only SRS 快照，不包含 DAT。

## Requirements

- 从最终、已 Patch 的 `geoip.dat` 确定性转换得到 `geoip.metadb`，不得绕过
  Patch 再从上游资产生成。
- 相同 DAT、规则、工具依赖与 `generated_at` 必须产生字节相同的 MetaDB；MMDB
  内的 `build_epoch` 使用已有确定性 `generated_at`，不得取构建机墙钟时间。
- `geoip.metadb` 必须包含最终 `geoip.dat` 的全部 GeoIP 分类与 CIDR 语义，至少
  验证本仓库新增的 `tailscale`。
- 将 `geoip.metadb` 纳入发布资产、manifest、checksum 和发布后身份校验。
- 新 manifest 明确表达 MetaDB 为必需资产，同时继续允许校验不含 MetaDB 的历史
  manifest，以保留历史 Release 审计与回滚能力。
- 构建失败或 MetaDB 校验失败时不得公开 Release 或更新 `release` 分支。
- 更新用户文档，给出 Mihomo `geox-url.mmdb` 的使用方式。

## Acceptance Criteria

- [x] 同一构建中，最终 `geoip.dat` 能生成有效的 `geoip.metadb`。
- [x] 重复构建得到字节相同的 `geoip.metadb`，其 `build_epoch` 等于 manifest 的
  `generated_at`。
- [x] 生成的 MetaDB 为 `Meta-geoip0`，保留重叠 CIDR 的多分类结果；Mihomo 能
  加载它，且查询可识别 `tailscale` 的 IPv4、IPv6 网段。
- [x] GitHub Release 包含 `geoip.metadb`，其 SHA-256 同时记录在 manifest、
  `SHA256SUMS` 与 GitHub Release asset digest 中。
- [x] 发布前本地验证和上传后远程验证均覆盖 `geoip.metadb`；任一失败时发布事务
  按现有补偿逻辑回滚。
- [x] README 与工具文档说明 DAT/MetaDB/SRS 的用途、下载位置和 Mihomo 配置。
- [x] 新 validator 能严格校验新 manifest/MetaDB，也能继续校验历史 schema 1
  发布目录；回归、race 与 vet 全部通过。

## Out of Scope

- 不改变 CSV Patch 语法或 GeoIP 分类内容。
- 不从 MMDB/MetaDB 反向生成 `geoip.dat`。
- 不改变 GeoSite 产物格式。
- 不引入独立于现有 Release 的第二套发布流程。
- 不把 `geoip.metadb` 放入 SRS-only 的 `release` 分支。
- 不发布 `geoip.db`（sing-geoip 单结果格式）。
