package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")

type fakeMigrationStore struct {
	mu          sync.Mutex
	db          *gorm.DB
	objects     map[string]storedObject
	uploadCount int
	failUploads int
}

func newFakeMigrationStore(db *gorm.DB) *fakeMigrationStore {
	return &fakeMigrationStore{db: db, objects: make(map[string]storedObject)}
}

func (s *fakeMigrationStore) BuildKey(kind, id, contentType string) (string, error) {
	extensions, err := mime.ExtensionsByType(contentType)
	if err != nil || len(extensions) == 0 {
		return "", fmt.Errorf("unsupported content type %q", contentType)
	}
	return kind + "/" + id + "/result" + extensions[0], nil
}

func (s *fakeMigrationStore) Upload(_ context.Context, key, contentType string, body io.Reader, sizeBytes int64) (storedObject, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return storedObject{}, err
	}
	digest := sha256.Sum256(data)
	object := storedObject{
		Key: key, ContentType: contentType, SizeBytes: sizeBytes,
		SHA256: fmt.Sprintf("%x", digest[:]), ETag: "test-etag-" + key,
	}
	if err = model.BeginMujianObjectUpload(s.db, &model.MujianObjectOperation{
		ObjectKey: key, ContentType: contentType, SizeBytes: sizeBytes, SHA256: object.SHA256,
	}); err != nil {
		return storedObject{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failUploads > 0 {
		s.failUploads--
		return storedObject{}, errors.New("temporary upload failure")
	}
	s.objects[key] = object
	s.uploadCount++
	return object, nil
}

func (s *fakeMigrationStore) Delete(_ context.Context, key string) error {
	if err := model.EnqueueMujianObjectDeletes(s.db, []string{key}); err != nil {
		return err
	}
	shouldDelete := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		shouldDelete, err = model.PrepareMujianObjectRemoteDelete(tx, key)
		return err
	}); err != nil {
		return err
	}
	if !shouldDelete {
		return nil
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return s.db.Where("object_key = ? AND status = ?", key, model.MujianObjectStatusPendingDelete).
		Delete(&model.MujianObjectOperation{}).Error
}

func (s *fakeMigrationStore) Head(_ context.Context, key string) (storedObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return storedObject{}, fmt.Errorf("object %q not found", key)
	}
	return object, nil
}

func TestImagesToOBSIsIdempotentAndVerifiable(t *testing.T) {
	db := newMigrationTestDB(t)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG)
	require.NoError(t, db.Create(&model.MujianImageGeneration{
		ID: "generation-1", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-1", ResultData: testPNG, ResultMIMEType: "image/png",
	}).Error)
	require.NoError(t, db.Create(&model.MujianImageReference{
		ID: "reference-1", GenerationID: "generation-1", Position: 0, Name: "reference.png",
		MIMEType: "image/png", SizeBytes: int64(len(testPNG)), Data: testPNG,
	}).Error)
	require.NoError(t, db.Create(&model.MujianShot{
		ID: "shot-1", ProjectID: "project-1", SceneID: "scene-1", Sequence: 1,
		DurationSeconds: 3, ResultURL: dataURL, Status: "completed",
	}).Error)

	store := newFakeMigrationStore(db)
	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}

	report, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 3, report.Summary["migrated"])
	require.Equal(t, 3, store.uploadCount)

	var generation model.MujianImageGeneration
	require.NoError(t, db.First(&generation, "id = ?", "generation-1").Error)
	require.NotEmpty(t, generation.ResultObjectKey)
	require.Equal(t, int64(len(testPNG)), generation.ResultSizeBytes)
	require.NotEmpty(t, generation.ResultSHA256)
	require.Equal(t, testPNG, generation.ResultData, "legacy BLOB must be retained during the compatibility release")
	var operation model.MujianObjectOperation
	require.NoError(t, db.First(&operation, "object_key = ?", generation.ResultObjectKey).Error)
	require.Equal(t, model.MujianObjectStatusActive, operation.Status)

	report, err = engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 3, report.Summary["already_migrated"])
	require.Equal(t, 3, store.uploadCount, "a second migration must not upload existing rows")

	verification, err := engine.verify(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, verification.Summary["verified"])
}

