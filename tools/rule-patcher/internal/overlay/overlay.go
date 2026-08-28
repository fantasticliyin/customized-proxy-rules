// SPDX-License-Identifier: GPL-3.0-only
package overlay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/rulescsv"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
	"google.golang.org/protobuf/proto"
)

type Warning struct {
	Code      string                  `json:"code"`
	Dataset   rulescsv.Dataset        `json:"dataset"`
	Mode      rulescsv.Mode           `json:"mode"`
	Category  string                  `json:"category"`
	Operation rulescsv.Operation      `json:"operation,omitempty"`
	Rule      any                     `json:"rule,omitempty"`
	Source    rulescsv.SourceLocation `json:"source"`
	Reason    string                  `json:"reason"`
}

type CategoryStats struct {
	Dataset  rulescsv.Dataset `json:"dataset"`
	Category string           `json:"category"`
	Added    int              `json:"added"`
	Deleted  int              `json:"deleted"`
	NoOp     int              `json:"no_op"`
	Retained int              `json:"retained"`
}

type Result struct {
	Warnings []Warning       `json:"warnings"`
	Stats    []CategoryStats `json:"stats"`
}

func ApplySite(input *v2ray.GeoSiteList, records []rulescsv.Record) (*v2ray.GeoSiteList, Result, error) {
	msg := proto.Clone(input).(*v2ray.GeoSiteList)
	index := map[string]*v2ray.GeoSite{}
	for _, category := range msg.Entry {
		index[strings.ToLower(category.CountryCode)] = category
	}
	result := Result{}
	addedEntries := map[*v2ray.GeoSite]map[*v2ray.Domain]bool{}
	missingFiles := map[string]bool{}
	collisionFiles := map[string]bool{}
	newCategories := map[string]*v2ray.GeoSite{}
	for _, record := range records {
		if record.Dataset != rulescsv.GeoSite {
			continue
		}
		category := index[record.Category]
		if category == nil {
			category = newCategories[record.Category]
		}
		if record.Mode == rulescsv.Patch && category == nil {
			if !missingFiles[record.Source.File] {
				result.Warnings = append(result.Warnings, warning("patch_category_missing", record, nil, "PATCH target category does not exist"))
				missingFiles[record.Source.File] = true
			}
			continue
		}
		if record.Mode == rulescsv.New && index[record.Category] != nil && !collisionFiles[record.Source.File] {
			result.Warnings = append(result.Warnings, warning("new_category_collision", record, nil, "NEW category already exists; applying additions to upstream category"))
			collisionFiles[record.Source.File] = true
		}
		if category == nil {
			category = &v2ray.GeoSite{CountryCode: record.Category}
			newCategories[record.Category] = category
		}
		stat := statFor(&result, rulescsv.GeoSite, record.Category)
		rule := *record.Site
		if record.Op == rulescsv.Add {
			if siteExists(category, rule) {
				result.Warnings = append(result.Warnings, warning("add_exists", record, rule, "rule already exists"))
				stat.NoOp++
				continue
			}
			added := toDomain(rule)
			category.Domain = append(category.Domain, added)
			if addedEntries[category] == nil {
				addedEntries[category] = map[*v2ray.Domain]bool{}
			}
			addedEntries[category][added] = true
			stat.Added++
		} else {
			kept := category.Domain[:0]
			deleted := 0
			for _, domain := range category.Domain {
				if siteMatchesDelete(domain, rule) {
					deleted++
				} else {
					kept = append(kept, domain)
				}
			}
			category.Domain = kept
			if deleted == 0 {
				result.Warnings = append(result.Warnings, warning("delete_missing", record, rule, "rule does not exist"))
				stat.NoOp++
			} else {
				stat.Deleted += deleted
			}
		}
	}
	for category, added := range addedEntries {
		sortSiteAdditions(category, added)
	}
	appendNewSite(msg, newCategories)
	for _, category := range msg.Entry {
		statFor(&result, rulescsv.GeoSite, strings.ToLower(category.CountryCode)).Retained = len(category.Domain)
	}
	return msg, result, geodat.ValidateSite(msg)
}

