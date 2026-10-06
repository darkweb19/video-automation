package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var (
	errProjectSubmissionConflict    = errors.New("project request ID was already used for different input")
	errProjectSubmissionUnavailable = errors.New("project request was accepted but the project is no longer available")
)

// Optional for older API clients. UUIDs and other bounded opaque ASCII keys
// are supported; the value identifies a submission and is never authorization.
func validProjectRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func (input projectSubmission) requestHash() string {
	// Hash the normalized submitted options, including an omitted model/account.
	// A replay keeps its original provider snapshot even if Settings changed.
	input.RequestID = ""
	encoded, _ := json.Marshal(input)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (s *Store) projectForSubmission(requestID, requestHash string) (VideoProject, error) {
	var id, existingHash string
	if err := s.db.QueryRow(`SELECT project_id,request_hash FROM project_submissions WHERE request_id=?`, requestID).Scan(&id, &existingHash); err != nil {
		return VideoProject{}, err
	}
	if requestHash != "" && requestHash != existingHash {
		return VideoProject{}, errProjectSubmissionConflict
	}
	project, err := s.projectLiveSnapshot(id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (project.InVault || project.Status == "deleting")) {
		return VideoProject{}, errProjectSubmissionUnavailable
	}
	return project, err
}
