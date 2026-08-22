package mujianobject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var errFakeObjectNotFound = fmt.Errorf("%w: fake object not found", ErrObjectNotFound)

type fakeStoredObject struct {
	object Object
	data   []byte
}

type fakeBackend struct {
	mu          sync.Mutex
	prefix      string
	objects     map[string]fakeStoredObject
	putErr      error
	deleteErr   error
	putCalls    int
	deleteCalls int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{prefix: "mujian/prod/public", objects: make(map[string]fakeStoredObject)}
}

func (backend *fakeBackend) BuildKey(kind ObjectKind, id, contentType string) (string, error) {
	return BuildObjectKey(backend.prefix, kind, id, contentType)
}

func (backend *fakeBackend) Put(_ context.Context, input PutInput) (Object, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.putCalls++
	if backend.putErr != nil {
		return Object{}, backend.putErr
	}
	if _, exists := backend.objects[input.Key]; exists {
		return Object{}, ErrObjectAlreadyExists
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return Object{}, err
	}
	object := Object{
		Key: input.Key, PublicURL: "https://static.mujianai.com/" + input.Key,
		ContentType: input.ContentType, SizeBytes: int64(len(data)),
		SHA256: input.SHA256, ETag: "etag-" + input.SHA256[:12],
	}
	backend.objects[input.Key] = fakeStoredObject{object: object, data: data}
	return object, nil
}

func (backend *fakeBackend) Get(_ context.Context, key string) (io.ReadCloser, Object, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	stored, exists := backend.objects[key]
	if !exists {
		return nil, Object{}, errFakeObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(stored.data)), stored.object, nil
}

func (backend *fakeBackend) Head(_ context.Context, key string) (Object, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	stored, exists := backend.objects[key]
	if !exists {
		return Object{}, errFakeObjectNotFound
	}
	return stored.object, nil
}

func (backend *fakeBackend) Delete(_ context.Context, key string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.deleteCalls++
	if backend.deleteErr != nil {
		return backend.deleteErr
	}
	delete(backend.objects, key)
	return nil
}

func (backend *fakeBackend) PublicURL(key string) (string, error) {
	if key != strings.TrimSpace(key) || !strings.HasPrefix(key, backend.prefix+"/") {
		return "", errors.New("invalid fake object key")
	}
	return "https://static.mujianai.com/" + key, nil
}

func (backend *fakeBackend) seed(key, contentType string, data []byte) {
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	backend.objects[key] = fakeStoredObject{
		object: Object{
			Key: key, PublicURL: "https://static.mujianai.com/" + key,
			ContentType: contentType, SizeBytes: int64(len(data)), SHA256: checksum,
			ETag: "etag-" + checksum[:12],
		},
		data: append([]byte(nil), data...),
	}
}

func setupObjectStoreTest(t *testing.T) (*gorm.DB, Store, *fakeBackend) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.MujianObjectOperation{}, &model.MujianImageGeneration{},
		&model.MujianImageReference{}, &model.MujianShot{},
	))
	backend := newFakeBackend()
	store, err := NewStore(db, backend)
	require.NoError(t, err)
	return db, store, backend
}

func imageUpload(key string, data []byte) UploadInput {
	return UploadInput{
		ObjectKey: key, ContentType: "image/png",
		Body: bytes.NewReader(data), SizeBytes: int64(len(data)),
	}
}

func operationForKey(t *testing.T, db *gorm.DB, key string) model.MujianObjectOperation {
	t.Helper()
	var operation model.MujianObjectOperation
	require.NoError(t, db.Where("object_key = ?", key).First(&operation).Error)
	return operation
}

func TestUploadUsesTwoPhaseLifecycle(t *testing.T) {
	db, store, _ := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindGeneration, uuid.NewString(), "image/png")
	require.NoError(t, err)

	object, err := store.Upload(context.Background(), imageUpload(key, []byte("image")))
	require.NoError(t, err)
	require.Equal(t, model.MujianObjectStatusPendingUpload, operationForKey(t, db, key).Status)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return model.ActivateMujianObject(tx, object.Key, object.ETag)
	}))
	operation := operationForKey(t, db, key)
	require.Equal(t, model.MujianObjectStatusActive, operation.Status)
	require.Equal(t, object.ETag, operation.ETag)
	// Transaction retries may invoke activation again after an uncertain commit.
	require.NoError(t, model.ActivateMujianObject(db, object.Key, object.ETag))
}

func TestUploadRejectsInvalidKeyBeforeWritingLedger(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	_, err := store.Upload(context.Background(), imageUpload("private/object.png", []byte("image")))
	require.ErrorContains(t, err, "invalid fake object key")
	require.Zero(t, backend.putCalls)
	var count int64
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestRepeatedPendingUploadIsIdempotentAndNeverOverwrites(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindReference, uuid.NewString(), "image/png")
	require.NoError(t, err)
	data := []byte("same-image")

	first, err := store.Upload(context.Background(), imageUpload(key, data))
	require.NoError(t, err)
	second, err := store.Upload(context.Background(), imageUpload(key, data))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, backend.putCalls)
	require.Equal(t, model.MujianObjectStatusPendingUpload, operationForKey(t, db, key).Status)

	_, err = store.Upload(context.Background(), imageUpload(key, []byte("different-image")))
	require.ErrorIs(t, err, model.ErrMujianObjectStateConflict)
	require.Equal(t, 1, backend.putCalls)
}

func TestConcurrentRepeatedUploadIsIdempotent(t *testing.T) {
	_, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindGeneration, uuid.NewString(), "image/png")
	require.NoError(t, err)
	inputData := []byte("concurrent-image")

	errorsCh := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, uploadErr := store.Upload(context.Background(), imageUpload(key, inputData))
			errorsCh <- uploadErr
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for uploadErr := range errorsCh {
		require.NoError(t, uploadErr)
	}
	require.GreaterOrEqual(t, backend.putCalls, 1)
	require.LessOrEqual(t, backend.putCalls, 2)
}