func ApplyIP(input *v2ray.GeoIPList, records []rulescsv.Record) (*v2ray.GeoIPList, Result, error) {
	msg := proto.Clone(input).(*v2ray.GeoIPList)
	index := map[string]*v2ray.GeoIP{}
	for _, category := range msg.Entry {
		index[strings.ToLower(category.CountryCode)] = category
	}
	result := Result{}
	addedEntries := map[*v2ray.GeoIP]map[*v2ray.CIDR]bool{}
	missingFiles, collisionFiles := map[string]bool{}, map[string]bool{}
	newCategories := map[string]*v2ray.GeoIP{}
	for _, record := range records {
		if record.Dataset != rulescsv.GeoIP {
			continue
		}
		category := index[record.Category]
		if category == nil {
			category = newCategories[record.Category]
		}
		if record.Mode == rulescsv.Patch && category == nil {
			if !missingFiles[record.Source.File] {
				result.Warnings = append(result.Warnings, warning("patch_category_missing", record, nil, "PATCH target category does not exist"))
				missingFiles[record.Source.File] = true
			}
			continue
		}
		if record.Mode == rulescsv.New && index[record.Category] != nil && !collisionFiles[record.Source.File] {
			result.Warnings = append(result.Warnings, warning("new_category_collision", record, nil, "NEW category already exists; applying additions to upstream category"))
			collisionFiles[record.Source.File] = true
		}
		if category == nil {
			category = &v2ray.GeoIP{CountryCode: record.Category}
			newCategories[record.Category] = category
		}
		stat := statFor(&result, rulescsv.GeoIP, record.Category)
		identity := rulescsv.IPIdentity(*record.IP)
		found := -1
		for i, cidr := range category.Cidr {
			prefix, err := geodat.Prefix(cidr)
			if err != nil {
				return nil, result, err
			}
			if prefix.String() == identity {
				found = i
				break
			}
		}
		if record.Op == rulescsv.Add {
			if found >= 0 {
				result.Warnings = append(result.Warnings, warning("add_exists", record, record.IP, "rule already exists"))
				stat.NoOp++
			} else {
				added := geodat.CIDR(record.IP.Prefix)
				category.Cidr = append(category.Cidr, added)
				if addedEntries[category] == nil {
					addedEntries[category] = map[*v2ray.CIDR]bool{}
				}
				addedEntries[category][added] = true
				stat.Added++
			}
		} else if found < 0 {
			result.Warnings = append(result.Warnings, warning("delete_missing", record, record.IP, "rule does not exist"))
			stat.NoOp++
		} else {
			kept := category.Cidr[:0]
			deleted := 0
			for _, cidr := range category.Cidr {
				prefix, err := geodat.Prefix(cidr)
				if err != nil {
					return nil, result, err
				}
				if prefix.String() == identity {
					deleted++
					continue
				}
				kept = append(kept, cidr)
			}
			category.Cidr = kept
			stat.Deleted += deleted
		}
	}
	for category, added := range addedEntries {
		sortIPAdditions(category, added)
	}
	appendNewIP(msg, newCategories)
	for _, category := range msg.Entry {
		statFor(&result, rulescsv.GeoIP, strings.ToLower(category.CountryCode)).Retained = len(category.Cidr)
	}
	return msg, result, geodat.ValidateIP(msg)
}

func warning(code string, record rulescsv.Record, rule any, reason string) Warning {
	return Warning{Code: code, Dataset: record.Dataset, Mode: record.Mode, Category: record.Category, Operation: record.Op, Rule: rule, Source: record.Source, Reason: reason}
}
func statFor(result *Result, dataset rulescsv.Dataset, category string) *CategoryStats {
	for i := range result.Stats {
		if result.Stats[i].Dataset == dataset && result.Stats[i].Category == category {
			return &result.Stats[i]
		}
	}
	result.Stats = append(result.Stats, CategoryStats{Dataset: dataset, Category: category})
	return &result.Stats[len(result.Stats)-1]
}

