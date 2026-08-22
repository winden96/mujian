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
	Version     int            `json:"version"`
	Command     string         `json:"command"`
	DryRun      bool           `json:"dry_run"`
	StartedAt   string         `json:"started_at"`
	CompletedAt string         `json:"completed_at"`
	Summary     map[string]int `json:"summary"`
	Entries     []reportEntry  `json:"entries"`
}

type reportEntry struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
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
	return nil
}
