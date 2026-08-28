// SPDX-License-Identifier: GPL-3.0-only
package rulescsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

var siteHeader = []string{"op", "type", "value", "attrs", "note"}
var ipHeader = []string{"op", "type", "value", "note"}

const maxRuleFileBytes int64 = 16 << 20

func LoadTree(root string) ([]Record, error) {
	var all []Record
	for _, dataset := range []Dataset{GeoSite, GeoIP} {
		for _, mode := range []Mode{New, Patch} {
			dir := filepath.Join(root, string(dataset), string(mode))
			records, err := loadDir(root, dir, dataset, mode)
			if err != nil {
				return nil, err
			}
			all = append(all, records...)
		}
	}
	if err := detectConflicts(all); err != nil {
		return nil, err
	}
	return all, nil
}

func loadDir(root, dir string, dataset Dataset, mode Mode) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read rules directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []Record
	seenCategories := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".csv" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("rule file %s is not a regular file", entry.Name())
		}
		if info.Size() <= 0 || info.Size() > maxRuleFileBytes {
			return nil, fmt.Errorf("rule file %s must be non-empty and no larger than %d bytes", entry.Name(), maxRuleFileBytes)
		}
		category := strings.TrimSuffix(entry.Name(), ".csv")
		if err := ValidateCategory(category); err != nil {
			return nil, err
		}
		key := strings.ToLower(category)
		if old, exists := seenCategories[key]; exists {
			return nil, fmt.Errorf("category collision: %s and %s", old, entry.Name())
		}
		seenCategories[key] = entry.Name()
		rel, _ := filepath.Rel(root, filepath.Join(dir, entry.Name()))
		records, err := readFile(filepath.Join(dir, entry.Name()), filepath.ToSlash(rel), dataset, mode, category)
		if err != nil {
			return nil, err
		}
		out = append(out, records...)
	}
	return out, nil
}

func readFile(path, display string, dataset Dataset, mode Mode, category string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", display, err)
	}
	if !utf8.Valid(data) || (len(data) >= 3 && string(data[:3]) == "\xef\xbb\xbf") {
		return nil, fmt.Errorf("%s: must be UTF-8 without BOM", display)
	}
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s record 1: read header: %w", display, err)
	}
	expected := siteHeader
	if dataset == GeoIP {
		expected = ipHeader
	}
	if !equalStrings(header, expected) {
		return nil, fmt.Errorf("%s record 1: header must be %s", display, strings.Join(expected, ","))
	}
	var out []Record
	logical := 1
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		logical++
		if err != nil {
			return nil, fmt.Errorf("%s record %d: %w", display, logical, err)
		}
		if len(row) != len(expected) {
			return nil, fmt.Errorf("%s record %d: expected %d columns, got %d", display, logical, len(expected), len(row))
		}
		op := Operation(row[0])
		if op != Add && op != Delete {
			return nil, fmt.Errorf("%s record %d: op must be + or -", display, logical)
		}
		if mode == New && op != Add {
			return nil, fmt.Errorf("%s record %d: NEW files only permit +", display, logical)
		}
		record := Record{Dataset: dataset, Mode: mode, Category: category, Op: op, Source: SourceLocation{File: display, Record: logical}}
		if dataset == GeoSite {
			rule, err := CanonicalSite(row[1], row[2], row[3])
			if err != nil {
				return nil, fmt.Errorf("%s record %d: %w", display, logical, err)
			}
			record.Site, record.Note = &rule, row[4]
		} else {
			if row[1] != "IP-CIDR" {
				return nil, fmt.Errorf("%s record %d: geoip type must be IP-CIDR", display, logical)
			}
			rule, err := CanonicalIP(row[2])
			if err != nil {
				return nil, fmt.Errorf("%s record %d: %w", display, logical, err)
			}
			record.IP, record.Note = &rule, row[3]
		}
		out = append(out, record)
	}
	return out, nil
}

func detectConflicts(records []Record) error {
	type seen struct {
		op     Operation
		source SourceLocation
	}
	seenOps := map[string]seen{}
	wildcardDeletes := map[string]SourceLocation{}
	baseAdds := map[string]SourceLocation{}
	for _, record := range records {
		identity := ""
		baseKey := ""
		if record.Site != nil {
			identity = SiteIdentity(*record.Site)
			baseKey = record.Source.File + "\x00" + SiteBaseIdentity(*record.Site)
			if record.Op == Delete && len(record.Site.Attrs) == 0 {
				if addSource, ok := baseAdds[baseKey]; ok {
					return fmt.Errorf("wildcard delete conflicts with add in %s record %d and %s record %d", addSource.File, addSource.Record, record.Source.File, record.Source.Record)
				}
				wildcardDeletes[baseKey] = record.Source
			}
			if record.Op == Add {
				if delSource, ok := wildcardDeletes[baseKey]; ok {
					return fmt.Errorf("add conflicts with wildcard delete in %s record %d and %s record %d", delSource.File, delSource.Record, record.Source.File, record.Source.Record)
				}
				baseAdds[baseKey] = record.Source
			}
		} else {
			identity = IPIdentity(*record.IP)
		}
		key := string(record.Dataset) + "\x00" + string(record.Mode) + "\x00" + record.Category + "\x00" + identity
		if old, ok := seenOps[key]; ok && old.op != record.Op {
			return fmt.Errorf("conflicting operations for %s in %s record %d and %s record %d", identity, old.source.File, old.source.Record, record.Source.File, record.Source.Record)
		}
		seenOps[key] = seen{record.Op, record.Source}
	}
	return nil
}

func equalStrings(a, b []string) bool {
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