func siteExists(category *v2ray.GeoSite, rule rulescsv.SiteRule) bool {
	for _, domain := range category.Domain {
		if siteMatchesExact(domain, rule) {
			return true
		}
	}
	return false
}
func siteMatchesDelete(domain *v2ray.Domain, rule rulescsv.SiteRule) bool {
	if domain.Type != domainType(rule.Type) || canonicalValue(domain.Type, domain.Value) != rule.Value {
		return false
	}
	if len(rule.Attrs) == 0 {
		return true
	}
	return boolAttrsEqual(domain.Attribute, rule.Attrs)
}
func siteMatchesExact(domain *v2ray.Domain, rule rulescsv.SiteRule) bool {
	return domain.Type == domainType(rule.Type) && canonicalValue(domain.Type, domain.Value) == rule.Value && boolAttrsEqual(domain.Attribute, rule.Attrs)
}
func boolAttrsEqual(attrs []*v2ray.Domain_Attribute, expected []string) bool {
	actual := []string{}
	for _, attr := range attrs {
		v, ok := attr.TypedValue.(*v2ray.Domain_Attribute_BoolValue)
		if !ok || !v.BoolValue {
			return false
		}
		actual = append(actual, strings.ToLower(attr.Key))
	}
	sort.Strings(actual)
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}
func canonicalValue(t v2ray.Domain_Type, value string) string {
	if t == v2ray.Domain_Regex {
		return value
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}
func domainType(t rulescsv.SiteRuleType) v2ray.Domain_Type {
	switch t {
	case rulescsv.Domain:
		return v2ray.Domain_Full
	case rulescsv.DomainSuffix:
		return v2ray.Domain_Domain
	case rulescsv.DomainKeyword:
		return v2ray.Domain_Plain
	case rulescsv.DomainRegex:
		return v2ray.Domain_Regex
	}
	panic(fmt.Sprintf("unsupported site type %q", t))
}
func toDomain(rule rulescsv.SiteRule) *v2ray.Domain {
	domain := &v2ray.Domain{Type: domainType(rule.Type), Value: rule.Value}
	for _, name := range rule.Attrs {
		domain.Attribute = append(domain.Attribute, &v2ray.Domain_Attribute{Key: name, TypedValue: &v2ray.Domain_Attribute_BoolValue{BoolValue: true}})
	}
	return domain
}
func appendNewSite(msg *v2ray.GeoSiteList, categories map[string]*v2ray.GeoSite) {
	keys := make([]string, 0, len(categories))
	for key := range categories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sort.SliceStable(categories[key].Domain, func(i, j int) bool {
			return domainSort(categories[key].Domain[i]) < domainSort(categories[key].Domain[j])
		})
		msg.Entry = append(msg.Entry, categories[key])
	}
}
func appendNewIP(msg *v2ray.GeoIPList, categories map[string]*v2ray.GeoIP) {
	keys := make([]string, 0, len(categories))
	for key := range categories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sort.SliceStable(categories[key].Cidr, func(i, j int) bool {
			a, _ := geodat.Prefix(categories[key].Cidr[i])
			b, _ := geodat.Prefix(categories[key].Cidr[j])
			return a.String() < b.String()
		})
		msg.Entry = append(msg.Entry, categories[key])
	}
}
func domainSort(domain *v2ray.Domain) string {
	return geodat.DomainTypeName(domain.Type) + "\x00" + domain.Value + "\x00" + strings.Join(geodat.AttributeStrings(domain.Attribute), " ")
}

func sortSiteAdditions(category *v2ray.GeoSite, added map[*v2ray.Domain]bool) {
	originals, additions := make([]*v2ray.Domain, 0, len(category.Domain)), make([]*v2ray.Domain, 0, len(added))
	for _, domain := range category.Domain {
		if added[domain] {
			additions = append(additions, domain)
		} else {
			originals = append(originals, domain)
		}
	}
	sort.SliceStable(additions, func(i, j int) bool { return domainSort(additions[i]) < domainSort(additions[j]) })
	category.Domain = append(originals, additions...)
}

func sortIPAdditions(category *v2ray.GeoIP, added map[*v2ray.CIDR]bool) {
	originals, additions := make([]*v2ray.CIDR, 0, len(category.Cidr)), make([]*v2ray.CIDR, 0, len(added))
	for _, cidr := range category.Cidr {
		if added[cidr] {
			additions = append(additions, cidr)
		} else {
			originals = append(originals, cidr)
		}
	}
	sort.SliceStable(additions, func(i, j int) bool {
		a, _ := geodat.Prefix(additions[i])
		b, _ := geodat.Prefix(additions[j])
		return a.String() < b.String()
	})
	category.Cidr = append(originals, additions...)
}
