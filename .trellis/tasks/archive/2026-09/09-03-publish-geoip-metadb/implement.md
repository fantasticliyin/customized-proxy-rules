# 发布 GeoIP MetaDB — 实施计划

## 1. 确定性转换层

- 新增 `internal/metadb`，封装 official V2Ray DAT → Meta-geoip0 转换、固定
  `build_epoch` 的规范化与原子落盘。
- 增加普通 IPv4/IPv6、重叠分类、非法/空输入、固定 epoch 和重复构建字节一致测试。
- 将实际使用的 MMDB reader/writer 依赖提升为 `go.mod` 直接依赖，不升级版本。

## 2. 接入构建链

- 在 `cmd/rule-patcher/main.go` 中提前解析确定性 timestamp。
- final `geoip.dat` 写回后，从磁盘 DAT 生成 `dist/geoip.metadb`。
- 保持 SRS 从 final DAT 生成，MetaDB 不成为中间权威源。
- 扩展端到端 fixture，确认整个 dist 双构建仍完全一致。

## 3. Artifact 与 manifest v2

- `artifact.Finalize` 将 `geoip.metadb` 纳入 `manifest.files` 和根
  `SHA256SUMS`，但不复制到 branch snapshot。
- 新构建输出 manifest schema 2；reader 接受 schema 1 与 2。
- validator 根据 schema 选择四项/五项 checksum 契约，并验证 schema 2 的 MetaDB
  metadata 与 DAT 语义；增加 schema 1 历史兼容测试。
- 扩展报告文件计数和所有 fixture。

## 4. Mihomo 消费验证

- 增加 MetaDB 模式 smoke helper：将生成文件放入 Mihomo 数据目录，以
  `geodata-mode: false` 加载 `GEOIP,tailscale`/代表分类。
- 独立用 MMDB reader 验证 Tailscale IPv4/IPv6 查询结果，避免只证明文件可打开。

## 5. GitHub 发布事务

- Workflow draft upload 加入 `geoip.metadb`。
- 上传后 asset size/digest 校验 loop 加入该文件。
- Release notes 输出 MetaDB SHA-256。
- 保持 `release` 分支工作树内容不变，并用测试锁定 SRS-only 边界。

## 6. 文档

- README 更新数据链、Release 资产清单、DAT/MetaDB 使用示例。
- `tools/rule-patcher/docs/rules.md` 更新构建/验证产物和发布回滚说明。

## 7. 验证命令

```sh
gofmt -w tools/rule-patcher/cmd tools/rule-patcher/internal
go -C tools/rule-patcher mod tidy
go -C tools/rule-patcher test ./...
go -C tools/rule-patcher test -race ./...
go -C tools/rule-patcher vet ./...
git diff --check
```

随后使用固定真实上游 Release 构建两次并比较完整 dist hash，检查：

```sh
sha256sum dist/geoip.metadb
jq '.schema_version, .files["geoip.metadb"]' dist/manifest.json
sha256sum -c dist/SHA256SUMS
```

最后验证本地 Mihomo MetaDB 模式以及 Workflow 资产列表/输入哈希触发范围测试。

## 8. 风险点与回滚点

- `internal/metadb` 规范化输出与官方直接输出的 lookup 语义必须在合入前对照。
- manifest schema 兼容分支必须在 artifact 变更同一提交中完成，不能出现只能生成、
  不能验证的中间状态。
- Workflow 只在所有本地测试通过后修改；不在实施阶段创建 Release 或更新远程分支。
