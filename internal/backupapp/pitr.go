package backupapp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var mysqlSourceDataPattern = regexp.MustCompile(`(?i)(?:MASTER|SOURCE)_LOG_FILE\s*=\s*'([^']+)'\s*,\s*(?:MASTER|SOURCE)_LOG_POS\s*=\s*(\d+)`)

// mysqlSourceDataExtractor only inspects the initial dump header. mysqldump
// emits --source-data there, before table data; the cap prevents an unusually
// large INSERT line from being retained in memory.
type mysqlSourceDataExtractor struct {
	buf []byte
}

func newMySQLSourceDataExtractor() *mysqlSourceDataExtractor {
	return &mysqlSourceDataExtractor{buf: make([]byte, 0, 16*1024)}
}

func (e *mysqlSourceDataExtractor) Write(p []byte) (int, error) {
	const maxHeaderBytes = 256 * 1024
	if len(e.buf) < maxHeaderBytes {
		remaining := maxHeaderBytes - len(e.buf)
		if remaining > len(p) {
			remaining = len(p)
		}
		e.buf = append(e.buf, p[:remaining]...)
	}
	return len(p), nil
}

func (e *mysqlSourceDataExtractor) coordinate() *mysqlPITRCoordinate {
	return parseMySQLSourceDataCoordinate(string(e.buf))
}

func parseMySQLSourceDataCoordinate(dumpHeader string) *mysqlPITRCoordinate {
	match := mysqlSourceDataPattern.FindStringSubmatch(dumpHeader)
	if len(match) != 3 {
		return nil
	}
	position, err := strconv.ParseUint(match[2], 10, 64)
	if err != nil || position == 0 || strings.TrimSpace(match[1]) == "" {
		return nil
	}
	return &mysqlPITRCoordinate{
		BinlogFile:     match[1],
		BinlogPosition: position,
		Method:         "mysqldump_source_data",
	}
}

func parseMySQLBinaryLogStatus(output string) (file string, position uint64, gtidSet string, err error) {
	line := ""
	for _, candidate := range strings.Split(output, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
		return "", 0, "", fmt.Errorf("unexpected SHOW BINARY LOG STATUS output")
	}
	position, err = strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
	if err != nil || position == 0 {
		return "", 0, "", fmt.Errorf("invalid binary log position %q", fields[1])
	}
	if len(fields) >= 5 {
		gtidSet = strings.TrimSpace(fields[4])
	}
	return strings.TrimSpace(fields[0]), position, gtidSet, nil
}

func parseMySQLServerIdentity(output string) (uuid, version, gtidSet string, err error) {
	line := ""
	for _, candidate := range strings.Split(output, "\n") {
		if strings.TrimSpace(candidate) != "" {
			// Do not TrimSpace this line: an empty GTID set is represented by a
			// trailing tab and is valid when GTID mode is disabled.
			line = strings.TrimSuffix(candidate, "\r")
			break
		}
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		return "", "", "", fmt.Errorf("unexpected MySQL server identity output")
	}
	return strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2]), nil
}

// captureMySQLPITRMetadata reads the server's current binary-log status just
// before logical dumps begin. This is a useful recovery boundary, but source
// data embedded in each dump is the authoritative coordinate for that dump.
func captureMySQLPITRMetadata(ctx context.Context, cfg config) (mysqlPITRMetadata, error) {
	metadata := mysqlPITRMetadata{
		Status:              "unavailable",
		Method:              "SHOW BINARY LOG STATUS",
		SnapshotConsistency: "server_state_observed_before_logical_dumps",
		Note:                "This server observation is not a shared snapshot across parallel logical dumps; use each artifact's mysql_pitr_coordinate when replaying archived binlogs.",
	}
	identityCmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-B", "-e", "SELECT @@GLOBAL.server_uuid, @@GLOBAL.version, @@GLOBAL.gtid_executed")
	identityOut, identityErr := identityCmd.CombinedOutput()
	if identityErr == nil {
		uuid, version, gtidSet, parseErr := parseMySQLServerIdentity(string(identityOut))
		if parseErr != nil {
			metadata.Error = parseErr.Error()
			return metadata, parseErr
		}
		metadata.ServerUUID = uuid
		metadata.ServerVersion = version
		metadata.ExecutedGTIDSet = gtidSet
	} else {
		metadata.Error = chooseFailureMessage(identityErr, string(identityOut))
		return metadata, fmt.Errorf("query MySQL server identity: %s", metadata.Error)
	}

	var statusOut []byte
	var statusErr error
	statusMethod := ""
	for _, query := range []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS"} {
		cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-B", "-e", query)
		statusOut, statusErr = cmd.CombinedOutput()
		if statusErr == nil {
			statusMethod = query
			break
		}
	}
	if statusErr != nil {
		metadata.Error = chooseFailureMessage(statusErr, string(statusOut))
		return metadata, fmt.Errorf("query MySQL binary log status: %s", metadata.Error)
	}
	file, position, gtidSet, parseErr := parseMySQLBinaryLogStatus(string(statusOut))
	if parseErr != nil {
		metadata.Error = parseErr.Error()
		return metadata, parseErr
	}
	metadata.Status = "captured"
	metadata.Method = statusMethod
	metadata.CapturedAt = time.Now().UTC()
	metadata.BinlogFile = file
	metadata.BinlogPosition = position
	if gtidSet != "" {
		metadata.ExecutedGTIDSet = gtidSet
	}
	metadata.Error = ""
	return metadata, nil
}
