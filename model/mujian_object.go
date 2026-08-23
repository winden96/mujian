package model

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MujianObjectStatusPendingUpload = "pending_upload"
	MujianObjectStatusActive        = "active"
	MujianObjectStatusPendingDelete = "pending_delete"
	MujianObjectStatusConflict      = "conflict"
)

var ErrMujianObjectStateConflict = errors.New("mujian object state conflict")

// MujianObjectOperation is the durable lifecycle record for an external image
// object. It is state, not an audit log: a successfully deleted object has no
// remaining row.
type MujianObjectOperation struct {
	ID            string `json:"-" gorm:"type:varchar(36);primaryKey"`
	ObjectKey     string `json:"-" gorm:"type:varchar(191);uniqueIndex;not null"`
	Status        string `json:"-" gorm:"type:varchar(24);index;not null"`
	ContentType   string `json:"-" gorm:"type:varchar(64)"`
	SizeBytes     int64  `json:"-" gorm:"not null;default:0"`
	SHA256        string `json:"-" gorm:"type:varchar(64)"`
	ETag          string `json:"-" gorm:"column:etag;type:varchar(191)"`
	AttemptCount  int    `json:"-" gorm:"not null;default:0"`
	NextAttemptAt int64  `json:"-" gorm:"index;not null;default:0"`
	LastError     string `json:"-" gorm:"type:text"`
	CreatedAt     int64  `json:"-" gorm:"autoCreateTime:milli"`
	UpdatedAt     int64  `json:"-" gorm:"autoUpdateTime:milli"`
}

func mujianObjectOperationID(objectKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("mujian-object:"+objectKey)).String()
}

// BeginMujianObjectUpload records intent before the external upload starts.
// OnConflict makes retries with the same deterministic key idempotent across
// SQLite, MySQL, and PostgreSQL.
func BeginMujianObjectUpload(tx *gorm.DB, operation *MujianObjectOperation) error {
	if tx == nil || operation == nil {
		return errors.New("database and object operation are required")
	}
	operation.ObjectKey = strings.TrimSpace(operation.ObjectKey)
	if operation.ObjectKey == "" {
		return errors.New("object key is required")
	}
	operation.ID = mujianObjectOperationID(operation.ObjectKey)
	operation.Status = MujianObjectStatusPendingUpload
	operation.AttemptCount = 0
	operation.NextAttemptAt = 0
	operation.LastError = ""
	operation.ETag = ""
	operation.UpdatedAt = time.Now().UnixMilli()
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(operation).Error; err != nil {
		return err
	}
	// Always reload and verify the durable row. MySQL can report a no-op
	// duplicate insert as affected when clientFoundRows is enabled, so
	// RowsAffected cannot safely distinguish insert from conflict here.
	var existing MujianObjectOperation
	if err := tx.Where("object_key = ?", operation.ObjectKey).First(&existing).Error; err != nil {
		return err
	}
	if existing.Status != MujianObjectStatusPendingUpload ||
		existing.ContentType != operation.ContentType ||
		existing.SizeBytes != operation.SizeBytes ||
		!strings.EqualFold(existing.SHA256, operation.SHA256) {
		return ErrMujianObjectStateConflict
	}
	resume := tx.Model(&MujianObjectOperation{}).
		Where("id = ? AND status = ?", existing.ID, MujianObjectStatusPendingUpload).
		UpdateColumn("updated_at", operation.UpdatedAt)
	if resume.Error != nil {
		return resume.Error
	}
	if resume.RowsAffected != 1 {
		return ErrMujianObjectStateConflict
	}
	return nil
}

