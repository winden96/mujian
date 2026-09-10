package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	recordKindGeneration = "generations"
	recordKindReference  = "references"
	recordKindShot       = "shots"
)

type storedObject struct {
	Key         string
	ContentType string
	SizeBytes   int64
	SHA256      string
	ETag        string
}

type migrationStore interface {
	BuildKey(kind, id, contentType string) (string, error)
	Upload(ctx context.Context, key, contentType string, body io.Reader, sizeBytes int64) (storedObject, error)
	Head(ctx context.Context, key string) (storedObject, error)
	Delete(ctx context.Context, key string) error
}

type imageRecord struct {
	Kind         string
	ID           string
	LegacyMIME   string
	LegacyData   []byte
	LegacyURL    string
	ObjectKey    string
	ObjectSize   int64
	ObjectSHA256 string
	ObjectETag   string
}

type migrationEngine struct {
	db           *gorm.DB
	store        migrationStore
	httpSource   *legacyHTTPSource
	database     string
	deploymentID string
}

func (e *migrationEngine) imagesToOBS(ctx context.Context, apply bool) (migrationReport, error) {
	report := newMigrationReport("images-to-obs", !apply)
	if apply {
		if err := e.reconcilePendingDeletes(ctx); err != nil {
			report.add(reportEntry{
				Kind: "object_lifecycle", ID: "pending_delete", Status: "error",
				Error: fmt.Sprintf("reconcile pending object deletes: %v", err),
			})
			report.finish()
			return report, fmt.Errorf("reconcile pending object deletes: %w", err)
		}
	}
	err := e.walkRecords(func(record imageRecord) error {
		entry := reportEntryFor(record)
		if record.ObjectKey != "" {
			object, headErr := e.store.Head(ctx, record.ObjectKey)
			if headErr != nil {
				entry.Status = "error"
				entry.Error = fmt.Sprintf("head existing object: %v", headErr)
				report.add(entry)
				return nil
			}
			if validateErr := e.validateTrackedObject(record, object); validateErr != nil {
				entry.Status = "error"
				entry.Error = validateErr.Error()
				report.add(entry)
				return nil
			}
			entry.Status = "already_migrated"
			entry.ContentType = object.ContentType
			report.add(entry)
			return nil
		}

		content, source, err := e.materialize(ctx, record)
		entry.Source = source
		if err != nil {
			if errors.Is(err, errLegacySource404) {
				entry.Status = "source_404"
				entry.Error = "legacy HTTP source returned 404"
				report.add(entry)
				return nil
			}
			entry.Status = "error"
			entry.Error = err.Error()
			report.add(entry)
			return nil
		}

		entry.ContentType = content.contentType
		entry.SizeBytes = int64(len(content.data))
		digest := sha256.Sum256(content.data)
		entry.SHA256 = fmt.Sprintf("%x", digest[:])
		objectKey, err := e.store.BuildKey(record.Kind, record.ID, content.contentType)
		if err != nil {
			entry.Status = "error"
			entry.Error = fmt.Sprintf("build object key: %v", err)
			report.add(entry)
			return nil
		}
		entry.ObjectKey = objectKey
		if !apply {
			entry.Status = "ready"
			report.add(entry)
			return nil
		}

		object, err := e.store.Upload(ctx, objectKey, content.contentType, bytes.NewReader(content.data), int64(len(content.data)))
		if err != nil {
			entry.Status = "error"
			uploadErr := fmt.Errorf("upload object: %w", err)
			entry.Error = uploadErr.Error()
			if !errors.Is(err, model.ErrMujianObjectStateConflict) {
				entry.Error = e.withCompensatingDelete(ctx, objectKey, uploadErr)
			}
			report.add(entry)
			return nil
		}
		if err = validateUploadedObject(object, objectKey, content.contentType, int64(len(content.data)), entry.SHA256); err != nil {
			entry.Status = "error"
			entry.Error = e.withCompensatingDelete(ctx, objectKey, err)
			report.add(entry)
			return nil
		}
		if err = e.persistObject(record, object); err != nil {
			entry.Status = "error"
			entry.Error = e.withCompensatingDelete(ctx, objectKey, fmt.Errorf("persist metadata: %w", err))
			report.add(entry)
			return nil
		}

		entry.Status = "migrated"
		entry.ETag = object.ETag
		report.add(entry)
		return nil
	})
	report.finish()
	if err != nil {
		return report, err
	}
	if report.Summary["error"] > 0 {
		return report, fmt.Errorf("image migration has %d error(s)", report.Summary["error"])
	}
	return report, nil
}

