package backupapp

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

var versionDirectiveRE = regexp.MustCompile(`/\*!\d{5,6}\s*|\*/`)
var useDatabaseRE = regexp.MustCompile("^USE `((?:``|[^`])+)`")
var schemaObjectRE = regexp.MustCompile("(?:^|[[:space:]])(TABLE|VIEW|TRIGGER|PROCEDURE|FUNCTION|EVENT)[[:space:]]+(?:`((?:``|[^`])+)`|([A-Za-z0-9_$]+))")

func artifactObjectInventory(path, defaultDB, key string) (map[string]map[string][]string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var source io.Reader = f
	if encryptedArtifactPath(path) {
		source, e = DecryptReader(f, key)
		if e != nil {
			return nil, e
		}
	}
	gz, e := gzip.NewReader(source)
	if e != nil {
		return nil, e
	}
	defer gz.Close()
	reader := bufio.NewReaderSize(gz, 64<<10)
	db := defaultDB
	result := map[string]map[string][]string{}
	for {
		line, e := reader.ReadString('\n')
		if e != nil && e != io.EOF {
			return nil, e
		}
		if m := useDatabaseRE.FindStringSubmatch(line); m != nil {
			db = strings.ReplaceAll(m[1], "``", "`")
		}
		directive := ""
		if strings.HasPrefix(line, "CREATE ") || strings.HasPrefix(line, "/*!") {
			directive = versionDirectiveRE.ReplaceAllString(line, "")
		}
		directive = strings.TrimSpace(directive)
		if (strings.HasPrefix(line, "CREATE ") || strings.HasPrefix(line, "/*!")) && !strings.HasPrefix(directive, "DROP ") && !strings.HasPrefix(directive, "SET ") {
			for _, m := range schemaObjectRE.FindAllStringSubmatch(line, -1) {
				kind := strings.ToLower(m[1])
				if kind == "procedure" || kind == "function" {
					kind = "routine"
				}
				rawName := m[2]
				if rawName == "" {
					rawName = m[3]
				}
				name := strings.ReplaceAll(rawName, "``", "`")
				if result[db] == nil {
					result[db] = map[string][]string{}
				}
				if !slices.Contains(result[db][kind], name) {
					result[db][kind] = append(result[db][kind], name)
				}
			}
		}
		if e == io.EOF {
			break
		}
	}
	for _, inventory := range result {
		tables := []string{}
		for _, name := range inventory["table"] {
			if !slices.Contains(inventory["view"], name) {
				tables = append(tables, name)
			}
		}
		inventory["table"] = tables
		for _, names := range inventory {
			slices.Sort(names)
		}
	}
	return result, nil
}
func compareObjectInventory(db string, expected, actual map[string][]string) error {
	for _, kind := range []string{"table", "view", "trigger", "routine", "event"} {
		want := append([]string(nil), expected[kind]...)
		got := append([]string(nil), actual[kind]...)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			return fmt.Errorf("captured %s inventory mismatch for database %s: expected=%d actual=%d", kind, db, len(want), len(got))
		}
	}
	return nil
}
