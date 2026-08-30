// SPDX-License-Identifier: GPL-3.0-only
package rulescsv

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

type Dataset string
type Mode string
type Operation string
type SiteRuleType string

const (
	GeoSite       Dataset      = "geosite"
	GeoIP         Dataset      = "geoip"
	New           Mode         = "new"
	Patch         Mode         = "patch"
	Add           Operation    = "+"
	Delete        Operation    = "-"
	Domain        SiteRuleType = "DOMAIN"
	DomainSuffix  SiteRuleType = "DOMAIN-SUFFIX"
	DomainKeyword SiteRuleType = "DOMAIN-KEYWORD"
	DomainRegex   SiteRuleType = "DOMAIN-REGEX"
)

type SourceLocation struct {
	File   string `json:"file"`
	Record int    `json:"record"`
}

type SiteRule struct {
	Type  SiteRuleType `json:"type"`
	Value string       `json:"value"`
	Attrs []string     `json:"attrs,omitempty"`
}

type IPRule struct {
	Prefix netip.Prefix `json:"-"`
}

func (r IPRule) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", r.Prefix.String())), nil
}

type Record struct {
	Dataset  Dataset        `json:"dataset"`
	Mode     Mode           `json:"mode"`
	Category string         `json:"category"`
	Op       Operation      `json:"op"`
	Site     *SiteRule      `json:"site,omitempty"`
	IP       *IPRule        `json:"ip,omitempty"`
	Source   SourceLocation `json:"source"`
	Note     string         `json:"note,omitempty"`
}

var categoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_!-]*$`)
var attrPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func ValidateCategory(s string) error {
	if !categoryPattern.MatchString(s) || strings.Contains(s, "@") {
		return fmt.Errorf("invalid category %q: expected lowercase [a-z0-9][a-z0-9_!-]*", s)
	}
	return nil
}

func CanonicalSite(t, value, attrs string) (SiteRule, error) {
	rule := SiteRule{Type: SiteRuleType(t)}
	switch rule.Type {
	case Domain, DomainSuffix, DomainKeyword:
		rule.Value = strings.TrimSpace(value)
		rule.Value = strings.TrimSuffix(strings.ToLower(rule.Value), ".")
		if rule.Value == "" {
			return rule, fmt.Errorf("empty %s value", t)
		}
	case DomainRegex:
		rule.Value = value
		if value == "" {
			return rule, fmt.Errorf("empty DOMAIN-REGEX value")
		}
		if _, err := regexp.Compile(value); err != nil {
			return rule, fmt.Errorf("invalid DOMAIN-REGEX: %w", err)
		}
	default:
		return rule, fmt.Errorf("unsupported geosite type %q", t)
	}
	seen := map[string]bool{}
	for _, field := range strings.Fields(attrs) {
		if !strings.HasPrefix(field, "@") || len(field) < 2 {
			return rule, fmt.Errorf("invalid attribute %q: expected @name", field)
		}
		name := strings.ToLower(strings.TrimPrefix(field, "@"))
		if !attrPattern.MatchString(name) {
			return rule, fmt.Errorf("invalid attribute %q", field)
		}
		seen[name] = true
	}
	for name := range seen {
		rule.Attrs = append(rule.Attrs, name)
	}
	sort.Strings(rule.Attrs)
	return rule, nil
}

func CanonicalIP(value string) (IPRule, error) {
	value = strings.TrimSpace(value)
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		addr, addrErr := netip.ParseAddr(value)
		if addrErr != nil {
			return IPRule{}, fmt.Errorf("invalid IP-CIDR %q", value)
		}
		bits := 128
		if addr.Is4() {
			bits = 32
		}
		prefix = netip.PrefixFrom(addr, bits)
	}
	return IPRule{Prefix: prefix.Masked()}, nil
}

func SiteIdentity(rule SiteRule) string {
	return string(rule.Type) + "\x00" + rule.Value + "\x00" + strings.Join(rule.Attrs, " ")
}

func SiteBaseIdentity(rule SiteRule) string { return string(rule.Type) + "\x00" + rule.Value }
func IPIdentity(rule IPRule) string         { return rule.Prefix.String() }
