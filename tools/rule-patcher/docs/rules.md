# Rule patcher 使用说明

## CSV

Category 由小写安全文件名决定。文件必须是 UTF-8（无 BOM）的标准 CSV，表头与
列数严格固定；`note` 仅供审阅，不参与 identity。
Category 可使用小写字母、数字、`_`、`-` 和 `!`（例如
`category-ai-!cn.csv`）；`@` 仍保留给派生 attribute 视图，不得出现在输入文件名中。

GeoSite：

```csv
op,type,value,attrs,note
+,DOMAIN,example.com,,精确域名
+,DOMAIN-SUFFIX,example.org,@ads @mobile,"带布尔 attributes, 可使用 quoted note"
-,DOMAIN-KEYWORD,tracker,,删除所有 attribute 变体
```

GeoIP：

```csv
op,type,value,note
+,IP-CIDR,192.0.2.7/24,规范化为 192.0.2.0/24
+,IP-CIDR,2001:db8::/32,IPv6 自动识别
```

`new` 只接受 `+`；`patch` 接受 `+`/`-`。GeoSite type 只接受 `DOMAIN`、
`DOMAIN-SUFFIX`、`DOMAIN-KEYWORD`、`DOMAIN-REGEX`。attrs 是空格分隔的
`@name`，v1 只支持布尔值。

带 attrs 的 GeoSite Del 只删除完整布尔 attribute 集相同的 entry；不带 attrs 的
Del 删除同 type/value 的全部变体（包括上游整数 attributes）。GeoIP Del 只删除
完全相同的规范 CIDR，不做网段合并或包含消除。

Add 已存在、Del 不存在、PATCH category 不存在以及 NEW 与上游重名不会导致构建
失败，而是写入结构化 warning。相同 identity 同时 Add/Del 属于输入冲突并失败。

## CLI

在 `tools/rule-patcher/` 下运行：

```sh
go run ./cmd/rule-patcher build --upstream-release-id 377547544 --commit <full-sha>
go run ./cmd/rule-patcher inspect --dataset geosite --dat ../../dist/geosite.dat --category google --attribute ads --format json
go run ./cmd/rule-patcher diff --dataset geoip --before old.dat --after new.dat --format json
go run ./cmd/rule-patcher validate --dist ../../dist --config config.yaml --toolchain-lock toolchain.lock.yaml
```

`build` 会严格读取 `config.yaml` 与 `toolchain.lock.yaml`，在 cache 中下载并验证
锁定的 sing-box/Mihomo、构建锁定 converter，然后依次处理 GeoSite 和 GeoIP。
使用本地 `--geosite`/`--geoip` 且未提供 `--generated-at` 或
`SOURCE_DATE_EPOCH` 时，构建使用确定性的 `1970-01-01T00:00:01Z`；生产构建仍使用
上游 Release 的发布时间。显式时间必须晚于 Unix epoch。
最终 `geoip.dat` 会通过已锁定的 MetaCubeX `geo` 转换库生成
`geoip.metadb`；MMDB `build_epoch` 固定为 manifest 的 `generated_at`，因此相同输入
可以复现相同字节。MetaDB 保留同一网段所属的多个 GeoIP 分类，不参与 SRS 生成；
若 DAT 含 MetaDB 无法表达的 `reverse_match` 或空分类，构建会在发布前失败。
单个 DAT 默认最多 128 MiB；下载是流式的，protobuf Patch 是逐 dataset 有界内存
处理。`validate` 会从磁盘重读完整产物，并再次使用锁定 sing-box 解码全部 SRS、
使用锁定 Mihomo 分别加载 DAT 与 MetaDB，并逐 CIDR 对照 MetaDB 和最终 DAT 的分类
语义。退出码：成功 0、外部网络/工具/I/O 故障 1、命令用法、输入或产物校验失败
2。

工具升级必须在 pull request 中更新 lock，重新核验 URL/SHA-256/module commit，
并运行单元测试、race/vet、固定真实上游双构建及 Mihomo/sing-box smoke test。

## 发布与回滚

发布先创建 draft Release、上传并校验资产，再以 force-with-lease 更新 orphan
`release` 分支，最后公开 Release。失败处理只按本次 Release database ID 与精确
branch SHA 补偿。历史成功 Release 不改写；回滚时用历史 `srs.tar.gz` 重建
`release` 快照，并暂停 schedule，修复通过全门禁后发布新 tag。

GitHub Release 的根资产为 `geosite.dat`、`geoip.dat`、`geoip.metadb`、
`srs.tar.gz`、`manifest.json` 和 `SHA256SUMS`。manifest schema 2 要求 MetaDB；
validator 仍接受不含 MetaDB 的历史 schema 1。`release` 分支继续仅包含逐分类 SRS、
manifest 和分支 checksum，不复制 DAT/MetaDB。
