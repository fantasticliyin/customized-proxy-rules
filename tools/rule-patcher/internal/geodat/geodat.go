// SPDX-License-Identifier: GPL-3.0-only
package geodat

import (
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"

	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
	"google.golang.org/protobuf/proto"
)

const DefaultMaxBytes int64 = 128 << 20

type SiteProjection struct {
	Category string      `json:"category"`
	Rules    []SiteEntry `json:"rules"`
}
type SiteEntry struct {
	Type  string   `json:"type"`
	Value string   `json:"value"`
	Attrs []string `json:"attrs,omitempty"`
}
type IPProjection struct {
	Category     string   `json:"category"`
	ReverseMatch bool     `json:"reverse_match"`
	CIDRs        []string `json:"cidrs"`
}

func LoadSite(path string, maxBytes int64) (*v2ray.GeoSiteList, error) {
	msg := &v2ray.GeoSiteList{}
	if err := load(path, maxBytes, msg); err != nil {
		return nil, err
	}
	if err := ValidateSite(msg); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return msg, nil
}

func LoadIP(path string, maxBytes int64) (*v2ray.GeoIPList, error) {
	msg := &v2ray.GeoIPList{}
	if err := load(path, maxBytes, msg); err != nil {
		return nil, err
	}
	if err := ValidateIP(msg); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return msg, nil
}

func load(path string, maxBytes int64, msg proto.Message) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open DAT: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat DAT: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("DAT must be a non-empty regular file")
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("DAT size %d exceeds limit %d", info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return fmt.Errorf("read DAT: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("DAT exceeds limit %d", maxBytes)
	}
	if err := proto.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("unmarshal DAT: %w", err)
	}
	return nil
}

func WriteSite(path string, msg *v2ray.GeoSiteList, maxBytes int64) error {
	if err := ValidateSite(msg); err != nil {
		return err
	}
	return writeReload(path, msg, &v2ray.GeoSiteList{}, maxBytes, func(m proto.Message) error { return ValidateSite(m.(*v2ray.GeoSiteList)) })
}
func WriteIP(path string, msg *v2ray.GeoIPList, maxBytes int64) error {
	if err := ValidateIP(msg); err != nil {
		return err
	}
	return writeReload(path, msg, &v2ray.GeoIPList{}, maxBytes, func(m proto.Message) error { return ValidateIP(m.(*v2ray.GeoIPList)) })
}

func writeReload(path string, msg, reload proto.Message, maxBytes int64, validate func(proto.Message) error) error {
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal DAT: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("output DAT exceeds limit %d", maxBytes)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write DAT: %w", err)
	}
	if err := proto.Unmarshal(data, reload); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("reload DAT: %w", err)
	}
	if err := validate(reload); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("validate reloaded DAT: %w", err)
	}
	if !proto.Equal(msg, reload) {
		_ = os.Remove(tmp)
		return fmt.Errorf("reloaded DAT differs from in-memory message")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit DAT: %w", err)
	}
	return nil
}

func ValidateSite(msg *v2ray.GeoSiteList) error {
	seen := map[string]bool{}
	for i, category := range msg.Entry {
		if category == nil {
			return fmt.Errorf("nil geosite category at index %d", i)
		}
		code := strings.ToLower(category.CountryCode)
		if code == "" || seen[code] {
			return fmt.Errorf("duplicate or empty geosite category at index %d: %q", i, category.CountryCode)
		}
		seen[code] = true
		for j, domain := range category.Domain {
			if domain == nil || domain.Value == "" {
				return fmt.Errorf("category %s has empty domain at index %d", code, j)
			}
			if domain.Type < v2ray.Domain_Plain || domain.Type > v2ray.Domain_Full {
				return fmt.Errorf("category %s has invalid domain type %d", code, domain.Type)
			}
			for k, attr := range domain.Attribute {
				if attr == nil || strings.TrimSpace(attr.Key) == "" {
					return fmt.Errorf("category %s domain %d has invalid attribute at index %d", code, j, k)
				}
				switch attr.TypedValue.(type) {
				case *v2ray.Domain_Attribute_BoolValue, *v2ray.Domain_Attribute_IntValue:
				case nil:
					if len(attr.ProtoReflect().GetUnknown()) == 0 {
						return fmt.Errorf("category %s domain %d attribute %q has no supported value", code, j, attr.Key)
					}
				default:
					return fmt.Errorf("category %s domain %d attribute %q has no supported value", code, j, attr.Key)
				}
			}
		}
	}
	return nil
}

