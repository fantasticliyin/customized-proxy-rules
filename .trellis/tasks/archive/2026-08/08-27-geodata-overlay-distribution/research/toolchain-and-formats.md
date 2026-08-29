# Toolchain and format research

> Planning snapshot: 2026-08-28. Versions below are recommendations for the
> initial lock file, not floating dependencies. Updating them requires a normal
> pull request and the full compatibility test suite.

## Conclusions

1. The patcher should read and write V2Ray GeoData protobuf directly using the
   generated types from `github.com/metacubex/geo/encoding/v2raygeo` and
   deterministic `google.golang.org/protobuf` marshaling. This keeps the
   semantic patch at the canonical DAT layer without creating another schema.
2. `sing-box geosite` and `sing-box geoip` do **not** consume V2Ray DAT files
   directly. They consume sing-geosite DB and sing-geoip MMDB respectively.
   The initially considered chain was:

   ```text
   final geosite.dat
     -> geo convert site -i v2ray -o sing
     -> sing-box geosite list/export
     -> sing-box rule-set compile

   final geoip.dat
     -> geo convert ip -i v2ray -o sing
     -> sing-box geoip list/export
     -> sing-box rule-set compile
   ```

3. MetaCubeX `geo` materializes GeoSite attribute views as names such as
   `category@attribute` in the sing-geosite database. The pipeline should use
   `sing-box geosite list` as the authoritative export index instead of
   rebuilding the attribute-view index itself.
4. **The sing-geoip MMDB intermediate is not lossless for this product.** A
   sing-geoip/MMDB lookup holds one result for a network, while the V2Ray DAT
   can place the same/overlapping range in multiple categories (for example a
   provider category and a country category). MetaCubeX's own release workflow
   therefore generates per-category GeoIP SRS directly from V2Ray DAT rather
   than exporting those SRS from `geoip.db`. The final pipeline must not use
   `geo convert ip -> sing-box geoip export` as the authoritative full-SRS path.
5. The simpler lossless existing-tool route is to pin MetaCubeX
   `meta-rules-converter`, let it export one sing-box JSON rule-set per V2Ray
   category, discard its internally compiled SRS, and compile each JSON with
   the separately pinned current sing-box. It supports both GeoSite (including
   `category@attribute` views) and GeoIP without collapsing cross-category
   membership. Recommended initial commit:
   `7dea27841a3579a633189830c98c08a0434e8b79`, pseudo-version
   `v0.0.0-20251201061744-7dea27841a35`.
6. The initial toolchain should pin immutable versions and SHA-256 values. A
   good Linux amd64 baseline at the planning date is:
   - sing-box `v1.13.19`, asset
     `sing-box-1.13.19-linux-amd64-glibc.tar.gz`, SHA-256
     `77e26226c111b8a269f559aec7999f6f5ae1961f25374b58b126d06405d4f516`.
   - Mihomo `v1.19.30`, asset
     `mihomo-linux-amd64-compatible-v1.19.30.gz`, SHA-256
     `db214c7a2517e63c150d123178d16d102e03a241ccdae4e5e07ffbe9cf56c6f9`.
   - MetaCubeX `meta-rules-converter` commit and pseudo-version from conclusion
     5. The older `geo` pin remains useful for comparison tests, not as the
     authoritative GeoIP SRS path.
7. Both MetaCubeX conversion executables report an internal application version
   rather than sufficient build provenance. The build must record and verify
   the pinned Go module pseudo-version and commit, not rely on runtime output.
8. The current `MetaCubeX/meta-rules-dat` `latest` tag is mutable. Build identity
   must use the immutable GitHub Release database ID plus each asset digest,
   never only the tag name.
9. The GitHub Release API exposes an asset `digest`, and the upstream release
   also publishes paired `.sha256sum` assets. The downloader should require
   both checks to agree with the downloaded DAT.
10. The upstream and conversion tools use GPL-compatible licensing; the
   repository-wide decision is `GPL-3.0-only` with explicit upstream
   attribution.

## Format details that affect implementation

### V2Ray GeoSite

- Domain types are `Plain`, `Regex`, `RootDomain`, and `Full`.
- Attributes can be boolean or integer. The patch CSV deliberately exposes only
  boolean attributes in v1, but integer attributes from upstream must survive a
  decode/encode round trip unchanged.
- Domain entry order and category order are meaningful for minimal, reviewable
  changes even though lookup semantics are mostly set-like.

### V2Ray GeoIP

- Entries are stored as address bytes plus prefix length.
- Category-level fields such as `reverse_match` must survive patching even
  though they are not represented in the CSV format.
- CIDR normalization is a patcher responsibility; conversion tools are not the
  validation boundary for user input.

### sing-box export and compile

- `geosite export` and `geoip export` produce sing-box headless rule-set JSON,
  but only from their respective legacy databases, not V2Ray DAT.
- `rule-set compile` is the supported JSON-to-SRS compiler.
- For this product, MetaCubeX `meta-rules-converter` should produce the
  per-category JSON from final DAT; pinned sing-box remains the SRS compiler and
  validator.

## Workflow and publication constraints

- GitHub scheduled workflows have a five-minute minimum interval, run from the
  default branch, and may be delayed under load—especially near the start of an
  hour. `17 * * * *` is a suitable hourly polling schedule.
- Public-repository scheduled workflows may be disabled after extended
  inactivity. The README should mention this operational property.
- A GitHub Release and a force-updated branch are separate GitHub mutations and
  cannot be made truly atomic. The workflow therefore needs a staged draft
  release, an exact previous `release` branch ref, and compensating rollback.

## Recommended trust boundaries

| Boundary | Validation owner |
| --- | --- |
| GitHub API response | upstream resolver; require exact repository, release ID and asset names |
| Downloaded DAT | downloader; API digest + paired checksum + size/non-empty checks |
| CSV | `rulescsv`; exact header, type, value, canonical identity and conflict checks |
| Protobuf DAT | `geodat`; strict unmarshal, invariant checks and reload after deterministic marshal |
| External tool | toolchain manager; pinned version/provenance and SHA-256 |
| Converter output | `srs`; command success, complete category index and JSON/SRS validation |
| Distribution | `validate`; manifest, checksums, DAT/SRS index and consumer smoke tests |
| GitHub publication | workflow; draft staging, concurrency lock and compensation |

## Primary sources

- [sing-box command source and documentation](https://github.com/SagerNet/sing-box)
- [sing-box v1.13.19 release](https://github.com/SagerNet/sing-box/releases/tag/v1.13.19)
- [MetaCubeX geo source](https://github.com/MetaCubeX/geo)
- [MetaCubeX geo conversion package](https://github.com/MetaCubeX/geo/tree/master/convert)
- [MetaCubeX meta-rules-converter](https://github.com/MetaCubeX/meta-rules-converter)
- [MetaCubeX meta-rules-dat conversion workflow](https://github.com/MetaCubeX/meta-rules-dat/blob/master/.github/workflows/run.yml)
- [MetaCubeX meta-rules-dat GeoIP SRS script](https://github.com/MetaCubeX/meta-rules-dat/blob/master/resouces/convert.sh)
- [Mihomo v1.19.30 release](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.30)
- [meta-rules-dat releases](https://github.com/MetaCubeX/meta-rules-dat/releases)
- [GitHub Actions scheduled-event documentation](https://docs.github.com/actions/using-workflows/events-that-trigger-workflows#schedule)
- [GitHub REST API release-asset schema](https://docs.github.com/rest/releases/assets)