func TestImagesToOBSDryRunDoesNotWrite(t *testing.T) {
	db := newMigrationTestDB(t)
	require.NoError(t, db.Create(&model.MujianImageGeneration{
		ID: "generation-dry", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-dry", ResultData: testPNG, ResultMIMEType: "image/png",
	}).Error)

	store := newFakeMigrationStore(db)
	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}
	report, err := engine.imagesToOBS(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, 1, report.Summary["ready"])
	require.Zero(t, store.uploadCount)

	var generation model.MujianImageGeneration
	require.NoError(t, db.First(&generation, "id = ?", "generation-dry").Error)
	require.Empty(t, generation.ResultObjectKey)
}

func TestImagesToOBSUploadFailureIsCompensatedAndRetryable(t *testing.T) {
	db := newMigrationTestDB(t)
	require.NoError(t, db.Create(&model.MujianImageGeneration{
		ID: "generation-retry", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-retry", ResultData: testPNG, ResultMIMEType: "image/png",
	}).Error)

	store := newFakeMigrationStore(db)
	store.failUploads = 1
	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}
	failed, err := engine.imagesToOBS(context.Background(), true)
	require.Error(t, err)
	require.Equal(t, 1, failed.Summary["error"])
	require.Contains(t, failed.Entries[0].Error, "compensating delete completed")
	var pending int64
	require.NoError(t, db.Model(&model.MujianObjectOperation{}).Count(&pending).Error)
	require.Zero(t, pending)

	retried, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 1, retried.Summary["migrated"])
	var generation model.MujianImageGeneration
	require.NoError(t, db.First(&generation, "id = ?", "generation-retry").Error)
	require.NotEmpty(t, generation.ResultObjectKey)
}

func TestImagesToOBSReconcilesPendingDeleteFromPreviousRun(t *testing.T) {
	db := newMigrationTestDB(t)
	record := model.MujianImageGeneration{
		ID: "generation-pending-delete", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-pending-delete", ResultData: testPNG, ResultMIMEType: "image/png",
	}
	require.NoError(t, db.Create(&record).Error)

	store := newFakeMigrationStore(db)
	key, err := store.BuildKey(recordKindGeneration, record.ID, record.ResultMIMEType)
	require.NoError(t, err)
	require.NoError(t, model.EnqueueMujianObjectDeletes(db, []string{key}))
	store.objects[key] = storedObject{Key: key, ContentType: "image/png", SizeBytes: int64(len(testPNG))}

	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}
	report, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 1, report.Summary["migrated"])
	require.Equal(t, 1, store.uploadCount)

	var operation model.MujianObjectOperation
	require.NoError(t, db.First(&operation, "object_key = ?", key).Error)
	require.Equal(t, model.MujianObjectStatusActive, operation.Status)
}

func TestImagesToOBSDoesNotHideAMissingExistingObject(t *testing.T) {
	db := newMigrationTestDB(t)
	require.NoError(t, db.Create(&model.MujianImageGeneration{
		ID: "generation-missing", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-missing", ResultData: testPNG, ResultMIMEType: "image/png",
	}).Error)
	store := newFakeMigrationStore(db)
	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}

	_, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	store.mu.Lock()
	for key := range store.objects {
		delete(store.objects, key)
	}
	store.mu.Unlock()

	report, err := engine.imagesToOBS(context.Background(), true)
	require.Error(t, err)
	require.Equal(t, 1, report.Summary["error"])
	require.Equal(t, 1, store.uploadCount, "immutable keys must not be overwritten when verification fails")
}