func TestExistingDifferentObjectIsQuarantinedWithoutReusingImmutableURL(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindShot, uuid.NewString(), "image/png")
	require.NoError(t, err)
	backend.seed(key, "image/png", []byte("existing"))

	_, err = store.Upload(context.Background(), imageUpload(key, []byte("incoming")))
	require.ErrorIs(t, err, model.ErrMujianObjectStateConflict)
	require.Equal(t, model.MujianObjectStatusConflict, operationForKey(t, db, key).Status)
	require.Contains(t, backend.objects, key)

	deleted, err := ProcessPendingDeletes(context.Background(), db, store, 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Contains(t, backend.objects, key)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("incoming")))
	require.ErrorIs(t, err, model.ErrMujianObjectStateConflict)
}

func TestStalePendingUploadIsRecoveredAfterRestart(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindGeneration, uuid.NewString(), "image/png")
	require.NoError(t, err)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("orphan")))
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Where("object_key = ?", key).
		UpdateColumn("updated_at", time.Now().Add(-staleUploadAfter-time.Minute).UnixMilli()).Error)

	deleted, err := ProcessPendingDeletes(context.Background(), db, store, 10)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.NotContains(t, backend.objects, key)
	var count int64
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Where("object_key = ?", key).Count(&count).Error)
	require.Zero(t, count)
}

func TestFreshPendingUploadIsNotRecovered(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindGeneration, uuid.NewString(), "image/png")
	require.NoError(t, err)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("in-flight")))
	require.NoError(t, err)

	deleted, err := ProcessPendingDeletes(context.Background(), db, store, 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Contains(t, backend.objects, key)
	require.Equal(t, model.MujianObjectStatusPendingUpload, operationForKey(t, db, key).Status)
}

func TestCompensatingDeletePreservesCommittedReference(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	generationID := uuid.NewString()
	key, err := store.BuildKey(ObjectKindGeneration, generationID, "image/png")
	require.NoError(t, err)
	object, err := store.Upload(context.Background(), imageUpload(key, []byte("committed")))
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		generation := model.MujianImageGeneration{
			ID: generationID, ProjectID: uuid.NewString(), UserID: 7,
			Prompt: "test", Engine: "nano", ModelID: "image", AspectRatio: "1:1",
			Status: "succeeded", TaskID: uuid.NewString(), ResultObjectKey: key,
		}
		if err := tx.Create(&generation).Error; err != nil {
			return err
		}
		return model.ActivateMujianObject(tx, key, object.ETag)
	}))

	// Simulate a caller receiving an uncertain commit result and issuing cleanup.
	require.NoError(t, store.Delete(context.Background(), key))
	require.Zero(t, backend.deleteCalls)
	require.Contains(t, backend.objects, key)
	require.Equal(t, model.MujianObjectStatusActive, operationForKey(t, db, key).Status)
}

func TestDeleteRemovesUnreferencedObjectAndLedger(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindGeneration, uuid.NewString(), "image/png")
	require.NoError(t, err)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("unreferenced")))
	require.NoError(t, err)

	require.NoError(t, store.Delete(context.Background(), key))
	require.Equal(t, 1, backend.deleteCalls)
	require.NotContains(t, backend.objects, key)
	var count int64
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Where("object_key = ?", key).Count(&count).Error)
	require.Zero(t, count)
}

func TestDeleteRejectsInvalidKeyBeforeWritingLedger(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	err := store.Delete(context.Background(), "private/object.png")
	require.ErrorContains(t, err, "invalid fake object key")
	require.Zero(t, backend.deleteCalls)
	var count int64
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestDeleteFailureRemainsDurableForRetry(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindReference, uuid.NewString(), "image/png")
	require.NoError(t, err)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("delete-me")))
	require.NoError(t, err)
	require.NoError(t, model.EnqueueMujianObjectDeletes(db, []string{key}))
	backend.deleteErr = errors.New("temporary OBS failure")

	deleted, err := ProcessPendingDeletes(context.Background(), db, store, 10)
	require.Error(t, err)
	require.Zero(t, deleted)
	operation := operationForKey(t, db, key)
	require.Equal(t, model.MujianObjectStatusPendingDelete, operation.Status)
	require.Equal(t, 1, operation.AttemptCount)
	require.Greater(t, operation.NextAttemptAt, time.Now().UnixMilli())
	require.Contains(t, operation.LastError, "temporary OBS failure")
}

func TestDeleteWorkerProcessesImmediatelyAndStops(t *testing.T) {
	db, store, backend := setupObjectStoreTest(t)
	key, err := store.BuildKey(ObjectKindReference, uuid.NewString(), "image/png")
	require.NoError(t, err)
	_, err = store.Upload(context.Background(), imageUpload(key, []byte("worker-delete")))
	require.NoError(t, err)
	require.NoError(t, model.EnqueueMujianObjectDeletes(db, []string{key}))

	worker, err := StartDeleteWorker(context.Background(), db, store, DeleteWorkerOptions{Interval: time.Hour, BatchSize: 5})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		_, exists := backend.objects[key]
		return !exists
	}, 2*time.Second, 10*time.Millisecond)
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, worker.Stop(stopContext))
}

func TestDeleteWorkerRejectsNilParent(t *testing.T) {
	db, store, _ := setupObjectStoreTest(t)
	worker, err := StartDeleteWorker(nil, db, store, DeleteWorkerOptions{})
	require.Error(t, err)
	require.Nil(t, worker)
}
