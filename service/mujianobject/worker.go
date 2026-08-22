package mujianobject

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	defaultDeleteInterval = 30 * time.Second
	defaultDeleteBatch    = 50
	// The OBS transport has bounded connection/socket timeouts. Keep recovery
	// comfortably beyond an ordinary upload so an in-flight request is not reaped.
	staleUploadAfter = 45 * time.Minute
	maxDeleteBackoff = time.Hour
)

type DeleteWorkerOptions struct {
	Interval  time.Duration
	BatchSize int
	OnError   func(error)
}

type DeleteWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func ProcessPendingDeletes(ctx context.Context, db *gorm.DB, store Store, limit int) (int, error) {
	if db == nil || store == nil {
		return 0, errors.New("database and object store are required")
	}
	if limit <= 0 {
		return 0, errors.New("delete batch limit must be positive")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	if err := recoverStalePendingUploads(db.WithContext(ctx), now); err != nil {
		return 0, err
	}
	var operations []model.MujianObjectOperation
	if err := db.WithContext(ctx).
		Where("status = ? AND next_attempt_at <= ?", model.MujianObjectStatusPendingDelete, now).
		Order("next_attempt_at, updated_at, id").Limit(limit).Find(&operations).Error; err != nil {
		return 0, fmt.Errorf("load pending object deletes: %w", err)
	}

	deleted := 0
	var deleteErrors []error
	for _, operation := range operations {
		shouldDelete := false
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			shouldDelete, err = model.PrepareMujianObjectRemoteDelete(tx, operation.ObjectKey)
			return err
		})
		if err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("prepare pending object delete: %w", err))
			continue
		}
		if !shouldDelete {
			continue
		}
		if err := deleteRemote(ctx, store, operation.ObjectKey); err != nil {
			if recordErr := recordDeleteFailure(db.WithContext(ctx), operation, err); recordErr != nil {
				deleteErrors = append(deleteErrors, errors.Join(err, recordErr))
			} else {
				deleteErrors = append(deleteErrors, err)
			}
			continue
		}
		result := db.WithContext(ctx).
			Where("id = ? AND status = ?", operation.ID, model.MujianObjectStatusPendingDelete).
			Delete(&model.MujianObjectOperation{})
		if result.Error != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("remove deleted object operation: %w", result.Error))
			continue
		}
		if result.RowsAffected == 1 {
			deleted++
		}
	}
	return deleted, errors.Join(deleteErrors...)
}

func recoverStalePendingUploads(db *gorm.DB, nowMillis int64) error {
	staleBefore := nowMillis - staleUploadAfter.Milliseconds()
	return db.Model(&model.MujianObjectOperation{}).
		Where("status = ? AND updated_at <= ?", model.MujianObjectStatusPendingUpload, staleBefore).
		Updates(map[string]interface{}{
			"status":          model.MujianObjectStatusPendingDelete,
			"next_attempt_at": 0,
			"last_error":      "pending upload exceeded recovery timeout",
			"updated_at":      nowMillis,
		}).Error
}

type remoteDeleter interface {
	deleteRemote(context.Context, string) error
}

func deleteRemote(ctx context.Context, store Store, objectKey string) error {
	if direct, ok := store.(remoteDeleter); ok {
		return direct.deleteRemote(ctx, objectKey)
	}
	return store.Delete(ctx, objectKey)
}

func recordDeleteFailure(db *gorm.DB, operation model.MujianObjectOperation, deleteErr error) error {
	attemptCount := operation.AttemptCount + 1
	nextAttempt := time.Now().Add(deleteBackoff(attemptCount)).UnixMilli()
	return model.RecordMujianObjectDeleteFailure(db, operation.ObjectKey, deleteErr.Error(), nextAttempt)
}

func deleteBackoff(attemptCount int) time.Duration {
	if attemptCount < 1 {
		attemptCount = 1
	}
	backoff := time.Second * time.Duration(1<<min(attemptCount-1, 12))
	if backoff > maxDeleteBackoff {
		return maxDeleteBackoff
	}
	return backoff
}

func StartDeleteWorker(parent context.Context, db *gorm.DB, store Store, options DeleteWorkerOptions) (*DeleteWorker, error) {
	if parent == nil || db == nil || store == nil {
		return nil, errors.New("parent context, database, and object store are required")
	}
	if options.Interval <= 0 {
		options.Interval = defaultDeleteInterval
	}
	if options.BatchSize <= 0 {
		options.BatchSize = defaultDeleteBatch
	}
	ctx, cancel := context.WithCancel(parent)
	worker := &DeleteWorker{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(worker.done)
		worker.process(ctx, db, store, options)
	}()
	return worker, nil
}

func (worker *DeleteWorker) process(ctx context.Context, db *gorm.DB, store Store, options DeleteWorkerOptions) {
	process := func() {
		_, err := ProcessPendingDeletes(ctx, db, store, options.BatchSize)
		if err != nil && !errors.Is(err, context.Canceled) && options.OnError != nil {
			options.OnError(err)
		}
	}
	process()
	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			process()
		}
	}
}

func (worker *DeleteWorker) Stop(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	worker.once.Do(worker.cancel)
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