// ActivateMujianObject completes the pending-upload transition. The status
// predicate prevents a concurrent delete request from being overwritten.
func ActivateMujianObject(tx *gorm.DB, objectKey, etag string) error {
	if tx == nil {
		return errors.New("database is required")
	}
	objectKey = strings.TrimSpace(objectKey)
	etag = strings.TrimSpace(etag)
	if objectKey == "" || etag == "" {
		return errors.New("object key and ETag are required")
	}
	result := tx.Model(&MujianObjectOperation{}).
		Where("object_key = ? AND status = ?", objectKey, MujianObjectStatusPendingUpload).
		Updates(map[string]interface{}{
			"status":          MujianObjectStatusActive,
			"etag":            etag,
			"attempt_count":   0,
			"next_attempt_at": 0,
			"last_error":      "",
			"updated_at":      time.Now().UnixMilli(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var existing MujianObjectOperation
		if err := tx.Where("object_key = ?", objectKey).First(&existing).Error; err != nil {
			return err
		}
		if existing.Status == MujianObjectStatusActive && existing.ETag == etag {
			return nil
		}
		return ErrMujianObjectStateConflict
	}
	return nil
}

// EnqueueMujianObjectDeletes is safe to call inside a project/user deletion
// transaction. Network deletion is deliberately deferred until after commit.
func EnqueueMujianObjectDeletes(tx *gorm.DB, objectKeys []string) error {
	if tx == nil {
		return errors.New("database is required")
	}
	now := time.Now().UnixMilli()
	seen := make(map[string]struct{}, len(objectKeys))
	for _, rawKey := range objectKeys {
		objectKey := strings.TrimSpace(rawKey)
		if objectKey == "" {
			continue
		}
		if _, exists := seen[objectKey]; exists {
			continue
		}
		seen[objectKey] = struct{}{}
		operation := MujianObjectOperation{
			ID: mujianObjectOperationID(objectKey), ObjectKey: objectKey,
			Status: MujianObjectStatusPendingDelete, UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "object_key"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"status":          MujianObjectStatusPendingDelete,
				"next_attempt_at": 0,
				"last_error":      "",
				"updated_at":      now,
			}),
		}).Create(&operation).Error; err != nil {
			return err
		}
	}
	return nil
}

// RecordMujianObjectDeleteFailure atomically advances the durable retry state.
func RecordMujianObjectDeleteFailure(tx *gorm.DB, objectKey, message string, nextAttemptAt int64) error {
	if tx == nil {
		return errors.New("database is required")
	}
	message = truncateMujianObjectError(message)
	return tx.Model(&MujianObjectOperation{}).
		Where("object_key = ? AND status = ?", objectKey, MujianObjectStatusPendingDelete).
		Updates(map[string]interface{}{
			"attempt_count":   gorm.Expr("attempt_count + ?", 1),
			"next_attempt_at": nextAttemptAt,
			"last_error":      message,
			"updated_at":      time.Now().UnixMilli(),
		}).Error
}

// MarkMujianObjectConflict permanently quarantines a key that already exists
// with different bytes. A public immutable URL must never be deleted and later
// reused for different content, even when no current database row references it.
func MarkMujianObjectConflict(tx *gorm.DB, objectKey, message string) error {
	if tx == nil {
		return errors.New("database is required")
	}
	result := tx.Model(&MujianObjectOperation{}).
		Where("object_key = ? AND status = ?", objectKey, MujianObjectStatusPendingUpload).
		Updates(map[string]interface{}{
			"status":          MujianObjectStatusConflict,
			"next_attempt_at": 0,
			"last_error":      truncateMujianObjectError(message),
			"updated_at":      time.Now().UnixMilli(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMujianObjectStateConflict
	}
	return nil
}

func truncateMujianObjectError(message string) string {
	if len(message) <= 2048 {
		return message
	}
	message = message[:2048]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

// PrepareMujianObjectRemoteDelete locks the lifecycle row and checks all live
// image references before allowing an irreversible provider delete. This
// protects the PostgreSQL "commit succeeded but the client saw an error" case:
// a compensating delete observes the committed reference and restores active.
func PrepareMujianObjectRemoteDelete(tx *gorm.DB, objectKey string) (bool, error) {
	if tx == nil {
		return false, errors.New("database is required")
	}
	var operation MujianObjectOperation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("object_key = ?", objectKey).First(&operation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if operation.Status != MujianObjectStatusPendingDelete {
		return false, nil
	}
	referenced, err := mujianObjectIsReferenced(tx, objectKey)
	if err != nil {
		return false, err
	}
	if !referenced {
		return true, nil
	}
	if err := tx.Model(&MujianObjectOperation{}).
		Where("id = ? AND status = ?", operation.ID, MujianObjectStatusPendingDelete).
		Updates(map[string]interface{}{
			"status":          MujianObjectStatusActive,
			"attempt_count":   0,
			"next_attempt_at": 0,
			"last_error":      "",
			"updated_at":      time.Now().UnixMilli(),
		}).Error; err != nil {
		return false, err
	}
	return false, nil
}

func mujianObjectIsReferenced(tx *gorm.DB, objectKey string) (bool, error) {
	queries := []*gorm.DB{
		tx.Model(&MujianImageGeneration{}).Where("result_object_key = ?", objectKey),
		tx.Model(&MujianImageReference{}).Where("object_key = ?", objectKey),
		tx.Model(&MujianShot{}).Where("result_object_key = ?", objectKey),
	}
	for _, query := range queries {
		var count int64
		if err := query.Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}
