package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type migrationReport struct {
	Version            int            `json:"version"`
	Command            string         `json:"command"`
	Database           string         `json:"database,omitempty"`
	DeploymentID       string         `json:"deployment_id,omitempty"`
	OperationID        string         `json:"operation_id,omitempty"`
	CommitDigest       string         `json:"commit_digest,omitempty"`
	SourceOperationID  string         `json:"source_operation_id,omitempty"`
	SourceCommitDigest string         `json:"source_commit_digest,omitempty"`
	DryRun             bool           `json:"dry_run"`
	StartedAt          string         `json:"started_at"`
	CompletedAt        string         `json:"completed_at"`
	Summary            map[string]int `json:"summary"`
	Entries            []reportEntry  `json:"entries"`
}

type reportEntry struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	UserID      int    `json:"user_id,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Status      string `json:"status"`
	Source      string `json:"source,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	ETag        string `json:"etag,omitempty"`
	Error       string `json:"error,omitempty"`
}

func newMigrationReport(command string, dryRun bool) migrationReport {
	return migrationReport{
		Version: 1, Command: command, DryRun: dryRun,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Summary:   make(map[string]int), Entries: []reportEntry{},
	}
}

func reportEntryFor(record imageRecord) reportEntry {
	return reportEntry{
		Kind: record.Kind, ID: record.ID, ObjectKey: record.ObjectKey,
		SizeBytes: record.ObjectSize, SHA256: record.ObjectSHA256, ETag: record.ObjectETag,
	}
}

func (r *migrationReport) add(entry reportEntry) {
	r.Entries = append(r.Entries, entry)
	r.Summary[entry.Status]++
	r.Summary["total"]++
}

func (r *migrationReport) finish() {
	r.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func writeMigrationReport(path string, report migrationReport) error {
	data, err := common.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal migration report: %w", err)
	}
	data = append(data, '\n')
	if path == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if path == "" {
		return errors.New("--report is required (use - for stdout)")
	}
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".mujian-migration-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write migration report: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish migration report: %w", err)
	}
	if err = syncMigrationReportDirectory(directory); err != nil {
		return err
	}
	return nil
}

func reserveMigrationReport(path string) error {
	if path == "-" {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("report path %q already exists; choose a new path", path)
	}
	if err != nil {
		return fmt.Errorf("reserve migration report: %w", err)
	}
	if err = file.Sync(); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		return fmt.Errorf("reserve migration report: %w", err)
	}
	return syncMigrationReportDirectory(directory)
}

func syncMigrationReportDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open report directory for sync: %w", err)
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return fmt.Errorf("sync report directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close report directory: %w", closeErr)
	}
	return nil
}