func (e *migrationEngine) verify(ctx context.Context) (migrationReport, error) {
	report := newMigrationReport("verify", false)
	err := e.walkRecords(func(record imageRecord) error {
		entry := reportEntryFor(record)
		if record.ObjectKey == "" {
			_, source, sourceErr := e.materialize(ctx, record)
			entry.Source = source
			switch {
			case errors.Is(sourceErr, errLegacySource404):
				entry.Status = "source_404"
				entry.Error = "legacy HTTP source returned 404"
			case sourceErr != nil:
				entry.Status = "error"
				entry.Error = sourceErr.Error()
			default:
				entry.Status = "not_migrated"
				entry.Error = "recoverable image has no OBS object key"
			}
			report.add(entry)
			return nil
		}

		object, headErr := e.store.Head(ctx, record.ObjectKey)
		if headErr != nil {
			entry.Status = "error"
			entry.Error = fmt.Sprintf("head object: %v", headErr)
			report.add(entry)
			return nil
		}
		if err := e.validateTrackedObject(record, object); err != nil {
			entry.Status = "error"
			entry.Error = err.Error()
			report.add(entry)
			return nil
		}
		entry.Status = "verified"
		entry.ContentType = object.ContentType
		entry.SizeBytes = object.SizeBytes
		entry.SHA256 = object.SHA256
		entry.ETag = object.ETag
		report.add(entry)
		return nil
	})
	report.finish()
	if err != nil {
		return report, err
	}
	failures := report.Summary["error"] + report.Summary["not_migrated"]
	if failures > 0 {
		return report, fmt.Errorf("image verification has %d failure(s)", failures)
	}
	return report, nil
}

func (e *migrationEngine) inventory() (migrationReport, error) {
	report := newMigrationReport("report", false)
	err := e.walkRecords(func(record imageRecord) error {
		entry := reportEntryFor(record)
		switch {
		case record.ObjectKey != "":
			entry.Status = "migrated"
		case len(record.LegacyData) > 0:
			entry.Source = "database_blob"
			entry.Status = "legacy"
		case strings.HasPrefix(strings.TrimSpace(record.LegacyURL), "data:"):
			entry.Source = "data_url"
			entry.Status = "legacy"
		case strings.TrimSpace(record.LegacyURL) != "":
			entry.Source = "http_url"
			entry.Status = "legacy"
		default:
			entry.Status = "no_source"
		}
		report.add(entry)
		return nil
	})
	report.finish()
	return report, err
}

func (e *migrationEngine) walkRecords(visit func(imageRecord) error) error {
	const batchSize = 50

	var generations []model.MujianImageGeneration
	err := e.db.Where("result_object_key <> ? OR length(result_data) > 0 OR result_url <> ?", "", "").
		Order("id ASC").FindInBatches(&generations, batchSize, func(_ *gorm.DB, _ int) error {
		for index := range generations {
			row := &generations[index]
			if err := visit(imageRecord{
				Kind: recordKindGeneration, ID: row.ID, LegacyMIME: row.ResultMIMEType,
				LegacyData: row.ResultData, LegacyURL: row.ResultURL,
				ObjectKey: row.ResultObjectKey, ObjectSize: row.ResultSizeBytes,
				ObjectSHA256: row.ResultSHA256, ObjectETag: row.ResultETag,
			}); err != nil {
				return err
			}
		}
		return nil
	}).Error
	if err != nil {
		return fmt.Errorf("list image generations: %w", err)
	}

	var references []model.MujianImageReference
	err = e.db.Where("object_key <> ? OR length(data) > 0", "").Order("id ASC").
		FindInBatches(&references, batchSize, func(_ *gorm.DB, _ int) error {
			for index := range references {
				row := &references[index]
				if err := visit(imageRecord{
					Kind: recordKindReference, ID: row.ID, LegacyMIME: row.MIMEType,
					LegacyData: row.Data, ObjectKey: row.ObjectKey, ObjectSize: row.ObjectSizeBytes,
					ObjectSHA256: row.ObjectSHA256, ObjectETag: row.ObjectETag,
				}); err != nil {
					return err
				}
			}
			return nil
		}).Error
	if err != nil {
		return fmt.Errorf("list image references: %w", err)
	}

	var shots []model.MujianShot
	err = e.db.Where("result_object_key <> ? OR result_url <> ?", "", "").Order("id ASC").
		FindInBatches(&shots, batchSize, func(_ *gorm.DB, _ int) error {
			for index := range shots {
				row := &shots[index]
				if err := visit(imageRecord{
					Kind: recordKindShot, ID: row.ID, LegacyURL: row.ResultURL,
					ObjectKey: row.ResultObjectKey, ObjectSize: row.ResultSizeBytes,
					ObjectSHA256: row.ResultSHA256, ObjectETag: row.ResultETag,
				}); err != nil {
					return err
				}
			}
			return nil
		}).Error
	if err != nil {
		return fmt.Errorf("list legacy shots: %w", err)
	}
	return nil
}

