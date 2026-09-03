# GeoIP MetaDB 调研

## 已确认事实

1. MetaCubeX `geo` 官方命令支持：

   ```sh
   geo convert ip -i v2ray -o meta -f geoip.metadb geoip.dat
   ```

   项目当前直接依赖的 `github.com/metacubex/geo` 版本
   `v0.0.0-20240718103914-a4db326ccfd7` 提供同一转换函数
   `convert.V2RayIPToMetaV0`，输出 database type 为 `Meta-geoip0`。

2. MetaDB 与 sing-geoip DB 不同：同一个 IP 前缀可以返回多个分类。这对上游国家、
   服务商和本仓自定义分类互相重叠的场景是必需的，因此不能用 `geoip.db` 替代。

3. 官方转换函数使用 `mmdbwriter.New`，没有暴露 `BuildEpoch` 参数；writer 默认
   使用 `time.Now().Unix()`。直接调用 CLI 或函数会令相同输入在不同时刻生成不同
   字节，与现有可复现构建契约冲突。

4. `mmdbwriter.Load` 可以载入已生成的 MetaDB，并通过 Options 覆盖
   `BuildEpoch`、database type、IP version 和 record size 后重新写出。因此可以
   先使用官方转换函数生成语义结果，再做一次确定性规范化，而不复制或 fork
   MetaCubeX 的转换算法。

5. Mihomo 在 `geodata-mode: false` 时从 `geoip.metadb` 加载 MMDB；
   `Meta-geoip0` reader 会将字符串或字符串数组作为 GeoIP code 集合。

## 推荐结论

- 在 `rule-patcher` 内新增窄职责的 `internal/metadb`：读取最终磁盘 DAT，调用官方
  `V2RayIPToMetaV0`，再以已有 `generated_at` 规范化 MMDB metadata，并原子写出。
- 不新增 `geo` 外部二进制，也不扩展 `toolchain.lock.yaml`。官方转换库已经由
  `go.mod` 锁定，依赖版本同时被 `custom_commit` 和 `release_inputs_hash` 锚定。
- manifest schema 升为 2：schema 2 要求 `geoip.metadb`；validator 保留 schema 1
  读取路径，使历史 Release 仍可核验。

## 主要代码证据

- `tools/rule-patcher/go.mod`：已锁定 `github.com/metacubex/geo`。
- `tools/rule-patcher/cmd/rule-patcher/main.go`：最终 GeoIP DAT 在 SRS 与 manifest
  之前写回，可在这里派生 MetaDB。
- `tools/rule-patcher/internal/artifact/artifact.go`：Release 文件 inventory、manifest
  与 checksum 的所有权边界。
- `tools/rule-patcher/internal/validate/validate.go`：发布目录与消费者校验边界。
- `.github/workflows/release.yml`：draft 上传、asset digest 校验和失败补偿事务。

## 上游资料

- https://github.com/MetaCubeX/geo/blob/master/README.md
- https://github.com/MetaCubeX/geo/blob/master/convert/v2ray_ip.go
- https://wiki.metacubex.one/config/general/
