// SPDX-License-Identifier: GPL-3.0-only
package metadb

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/maxmind/mmdbwriter"
	"github.com/metacubex/geo/convert"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
	"github.com/oschwald/maxminddb-golang"
)

const (
	DatabaseType = "Meta-geoip0"
	IPVersion    = 6
	RecordSize   = 24
)

// Generate converts the final on-disk V2Ray GeoIP DAT with MetaCubeX's
// official converter, normalizes its wall-clock metadata, validates the
// result, and atomically installs it at target.
func Generate(datPath, target string, generatedAt time.Time, maxBytes int64) error {
	generatedAt = generatedAt.UTC().Truncate(time.Second)
	if generatedAt.IsZero() || generatedAt.Unix() <= 0 {
		return fmt.Errorf("MetaDB generated_at must be after the Unix epoch")
	}
	list, err := geodat.LoadIP(datPath, maxBytes)
	if err != nil {
		return fmt.Errorf("load final GeoIP DAT: %w", err)
	}
	if err := validateSource(list); err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := os.CreateTemp(dir, ".geoip-metadb-raw-")
	if err != nil {
		return err
	}
	rawPath := raw.Name()
	defer os.Remove(rawPath)
	if err := convert.V2RayIPToMetaV0(list.Entry, raw); err != nil {
		_ = raw.Close()
		return fmt.Errorf("convert GeoIP DAT to MetaDB: %w", err)
	}
	if err := raw.Close(); err != nil {
		return err
	}

	tree, err := mmdbwriter.Load(rawPath, mmdbwriter.Options{
		BuildEpoch:              generatedAt.Unix(),
		DatabaseType:            DatabaseType,
		IPVersion:               IPVersion,
		RecordSize:              RecordSize,
		DisableIPv4Aliasing:     true,
		IncludeReservedNetworks: true,
	})
	if err != nil {
		return fmt.Errorf("normalize MetaDB: %w", err)
	}
	normalized, err := os.CreateTemp(dir, ".geoip-metadb-normalized-")
	if err != nil {
		return err
	}
	normalizedPath := normalized.Name()
	defer os.Remove(normalizedPath)
	if _, err := tree.WriteTo(normalized); err != nil {
		_ = normalized.Close()
		return fmt.Errorf("write normalized MetaDB: %w", err)
	}
	if err := normalized.Sync(); err != nil {
		_ = normalized.Close()
		return err
	}
	if err := normalized.Close(); err != nil {
		return err
	}
	if err := os.Chmod(normalizedPath, 0o644); err != nil {
		return err
	}
	if err := Validate(normalizedPath, list, generatedAt, maxBytes); err != nil {
		return err
	}
	if err := os.Rename(normalizedPath, target); err != nil {
		return err
	}
	return syncDir(dir)
}

// Validate checks MetaDB metadata and proves the lookup relationship against
// every category/CIDR in the authoritative final DAT.
func Validate(path string, list *v2ray.GeoIPList, generatedAt time.Time, maxBytes int64) error {
	if err := validateSource(list); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return fmt.Errorf("MetaDB must be a non-empty regular file no larger than %d bytes", maxBytes)
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return fmt.Errorf("open MetaDB: %w", err)
	}
	defer reader.Close()
	wantEpoch := generatedAt.UTC().Truncate(time.Second).Unix()
	metadata := reader.Metadata
	if metadata.DatabaseType != DatabaseType || metadata.IPVersion != IPVersion || metadata.RecordSize != RecordSize || int64(metadata.BuildEpoch) != wantEpoch {
		return fmt.Errorf("unexpected MetaDB metadata: type=%q ip_version=%d record_size=%d build_epoch=%d", metadata.DatabaseType, metadata.IPVersion, metadata.RecordSize, metadata.BuildEpoch)
	}
	known := make(map[string]bool, len(list.Entry))
	for _, category := range list.Entry {
		code := strings.ToLower(category.CountryCode)
		if code == "" {
			return fmt.Errorf("GeoIP DAT contains an empty category")
		}
		known[code] = true
	}
	for _, category := range list.Entry {
		code := strings.ToLower(category.CountryCode)
		for _, cidr := range category.Cidr {
			prefix, err := geodat.Prefix(cidr)
			if err != nil {
				return fmt.Errorf("GeoIP category %s: %w", code, err)
			}
			for _, address := range []netip.Addr{prefix.Masked().Addr(), prefixLast(prefix)} {
				var record any
				if err := reader.Lookup(address.AsSlice(), &record); err != nil {
					return fmt.Errorf("MetaDB lookup %s for %s: %w", address, code, err)
				}
				codes, err := recordCodes(record)
				if err != nil {
					return fmt.Errorf("MetaDB lookup %s: %w", address, err)
				}
				if !contains(codes, code) {
					return fmt.Errorf("MetaDB lookup %s is missing category %s", address, code)
				}
				for _, returned := range codes {
					if !known[returned] {
						return fmt.Errorf("MetaDB lookup %s returned unknown category %s", address, returned)
					}
				}
			}
		}
	}
	return nil
}

func validateSource(list *v2ray.GeoIPList) error {
	if list == nil {
		return fmt.Errorf("final GeoIP DAT is required")
	}
	if err := geodat.ValidateIP(list); err != nil {
		return fmt.Errorf("validate final GeoIP DAT: %w", err)
	}
	if len(list.Entry) == 0 {
		return fmt.Errorf("final GeoIP DAT contains no categories")
	}
	for _, category := range list.Entry {
		if category.ReverseMatch {
			return fmt.Errorf("MetaDB cannot represent reverse_match GeoIP category %q", strings.ToLower(category.CountryCode))
		}
		if len(category.Cidr) == 0 {
			return fmt.Errorf("MetaDB cannot represent empty GeoIP category %q", strings.ToLower(category.CountryCode))
		}
	}
	return nil
}

// LookupCodes exposes the MetaDB multi-category lookup used by focused tests
// and consumers that need to verify a published category.
func LookupCodes(path string, address netip.Addr) ([]string, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var record any
	if err := reader.Lookup(address.AsSlice(), &record); err != nil {
		return nil, err
	}
	return recordCodes(record)
}

func prefixLast(prefix netip.Prefix) netip.Addr {
	prefix = prefix.Masked()
	if prefix.Addr().Is4() {
		bytes := prefix.Addr().As4()
		for bit := prefix.Bits(); bit < 32; bit++ {
			bytes[bit/8] |= 1 << (7 - uint(bit%8))
		}
		return netip.AddrFrom4(bytes)
	}
	bytes := prefix.Addr().As16()
	for bit := prefix.Bits(); bit < 128; bit++ {
		bytes[bit/8] |= 1 << (7 - uint(bit%8))
	}
	return netip.AddrFrom16(bytes)
}

func recordCodes(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{strings.ToLower(typed)}, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			code, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("record contains non-string category")
			}
			out = append(out, strings.ToLower(code))
		}
		return out, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("record has unsupported type %T", value)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	return nil
}