func (e *migrationEngine) persistObject(record imageRecord, object storedObject) error {
	return e.db.Transaction(func(tx *gorm.DB) error {
		var updates map[string]interface{}
		var result *gorm.DB
		switch record.Kind {
		case recordKindGeneration:
			updates = map[string]interface{}{
				"result_object_key": object.Key, "result_size_bytes": object.SizeBytes,
				"result_sha256": object.SHA256, "result_etag": object.ETag,
			}
			result = tx.Model(&model.MujianImageGeneration{}).
				Where("id = ? AND result_object_key = ?", record.ID, "").Updates(updates)
		case recordKindReference:
			updates = map[string]interface{}{
				"object_key": object.Key, "object_size_bytes": object.SizeBytes,
				"object_sha256": object.SHA256, "object_etag": object.ETag,
			}
			result = tx.Model(&model.MujianImageReference{}).
				Where("id = ? AND object_key = ?", record.ID, "").Updates(updates)
		case recordKindShot:
			updates = map[string]interface{}{
				"result_object_key": object.Key, "result_size_bytes": object.SizeBytes,
				"result_sha256": object.SHA256, "result_etag": object.ETag,
			}
			result = tx.Model(&model.MujianShot{}).
				Where("id = ? AND result_object_key = ?", record.ID, "").Updates(updates)
		default:
			return fmt.Errorf("unsupported image record kind %q", record.Kind)
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("image row changed concurrently")
		}
		return model.ActivateMujianObject(tx, object.Key, object.ETag)
	})
}

func (e *migrationEngine) withCompensatingDelete(ctx context.Context, objectKey string, cause error) string {
	// Cleanup must still run when the migration request itself was cancelled or
	// timed out. OBS requests are independently bounded by the cleanup deadline.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if cleanupErr := e.store.Delete(cleanupCtx, objectKey); cleanupErr != nil {
		return fmt.Sprintf("%v; compensating delete remains pending: %v", cause, cleanupErr)
	}
	return fmt.Sprintf("%v; compensating delete completed", cause)
}

func (e *migrationEngine) reconcilePendingDeletes(ctx context.Context) error {
	const batchSize = 100

	for {
		var operations []model.MujianObjectOperation
		if err := e.db.WithContext(ctx).
			Where("status = ?", model.MujianObjectStatusPendingDelete).
			Order("updated_at, id").Limit(batchSize).Find(&operations).Error; err != nil {
			return fmt.Errorf("load pending object deletes: %w", err)
		}
		if len(operations) == 0 {
			return nil
		}

		var cleanupErrors []error
		for _, operation := range operations {
			if err := e.store.Delete(ctx, operation.ObjectKey); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete %q: %w", operation.ObjectKey, err))
			}
		}
		if err := errors.Join(cleanupErrors...); err != nil {
			return err
		}
	}
}

func validateUploadedObject(object storedObject, key, contentType string, sizeBytes int64, digest string) error {
	if object.Key != key || object.ContentType != contentType || object.SizeBytes != sizeBytes || object.SHA256 != digest {
		return errors.New("uploaded object metadata does not match source")
	}
	if strings.TrimSpace(object.ETag) == "" {
		return errors.New("uploaded object has no ETag")
	}
	return nil
}

func validateRecordedObject(record imageRecord, object storedObject) error {
	if object.Key != record.ObjectKey {
		return errors.New("OBS object key mismatch")
	}
	if !allowedImageContentType(object.ContentType) {
		return fmt.Errorf("OBS content type %q is not a supported image type", object.ContentType)
	}
	if record.ObjectSize <= 0 || record.ObjectSHA256 == "" || record.ObjectETag == "" {
		return errors.New("database object metadata is incomplete")
	}
	if object.SizeBytes != record.ObjectSize {
		return fmt.Errorf("OBS size mismatch: database=%d OBS=%d", record.ObjectSize, object.SizeBytes)
	}
	if object.SHA256 != record.ObjectSHA256 {
		return errors.New("OBS SHA-256 metadata mismatch")
	}
	if object.ETag != record.ObjectETag {
		return errors.New("OBS ETag mismatch")
	}
	return nil
}

func (e *migrationEngine) validateTrackedObject(record imageRecord, object storedObject) error {
	if err := validateRecordedObject(record, object); err != nil {
		return err
	}
	var operation model.MujianObjectOperation
	if err := e.db.Where("object_key = ?", record.ObjectKey).First(&operation).Error; err != nil {
		return fmt.Errorf("load object lifecycle state: %w", err)
	}
	if operation.Status != model.MujianObjectStatusActive {
		return fmt.Errorf("object lifecycle state is %q, expected active", operation.Status)
	}
	if operation.ContentType != object.ContentType || operation.SizeBytes != record.ObjectSize ||
		operation.SHA256 != record.ObjectSHA256 || operation.ETag != record.ObjectETag {
		return errors.New("object lifecycle metadata does not match image row")
	}
	return nil
}
