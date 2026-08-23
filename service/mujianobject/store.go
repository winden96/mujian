package mujianobject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type durableStore struct {
	db      *gorm.DB
	backend Backend
}

func newDurableStore(db *gorm.DB, backend Backend) (*durableStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if backend == nil {
		return nil, errors.New("object backend is required")
	}
	return &durableStore{db: db, backend: backend}, nil
}

func (store *durableStore) BuildKey(kind ObjectKind, id, contentType string) (string, error) {
	return store.backend.BuildKey(kind, id, contentType)
}

func (store *durableStore) Upload(ctx context.Context, input UploadInput) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	// Validate the provider boundary before buffering content or persisting an
	// operation that the cleanup worker could never safely process.
	if _, err := store.backend.PublicURL(input.ObjectKey); err != nil {
		return Object{}, err
	}
	data, contentType, checksum, err := prepareUpload(input)
	if err != nil {
		return Object{}, err
	}
	operation := model.MujianObjectOperation{
		ObjectKey: input.ObjectKey, ContentType: contentType,
		SizeBytes: input.SizeBytes, SHA256: checksum,
	}
	if err := model.BeginMujianObjectUpload(store.db.WithContext(ctx), &operation); err != nil {
		return Object{}, fmt.Errorf("record pending object upload: %w", err)
	}
	// HEAD before PUT keeps immutable URLs safe even if bucket versioning makes
	// the provider's forbid-overwrite header ineffective. Matching bytes are an
	// idempotent retry; different bytes permanently quarantine the key.
	existing, headErr := store.backend.Head(ctx, input.ObjectKey)
	if headErr == nil {
		if validateErr := validateUploadedObject(existing, input.ObjectKey, contentType, input.SizeBytes, checksum); validateErr == nil {
			return existing, nil
		} else {
			quarantineErr := model.MarkMujianObjectConflict(store.db.WithContext(ctx), input.ObjectKey, validateErr.Error())
			return Object{}, errors.Join(model.ErrMujianObjectStateConflict, validateErr, quarantineErr)
		}
	}
	if !errors.Is(headErr, ErrObjectNotFound) {
		return Object{}, store.enqueueFailedUpload(input.ObjectKey, fmt.Errorf("check existing object: %w", headErr))
	}

	_, err = store.backend.Put(ctx, PutInput{
		Key: input.ObjectKey, ContentType: contentType, Body: bytes.NewReader(data),
		SizeBytes: input.SizeBytes, SHA256: checksum,
	})
	objectAlreadyExists := errors.Is(err, ErrObjectAlreadyExists)
	if err != nil && !objectAlreadyExists {
		return Object{}, store.enqueueFailedUpload(input.ObjectKey, fmt.Errorf("put object: %w", err))
	}
	object, err := store.backend.Head(ctx, input.ObjectKey)
	if err != nil {
		return Object{}, store.enqueueFailedUpload(input.ObjectKey, fmt.Errorf("verify object: %w", err))
	}
	if err := validateUploadedObject(object, input.ObjectKey, contentType, input.SizeBytes, checksum); err != nil {
		if objectAlreadyExists {
			quarantineErr := model.MarkMujianObjectConflict(store.db.WithContext(ctx), input.ObjectKey, err.Error())
			return Object{}, errors.Join(model.ErrMujianObjectStateConflict, err, quarantineErr)
		}
		return Object{}, store.enqueueFailedUpload(input.ObjectKey, err)
	}
	if object.PublicURL == "" {
		object.PublicURL, err = store.backend.PublicURL(input.ObjectKey)
		if err != nil {
			return Object{}, store.enqueueFailedUpload(input.ObjectKey, err)
		}
	}
	return object, nil
}

func prepareUpload(input UploadInput) ([]byte, string, string, error) {
	if strings.TrimSpace(input.ObjectKey) == "" {
		return nil, "", "", errors.New("object key is required")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(input.ContentType))
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid content type: %w", err)
	}
	if _, ok := imageExtensions[strings.ToLower(mediaType)]; !ok {
		return nil, "", "", fmt.Errorf("unsupported image content type %q", mediaType)
	}
	if input.Body == nil {
		return nil, "", "", errors.New("upload body is required")
	}
	if input.SizeBytes <= 0 || input.SizeBytes > MaxUploadBytes {
		return nil, "", "", fmt.Errorf("upload size must be between 1 and %d bytes", MaxUploadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(input.Body, input.SizeBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read upload body: %w", err)
	}
	if int64(len(data)) != input.SizeBytes {
		return nil, "", "", fmt.Errorf("upload body size %d does not match declared size %d", len(data), input.SizeBytes)
	}
	digest := sha256.Sum256(data)
	return data, strings.ToLower(mediaType), hex.EncodeToString(digest[:]), nil
}

func validateUploadedObject(object Object, key, contentType string, sizeBytes int64, checksum string) error {
	if object.Key != key {
		return errors.New("verified object key does not match upload")
	}
	if object.SizeBytes != sizeBytes {
		return fmt.Errorf("verified object size %d does not match upload size %d", object.SizeBytes, sizeBytes)
	}
	if !strings.EqualFold(object.SHA256, checksum) {
		return errors.New("verified object checksum does not match upload")
	}
	if !strings.EqualFold(object.ContentType, contentType) {
		return errors.New("verified object content type does not match upload")
	}
	if strings.TrimSpace(object.ETag) == "" {
		return errors.New("verified object ETag is empty")
	}
	return nil
}

func (store *durableStore) enqueueFailedUpload(objectKey string, uploadErr error) error {
	cleanupErr := model.EnqueueMujianObjectDeletes(store.db, []string{objectKey})
	if cleanupErr == nil {
		return uploadErr
	}
	return errors.Join(uploadErr, fmt.Errorf("enqueue object cleanup: %w", cleanupErr))
}

func (store *durableStore) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	return store.backend.Get(ctx, key)
}

func (store *durableStore) Head(ctx context.Context, key string) (Object, error) {
	return store.backend.Head(ctx, key)
}

func (store *durableStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// PublicURL performs the provider's key validation without making a remote
	// request. Invalid/out-of-prefix keys must never enter the durable ledger.
	if _, err := store.backend.PublicURL(key); err != nil {
		return err
	}
	shouldDelete := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := model.EnqueueMujianObjectDeletes(tx, []string{key}); err != nil {
			return err
		}
		var err error
		shouldDelete, err = model.PrepareMujianObjectRemoteDelete(tx, key)
		return err
	})
	if err != nil {
		return fmt.Errorf("prepare object delete: %w", err)
	}
	if !shouldDelete {
		return nil
	}
	if err := store.deleteRemote(ctx, key); err != nil {
		nextAttemptAt := time.Now().Add(deleteBackoff(1)).UnixMilli()
		if recordErr := model.RecordMujianObjectDeleteFailure(store.db.WithContext(ctx), key, err.Error(), nextAttemptAt); recordErr != nil {
			return errors.Join(err, recordErr)
		}
		return err
	}
	return store.db.WithContext(ctx).
		Where("object_key = ? AND status = ?", key, model.MujianObjectStatusPendingDelete).
		Delete(&model.MujianObjectOperation{}).Error
}

func (store *durableStore) deleteRemote(ctx context.Context, key string) error {
	return store.backend.Delete(ctx, key)
}

func (store *durableStore) PublicURL(key string) (string, error) {
	return store.backend.PublicURL(key)
}

func (store *durableStore) close() {
	if closer, ok := store.backend.(interface{ close() }); ok {
		closer.close()
	}
}
