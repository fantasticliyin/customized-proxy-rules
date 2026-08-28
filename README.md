# customized-proxy-rules

本仓库以少量 CSV overlay 维护自定义 GeoSite / GeoIP 规则，并持续基于
[`MetaCubeX/meta-rules-dat`](https://github.com/MetaCubeX/meta-rules-dat) 发布的
V2Ray GeoData 生成同一版本的 Mihomo DAT 与 sing-box SRS。

数据链固定为：上游 `geosite.dat` / `geoip.dat` → 语义 Patch → 最终 DAT →
逐分类 sing-box JSON → SRS。仓库不会复制或重新运行上游完整规则构建工程。

## 获取产物

GitHub Release 是不可变的审计与回滚来源，包含：

- `geosite.dat`、`geoip.dat`
- `srs.tar.gz`
- `manifest.json`、`SHA256SUMS`

`release` 分支是 latest-only 的干净快照，提供 `geosite/<category>.srs`、
`geoip/<category>.srs` 及对应 manifest/checksum。Release tag 格式为
`u<upstream-release-id>-c<12位本仓库commit>`。

Mihomo 可将 Release 中的 DAT 配置为 GeoData 数据源；sing-box 可直接把
`release` 分支中的单分类 `.srs` 配置为远程 rule-set。使用前应按
`SHA256SUMS` 校验文件。

## 维护规则

正式规则只放在 `rules/` 的四个目录。首版目录有意保持为空；语法与工具用法见
[rule-patcher 文档](tools/rule-patcher/docs/rules.md)。示例和测试 fixture 不会进入
公开规则。

自动任务在每小时第 17 分钟检查上游，也可手工指定上游 Release database ID。
GitHub 可能延迟定时任务；公开仓库长时间无活动时 GitHub 也可能停用 schedule，
这不是准点 SLA。

## 来源与许可证

上游数据来自 `MetaCubeX/meta-rules-dat`，转换器来自 MetaCubeX，SRS 使用
sing-box 官方编译器生成。产物 manifest 和 Release notes 会保留来源、版本与
校验信息。本仓库整体使用 `GPL-3.0-only`，详见 [LICENSE](LICENSE)。
