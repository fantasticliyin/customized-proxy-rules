// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/artifact"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/config"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/metadb"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/overlay"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/rulescsv"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/srs"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/toolchain"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/upstream"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/validate"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

var errUsage = errors.New("usage error")
var errInput = errors.New("input or validation error")
var hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "build":
		err = runBuild(args[1:], stdout, stderr)
	case "inspect":
		err = runInspect(args[1:], stdout, stderr)
	case "diff":
		err = runDiff(args[1:], stdout, stderr)
	case "validate":
		err = runValidate(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		if errors.Is(err, errUsage) || errors.Is(err, errInput) {
			return 2
		}
		return 1
	}
	return 0
}
func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: rule-patcher <build|inspect|diff|validate> [options]")
}

func runBuild(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.yaml", "config YAML")
	lockPath := fs.String("toolchain-lock", "toolchain.lock.yaml", "toolchain lock YAML")
	outputPath := fs.String("output", "", "distribution output directory (defaults to config)")
	tokenEnv := fs.String("github-token-env", "GITHUB_TOKEN", "environment variable containing the GitHub token")
	releaseID := fs.String("upstream-release-id", "", "immutable GitHub Release database ID; empty means latest")
	localSite := fs.String("geosite", "", "local upstream geosite.dat (testing/offline)")
	localIP := fs.String("geoip", "", "local upstream geoip.dat (testing/offline)")
	converter := fs.String("converter", "meta-rules-converter", "pinned converter executable")
	singBox := fs.String("sing-box", "sing-box", "pinned sing-box executable")
	mihomo := fs.String("mihomo", "mihomo", "pinned Mihomo executable")
	commit := fs.String("commit", "", "custom full git commit")
	inputHash := fs.String("release-inputs-hash", "", "precomputed release input hash")
	generatedAt := fs.String("generated-at", "", "deterministic RFC3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: build accepts no positional arguments", errUsage)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	if *outputPath != "" {
		cfg.DistDir, err = filepath.Abs(*outputPath)
		if err != nil {
			return fmt.Errorf("%w: invalid output path: %v", errInput, err)
		}
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(*tokenEnv) {
		return fmt.Errorf("%w: invalid GitHub token environment variable name", errUsage)
	}
	lock, err := config.LoadToolchain(*lockPath)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	if (*localSite == "") != (*localIP == "") {
		return fmt.Errorf("%w: --geosite and --geoip must be supplied together", errUsage)
	}
	localBuild := *localSite != ""
	if !localBuild && (*converter != "meta-rules-converter" || *singBox != "sing-box" || *mihomo != "mihomo") {
		return fmt.Errorf("%w: production builds cannot override locked tool executables", errUsage)
	}
	if _, err := os.Lstat(cfg.DistDir); err == nil {
		return fmt.Errorf("output directory already exists: %s", cfg.DistDir)
	} else if !os.IsNotExist(err) {
		return err
	}
	if *commit == "" {
		*commit = gitCommit()
	}
	if !localBuild && !commitPattern.MatchString(*commit) {
		return fmt.Errorf("%w: custom commit must be a lowercase 40-character git SHA", errInput)
	}
	if localBuild && len(*commit) < 12 {
		return fmt.Errorf("%w: custom commit must contain at least 12 characters", errInput)
	}
	computedInputHash, err := releaseInputsHash(cfg.RulesDir, *configPath, *lockPath)
	if err != nil {
		return err
	}
	if *inputHash == "" {
		*inputHash = computedInputHash
	} else if !strings.EqualFold(*inputHash, computedInputHash) {
		return fmt.Errorf("%w: supplied release inputs hash does not match the checked-out publication inputs", errInput)
	}
	if !hexDigestPattern.MatchString(strings.ToLower(*inputHash)) {
		return fmt.Errorf("%w: release inputs hash must be 64 hexadecimal characters", errInput)
	}
	if !localBuild || checkExecutable(*converter) != nil || checkExecutable(*singBox) != nil || checkExecutable(*mihomo) != nil {
		prepareCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		paths, err := toolchain.Prepare(prepareCtx, lock, cfg.CacheDir)
		if err != nil {
			return err
		}
		if !localBuild || *converter == "meta-rules-converter" {
			*converter = paths.Converter
		}
		if !localBuild || *singBox == "sing-box" {
			*singBox = paths.SingBox
		}
		if !localBuild || *mihomo == "mihomo" {
			*mihomo = paths.Mihomo
		}
		if err := checkExecutable(*converter); err != nil {
			return err
		}
		if err := checkExecutable(*singBox); err != nil {
			return err
		}
		if err := checkExecutable(*mihomo); err != nil {
			return err
		}
	}
	parent := filepath.Dir(cfg.DistDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".rule-patcher-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	dist := filepath.Join(stage, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return err
	}
	prov := upstream.Provenance{Repository: cfg.Upstream.Repository, ReleaseID: 0, TagName: "local", PublishedAt: time.Unix(0, 0).UTC()}
	if *localSite != "" {
		prov.GeoSiteSHA256, err = copyFileLimited(*localSite, filepath.Join(dist, "geosite.dat"), cfg.MaxDATBytes)
		if err != nil {
			return err
		}
		prov.GeoIPSHA256, err = copyFileLimited(*localIP, filepath.Join(dist, "geoip.dat"), cfg.MaxDATBytes)
		if err != nil {
			return err
		}
	} else {
		client := upstream.NewClient(os.Getenv(*tokenEnv))
		release, err := client.Resolve(context.Background(), cfg.Upstream.Repository, *releaseID)
		if err != nil {
			return err
		}
		assets, err := upstream.SelectAssets(release, []string{cfg.Upstream.Assets.GeoSite, cfg.Upstream.Assets.GeoSiteChecksum, cfg.Upstream.Assets.GeoIP, cfg.Upstream.Assets.GeoIPChecksum})
		if err != nil {
			return err
		}
		prov.ReleaseID, prov.TagName, prov.PublishedAt = release.ID, release.TagName, release.PublishedAt
		prov.GeoSiteAsset = upstream.Evidence(assets[cfg.Upstream.Assets.GeoSite])
		prov.GeoSiteChecksum = upstream.Evidence(assets[cfg.Upstream.Assets.GeoSiteChecksum])
		prov.GeoIPAsset = upstream.Evidence(assets[cfg.Upstream.Assets.GeoIP])
		prov.GeoIPChecksum = upstream.Evidence(assets[cfg.Upstream.Assets.GeoIPChecksum])
		prov.GeoSiteSHA256, err = client.DownloadVerified(context.Background(), assets[cfg.Upstream.Assets.GeoSite], assets[cfg.Upstream.Assets.GeoSiteChecksum], filepath.Join(dist, "geosite.dat"), cfg.MaxDATBytes)
		if err != nil {
			return err
		}
		prov.GeoIPSHA256, err = client.DownloadVerified(context.Background(), assets[cfg.Upstream.Assets.GeoIP], assets[cfg.Upstream.Assets.GeoIPChecksum], filepath.Join(dist, "geoip.dat"), cfg.MaxDATBytes)
		if err != nil {
			return err
		}
	}
	records, err := rulescsv.LoadTree(cfg.RulesDir)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	site, err := geodat.LoadSite(filepath.Join(dist, "geosite.dat"), cfg.MaxDATBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	site, siteResult, err := overlay.ApplySite(site, records)
	if err != nil {
		return err
	}
	if err := geodat.WriteSite(filepath.Join(dist, "geosite.dat"), site, cfg.MaxDATBytes); err != nil {
		return err
	}
	siteIndex, err := srs.ExpectedSite(site)
	if err != nil {
		return err
	}
	siteViews := len(siteIndex) - len(site.Entry)
	if len(site.Entry) == 0 {
		return fmt.Errorf("final geosite DAT contains no categories")
	}
	site = nil
	ip, err := geodat.LoadIP(filepath.Join(dist, "geoip.dat"), cfg.MaxDATBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	ip, ipResult, err := overlay.ApplyIP(ip, records)
	if err != nil {
		return err
	}
	if err := geodat.WriteIP(filepath.Join(dist, "geoip.dat"), ip, cfg.MaxDATBytes); err != nil {
		return err
	}
	ipIndex, err := srs.ExpectedIP(ip)
	if err != nil {
		return err
	}
	ipCount := len(ip.Entry)
	if len(ip.Entry) == 0 {
		return fmt.Errorf("final geoip DAT contains no categories")
	}
	ip = nil
	timestamp := prov.PublishedAt
	if *generatedAt != "" {
		timestamp, err = time.Parse(time.RFC3339, *generatedAt)
		if err != nil {
			return fmt.Errorf("invalid --generated-at: %w", err)
		}
	}
	sourceDateEpoch := os.Getenv("SOURCE_DATE_EPOCH")
	if sourceDateEpoch != "" {
		seconds, e := strconv.ParseInt(sourceDateEpoch, 10, 64)
		if e != nil {
			return fmt.Errorf("invalid SOURCE_DATE_EPOCH")
		}
		timestamp = time.Unix(seconds, 0).UTC()
	}
	// Local DAT inputs have no upstream publication timestamp. Keep that mode
	// usable without requiring an extra flag while avoiding MMDB writer's zero
	// BuildEpoch sentinel, which would otherwise fall back to wall-clock time.
	if localBuild && *generatedAt == "" && sourceDateEpoch == "" {
		timestamp = time.Unix(1, 0).UTC()
	}
	timestamp = timestamp.UTC().Truncate(time.Second)
	if err := metadb.Generate(filepath.Join(dist, "geoip.dat"), filepath.Join(dist, "geoip.metadb"), timestamp, cfg.MaxDATBytes); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	tools := srs.Tools{Converter: *converter, SingBox: *singBox, Workers: cfg.Workers}
	if err := srs.Generate(ctx, tools, "geosite", filepath.Join(dist, "geosite.dat"), filepath.Join(dist, "geosite"), siteIndex); err != nil {
		return err
	}
	if err := srs.Generate(ctx, tools, "geoip", filepath.Join(dist, "geoip.dat"), filepath.Join(dist, "geoip"), ipIndex); err != nil {
		return err
	}
	version := "local-c" + (*commit)[:12]
	if prov.ReleaseID > 0 {
		version = fmt.Sprintf("u%d-c%s", prov.ReleaseID, (*commit)[:12])
	}
	manifest := artifact.Manifest{Version: version, GeneratedAt: timestamp, CustomCommit: *commit, ReleaseInputsHash: *inputHash, Upstream: prov, Tools: artifact.ToolVersions{SingBox: lock.SingBox.Version, Mihomo: lock.Mihomo.Version, ConverterModule: lock.Converter.Module, ConverterVersion: lock.Converter.Version, ConverterCommit: lock.Converter.Commit}, Categories: artifact.CategoryCount{GeoSite: len(siteIndex) - siteViews, GeoSiteViews: siteViews, GeoIP: ipCount}, PatchStats: append(siteResult.Stats, ipResult.Stats...), Warnings: append(siteResult.Warnings, ipResult.Warnings...)}
	if err := artifact.Finalize(dist, manifest); err != nil {
		return err
	}
	report, err := validate.DistWithTools(ctx, dist, cfg.MaxDATBytes, tools, *mihomo)
	if err != nil {
		return err
	}
	if err := replaceDir(dist, cfg.DistDir); err != nil {
		return err
	}
	return writeJSON(stdout, report)
}

func runInspect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataset := fs.String("dataset", "", "geosite or geoip")
	datPath := fs.String("dat", "", "input DAT path")
	category := fs.String("category", "", "optional category filter")
	attribute := fs.String("attribute", "", "optional GeoSite attribute filter")
	format := fs.String("format", "json", "text or json")
	max := fs.Int64("max-dat-bytes", geodat.DefaultMaxBytes, "input limit")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if *datPath == "" && fs.NArg() == 1 {
		*datPath = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("%w: inspect accepts --dat or one DAT positional path", errUsage)
	}
	if *datPath == "" || (*dataset != "geosite" && *dataset != "geoip") || (*format != "text" && *format != "json") {
		return fmt.Errorf("%w: inspect requires --dataset, --dat, and [--format text|json]", errUsage)
	}
	if *dataset == "geoip" && *attribute != "" {
		return fmt.Errorf("%w: --attribute only applies to geosite", errUsage)
	}
	if *dataset == "geosite" {
		msg, err := geodat.LoadSite(*datPath, *max)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		items := geodat.SiteProjections(msg)
		if *category != "" {
			items = filterSite(items, strings.ToLower(*category))
		}
		if *attribute != "" {
			items = filterSiteAttribute(items, strings.TrimPrefix(strings.ToLower(*attribute), "@"))
		}
		if *format == "json" {
			return writeJSON(stdout, items)
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "[%s]\n", item.Category)
			for _, rule := range item.Rules {
				fmt.Fprintf(stdout, "%s,%s,%s\n", rule.Type, rule.Value, strings.Join(rule.Attrs, " "))
			}
		}
		return nil
	}
	msg, err := geodat.LoadIP(*datPath, *max)
	if err != nil {
		return fmt.Errorf("%w: %v", errInput, err)
	}
	items := geodat.IPProjections(msg)
	if *category != "" {
		items = filterIP(items, strings.ToLower(*category))
	}
	if *format == "json" {
		return writeJSON(stdout, items)
	}
	for _, item := range items {
		fmt.Fprintf(stdout, "[%s] reverse_match=%t\n", item.Category, item.ReverseMatch)
		for _, cidr := range item.CIDRs {
			fmt.Fprintln(stdout, cidr)
		}
	}
	return nil
}

type Diff struct {
	Dataset    string         `json:"dataset"`
	Categories []CategoryDiff `json:"categories"`
}
type CategoryDiff struct {
	Category     string   `json:"category"`
	Added        []string `json:"added,omitempty"`
	Deleted      []string `json:"deleted,omitempty"`
	OrderChanged bool     `json:"order_changed"`
	Changed      bool     `json:"changed"`
}

func runDiff(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataset := fs.String("dataset", "", "geosite or geoip")
	format := fs.String("format", "text", "text or json")
	beforePath := fs.String("before", "", "before DAT path")
	afterPath := fs.String("after", "", "after DAT path")
	max := fs.Int64("max-dat-bytes", geodat.DefaultMaxBytes, "input limit")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if *beforePath == "" && *afterPath == "" && fs.NArg() == 2 {
		*beforePath, *afterPath = fs.Arg(0), fs.Arg(1)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("%w: diff accepts --before/--after or two positional DAT paths", errUsage)
	}
	if *beforePath == "" || *afterPath == "" || (*dataset != "geosite" && *dataset != "geoip") || (*format != "text" && *format != "json") {
		return fmt.Errorf("%w: diff requires --dataset, --before, --after, and [--format text|json]", errUsage)
	}
	var before, after map[string][]string
	if *dataset == "geosite" {
		a, err := geodat.LoadSite(*beforePath, *max)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		b, err := geodat.LoadSite(*afterPath, *max)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		before = siteMap(a)
		after = siteMap(b)
	} else {
		a, err := geodat.LoadIP(*beforePath, *max)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		b, err := geodat.LoadIP(*afterPath, *max)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		before = ipMap(a)
		after = ipMap(b)
	}
	result := makeDiff(*dataset, before, after)
	if *format == "json" {
		return writeJSON(stdout, result)
	}
	for _, category := range result.Categories {
		if !category.Changed {
			continue
		}
		fmt.Fprintf(stdout, "[%s]\n", category.Category)
		for _, rule := range category.Deleted {
			fmt.Fprintf(stdout, "- %s\n", rule)
		}
		for _, rule := range category.Added {
			fmt.Fprintf(stdout, "+ %s\n", rule)
		}
		if category.OrderChanged {
			fmt.Fprintln(stdout, "~ order changed")
		}
	}
	return nil
}

func runValidate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	max := fs.Int64("max-dat-bytes", geodat.DefaultMaxBytes, "input limit")
	distPath := fs.String("dist", "", "distribution directory")
	configPath := fs.String("config", "config.yaml", "config YAML")
	lockPath := fs.String("toolchain-lock", "toolchain.lock.yaml", "toolchain lock YAML")
	singBox := fs.String("sing-box", "", "test-only sing-box executable override")
	mihomo := fs.String("mihomo", "", "test-only Mihomo executable override")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if *distPath == "" && fs.NArg() == 1 {
		*distPath = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("%w: validate accepts --dist or one positional DIST", errUsage)
	}
	if *distPath == "" || (*singBox == "") != (*mihomo == "") {
		return fmt.Errorf("%w: validate requires --dist and paired tool overrides", errUsage)
	}
	if *singBox == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		lock, err := config.LoadToolchain(*lockPath)
		if err != nil {
			return fmt.Errorf("%w: %v", errInput, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		paths, err := toolchain.Prepare(ctx, lock, cfg.CacheDir)
		if err != nil {
			return err
		}
		*singBox, *mihomo = paths.SingBox, paths.Mihomo
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report, err := validate.DistWithTools(ctx, *distPath, *max, srs.Tools{SingBox: *singBox}, *mihomo)
	if err != nil {
		if errors.Is(err, validate.ErrExternalTool) {
			return err
		}
		return fmt.Errorf("%w: %v", errInput, err)
	}
	return writeJSON(stdout, report)
}

func siteMap(msg *v2ray.GeoSiteList) map[string][]string {
	out := map[string][]string{}
	for _, item := range geodat.SiteProjections(msg) {
		for _, rule := range item.Rules {
			out[item.Category] = append(out[item.Category], fmt.Sprintf("%s,%s,%s", rule.Type, rule.Value, strings.Join(rule.Attrs, " ")))
		}
	}
	return out
}

func ipMap(msg *v2ray.GeoIPList) map[string][]string {
	out := map[string][]string{}
	for _, item := range geodat.IPProjections(msg) {
		if item.ReverseMatch {
			out[item.Category] = append(out[item.Category], "@reverse_match=true")
		}
		out[item.Category] = append(out[item.Category], item.CIDRs...)
	}
	return out
}

func makeDiff(dataset string, before, after map[string][]string) Diff {
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	categories := make([]string, 0, len(keys))
	for key := range keys {
		categories = append(categories, key)
	}
	sort.Strings(categories)
	result := Diff{Dataset: dataset}
	for _, category := range categories {
		oldCounts := counts(before[category])
		newCounts := counts(after[category])
		item := CategoryDiff{Category: category}
		rules := map[string]bool{}
		for rule := range oldCounts {
			rules[rule] = true
		}
		for rule := range newCounts {
			rules[rule] = true
		}
		ordered := make([]string, 0, len(rules))
		for rule := range rules {
			ordered = append(ordered, rule)
		}
		sort.Strings(ordered)
		for _, rule := range ordered {
			for i := newCounts[rule]; i < oldCounts[rule]; i++ {
				item.Deleted = append(item.Deleted, rule)
			}
			for i := oldCounts[rule]; i < newCounts[rule]; i++ {
				item.Added = append(item.Added, rule)
			}
		}
		item.OrderChanged = sameCounts(oldCounts, newCounts) && !equalOrdered(before[category], after[category])
		item.Changed = len(item.Added) > 0 || len(item.Deleted) > 0 || item.OrderChanged
		result.Categories = append(result.Categories, item)
	}
	return result
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func equalOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func counts(values []string) map[string]int {
	out := map[string]int{}
	for _, value := range values {
		out[value]++
	}
	return out
}
func filterSite(items []geodat.SiteProjection, category string) []geodat.SiteProjection {
	for _, item := range items {
		if item.Category == category {
			return []geodat.SiteProjection{item}
		}
	}
	return []geodat.SiteProjection{}
}
func filterIP(items []geodat.IPProjection, category string) []geodat.IPProjection {
	for _, item := range items {
		if item.Category == category {
			return []geodat.IPProjection{item}
		}
	}
	return []geodat.IPProjection{}
}

func filterSiteAttribute(items []geodat.SiteProjection, attribute string) []geodat.SiteProjection {
	if attribute == "" {
		return items
	}
	for i := range items {
		filtered := items[i].Rules[:0]
		for _, rule := range items[i].Rules {
			for _, attr := range rule.Attrs {
				name := strings.TrimPrefix(strings.SplitN(attr, "=", 2)[0], "@")
				if name == attribute {
					filtered = append(filtered, rule)
					break
				}
			}
		}
		items[i].Rules = filtered
	}
	return items
}
func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
func checkExecutable(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("required executable %q: %w", name, err)
	}
	return nil
}
func gitCommit() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		return "working-tree"
	}
	return strings.TrimSpace(string(data))
}
func releaseInputsHash(rulesDir, configPath, lockPath string) (string, error) {
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(configAbs), "..", ".."))
	canonicalRules := filepath.Join(repoRoot, "rules")
	canonicalConfig := filepath.Join(repoRoot, "tools", "rule-patcher", "config.yaml")
	canonicalLock := filepath.Join(repoRoot, "tools", "rule-patcher", "toolchain.lock.yaml")
	if samePath(rulesDir, canonicalRules) && samePath(configAbs, canonicalConfig) && samePath(lockPath, canonicalLock) {
		return hashPublicationInputs(repoRoot)
	}
	rulesHash, err := artifact.HashTree(rulesDir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintln(h, rulesHash)
	for _, path := range []string{configPath, lockPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintln(h, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashPublicationInputs(repoRoot string) (string, error) {
	paths := []string{
		"tools/rule-patcher/config.yaml",
		"tools/rule-patcher/toolchain.lock.yaml",
		"tools/rule-patcher/go.mod",
		"tools/rule-patcher/go.sum",
		".github/workflows/release.yml",
	}
	for _, scope := range []string{"rules", "tools/rule-patcher/cmd", "tools/rule-patcher/internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, filepath.FromSlash(scope)), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("publication input contains symlink: %s", path)
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("publication input is not a regular file: %s", path)
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, rel := range paths {
		file, err := os.Open(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fileHash := sha256.New()
		_, copyErr := io.Copy(fileHash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		fmt.Fprintf(h, "%s  %s\n", hex.EncodeToString(fileHash.Sum(nil)), rel)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func samePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}
func copyFileLimited(source, target string, maxBytes int64) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("input %s is not a regular file", source)
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return "", fmt.Errorf("input %s size is outside 1..%d", source, maxBytes)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != info.Size() || n > maxBytes {
		return "", fmt.Errorf("input %s changed or exceeded the size limit while reading", source)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func replaceDir(staged, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("output directory already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	return nil
}