func TestImagesToOBSRejectsExistingObjectWithWrongContentType(t *testing.T) {
	db := newMigrationTestDB(t)
	require.NoError(t, db.Create(&model.MujianImageGeneration{
		ID: "generation-wrong-mime", ProjectID: "project-1", UserID: 1, SessionID: "session-1",
		Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: "succeeded", TaskID: "task-wrong-mime", ResultData: testPNG, ResultMIMEType: "image/png",
	}).Error)
	store := newFakeMigrationStore(db)
	engine := migrationEngine{db: db, store: store, httpSource: newLegacyHTTPSource(time.Second)}

	_, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err)
	store.mu.Lock()
	for key, object := range store.objects {
		object.ContentType = "application/octet-stream"
		store.objects[key] = object
	}
	store.mu.Unlock()

	report, err := engine.imagesToOBS(context.Background(), true)
	require.Error(t, err)
	require.Equal(t, 1, report.Summary["error"])
	require.Equal(t, 1, store.uploadCount)
}

func TestDecodeImageDataURLRejectsOversizedContent(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testPNG)
	content, err := decodeImageDataURL("data:image/png;base64,"+encoded, int64(len(testPNG)))
	require.NoError(t, err)
	require.Equal(t, testPNG, content.data)

	_, err = decodeImageDataURL("data:image/png;base64,"+encoded, int64(len(testPNG)-1))
	require.ErrorContains(t, err, "exceeds")
}

func TestLegacyHTTPSourceBlocksPrivateAddresses(t *testing.T) {
	source := newLegacyHTTPSource(time.Second)
	_, err := source.download(context.Background(), "http://127.0.0.1/image.png", maxGeneratedImageBytes)
	require.ErrorContains(t, err, "unsafe legacy image URL")
}

func TestLegacyHTTPSourceDoesNotExposeSignedURLOnTransportError(t *testing.T) {
	source := newLegacyHTTPSource(time.Second)
	source.protection.ApplyIPFilterForDomain = false
	source.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request to %s failed", request.URL)
	})

	_, err := source.download(
		context.Background(),
		"https://example.com/image.png?token=do-not-report",
		maxGeneratedImageBytes,
	)
	require.EqualError(t, err, "download legacy image request failed")
	require.NotContains(t, err.Error(), "do-not-report")
}

func TestImagesToOBSPreservesLegacySource404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	db := newMigrationTestDB(t)
	require.NoError(t, db.Create(&model.MujianShot{
		ID: "shot-404", ProjectID: "project-1", SceneID: "scene-1", Sequence: 1,
		DurationSeconds: 3, ResultURL: server.URL + "/missing.png", Status: "completed",
	}).Error)
	source := newLegacyHTTPSource(time.Second)
	source.protection.AllowPrivateIp = true
	source.protection.AllowedPorts = nil
	source.client = server.Client()
	engine := migrationEngine{db: db, store: newFakeMigrationStore(db), httpSource: source}

	report, err := engine.imagesToOBS(context.Background(), true)
	require.NoError(t, err, "source_404 is a preserved non-blocking migration result")
	require.Equal(t, 1, report.Summary["source_404"])
	var shot model.MujianShot
	require.NoError(t, db.First(&shot, "id = ?", "shot-404").Error)
	require.Empty(t, shot.ResultObjectKey)
	require.Equal(t, server.URL+"/missing.png", shot.ResultURL)
}

func TestInventoryDoesNotExposeLegacyURL(t *testing.T) {
	db := newMigrationTestDB(t)
	secretURL := "https://example.com/image.png?secret=do-not-report"
	require.NoError(t, db.Create(&model.MujianShot{
		ID: "shot-secret", ProjectID: "project-1", SceneID: "scene-1", Sequence: 1,
		DurationSeconds: 3, ResultURL: secretURL, Status: "completed",
	}).Error)
	engine := migrationEngine{db: db, store: newFakeMigrationStore(db), httpSource: newLegacyHTTPSource(time.Second)}
	report, err := engine.inventory()
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	require.Equal(t, "http_url", report.Entries[0].Source)
	require.False(t, strings.Contains(fmt.Sprintf("%+v", report.Entries[0]), "do-not-report"))
}

func newMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.MujianImageGeneration{}, &model.MujianImageReference{},
		&model.MujianShot{}, &model.MujianObjectOperation{},
	))
	return db
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