func ValidateIP(msg *v2ray.GeoIPList) error {
	seen := map[string]bool{}
	for i, category := range msg.Entry {
		if category == nil {
			return fmt.Errorf("nil geoip category at index %d", i)
		}
		code := strings.ToLower(category.CountryCode)
		if code == "" || seen[code] {
			return fmt.Errorf("duplicate or empty geoip category at index %d: %q", i, category.CountryCode)
		}
		seen[code] = true
		for j, cidr := range category.Cidr {
			if _, err := Prefix(cidr); err != nil {
				return fmt.Errorf("category %s CIDR %d: %w", code, j, err)
			}
		}
	}
	return nil
}

func Prefix(cidr *v2ray.CIDR) (netip.Prefix, error) {
	if cidr == nil {
		return netip.Prefix{}, fmt.Errorf("nil CIDR")
	}
	addr, ok := netip.AddrFromSlice(cidr.Ip)
	if !ok {
		return netip.Prefix{}, fmt.Errorf("IP must contain 4 or 16 bytes")
	}
	if addr.Is4In6() {
		return netip.Prefix{}, fmt.Errorf("IPv4-mapped IPv6 addresses are not canonical GeoIP entries")
	}
	bits := addr.BitLen()
	if cidr.Prefix > uint32(bits) {
		return netip.Prefix{}, fmt.Errorf("prefix %d exceeds address width %d", cidr.Prefix, bits)
	}
	prefix := netip.PrefixFrom(addr, int(cidr.Prefix))
	if !prefix.IsValid() {
		return netip.Prefix{}, fmt.Errorf("invalid IP prefix")
	}
	return prefix.Masked(), nil
}

func CIDR(prefix netip.Prefix) *v2ray.CIDR {
	prefix = prefix.Masked()
	addr := prefix.Addr()
	if addr.Is4() {
		a := addr.As4()
		return &v2ray.CIDR{Ip: append([]byte(nil), a[:]...), Prefix: uint32(prefix.Bits())}
	}
	a := addr.As16()
	return &v2ray.CIDR{Ip: append([]byte(nil), a[:]...), Prefix: uint32(prefix.Bits())}
}

func SiteProjections(msg *v2ray.GeoSiteList) []SiteProjection {
	out := make([]SiteProjection, 0, len(msg.Entry))
	for _, category := range msg.Entry {
		item := SiteProjection{Category: strings.ToLower(category.CountryCode)}
		for _, domain := range category.Domain {
			entry := SiteEntry{Type: DomainTypeName(domain.Type), Value: domain.Value, Attrs: AttributeStrings(domain.Attribute)}
			item.Rules = append(item.Rules, entry)
		}
		out = append(out, item)
	}
	return out
}

func IPProjections(msg *v2ray.GeoIPList) []IPProjection {
	out := make([]IPProjection, 0, len(msg.Entry))
	for _, category := range msg.Entry {
		item := IPProjection{Category: strings.ToLower(category.CountryCode), ReverseMatch: category.ReverseMatch}
		for _, cidr := range category.Cidr {
			prefix, _ := Prefix(cidr)
			item.CIDRs = append(item.CIDRs, prefix.String())
		}
		out = append(out, item)
	}
	return out
}

func DomainTypeName(t v2ray.Domain_Type) string {
	switch t {
	case v2ray.Domain_Full:
		return "DOMAIN"
	case v2ray.Domain_Domain:
		return "DOMAIN-SUFFIX"
	case v2ray.Domain_Plain:
		return "DOMAIN-KEYWORD"
	case v2ray.Domain_Regex:
		return "DOMAIN-REGEX"
	}
	return fmt.Sprintf("UNKNOWN-%d", t)
}

func AttributeStrings(attrs []*v2ray.Domain_Attribute) []string {
	result := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		switch value := attr.TypedValue.(type) {
		case *v2ray.Domain_Attribute_BoolValue:
			if value.BoolValue {
				result = append(result, "@"+strings.ToLower(attr.Key))
			} else {
				result = append(result, "@"+strings.ToLower(attr.Key)+"=false")
			}
		case *v2ray.Domain_Attribute_IntValue:
			result = append(result, fmt.Sprintf("@%s=%d", strings.ToLower(attr.Key), value.IntValue))
		default:
			result = append(result, "@"+strings.ToLower(attr.Key)+"=<unknown>")
		}
	}
	sort.Strings(result)
	return result
}
