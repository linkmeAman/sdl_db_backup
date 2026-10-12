package backupapp

import (
	"os"
	"time"
)

type RecoveryHealth struct {
	LatestCompleteID         string   `json:"latest_complete_id"`
	LatestPartialID          string   `json:"latest_partial_id"`
	LatestRemoteVerifiedID   string   `json:"latest_remote_verified_id"`
	LatestLocalCompleteID    string   `json:"latest_local_complete_id"`
	CompleteAgeSeconds       int64    `json:"complete_age_seconds"`
	AvailabilityState        string   `json:"availability_state"`
	FailureDetails           []string `json:"failure_details"`
	CrossDatabaseConsistency bool     `json:"cross_database_consistency"`
}

func recoveryHealth(cfg config) RecoveryHealth {
	h := RecoveryHealth{AvailabilityState: "UNVERIFIED"}
	runs, e := ReadRunHistory(cfg.RunLogPath)
	if e != nil {
		return h
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.Status == "partial" && h.LatestPartialID == "" {
			h.LatestPartialID = r.RunID
		}
		if r.RemoteIntegrityState == "VERIFIED" && r.Status == "success" && h.LatestRemoteVerifiedID == "" {
			h.LatestRemoteVerifiedID = r.RunID
		}
		if r.Status == "success" && r.DatabasesTotal > 0 && r.DatabasesTotal == len(cfg.LogicalDatabases) && r.DatabasesFailed == 0 {
			if h.LatestCompleteID == "" {
				h.LatestCompleteID = r.RunID
				h.CompleteAgeSeconds = int64(time.Since(r.Timestamp).Seconds())
				h.CrossDatabaseConsistency = r.ConsistencyMethod == "single mysqldump transaction across all selected InnoDB databases; DDL excluded by policy"
			}
			if h.LatestLocalCompleteID == "" {
				if _, e := os.Stat(r.RunFolder); e == nil {
					h.LatestLocalCompleteID = r.RunID
				}
			}
		}
		if i == len(runs)-1 {
			for _, d := range r.Results {
				if d.Status != "success" {
					h.FailureDetails = append(h.FailureDetails, d.Name+": "+d.Error)
				}
			}
		}
	}
	return h
}
