package mujian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianobject"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeImageObjectStore struct {
	mutex      sync.Mutex
	objects    map[string]mujianobject.Object
	data       map[string][]byte
	uploadErr  error
	deleteErr  error
	deleteKeys []string
}

func newFakeImageObjectStore() *fakeImageObjectStore {
	return &fakeImageObjectStore{
		objects: make(map[string]mujianobject.Object),
		data:    make(map[string][]byte),
	}
}

func (store *fakeImageObjectStore) BuildKey(kind mujianobject.ObjectKind, id, contentType string) (string, error) {
	return mujianobject.BuildObjectKey("mujian/prod/public", kind, id, contentType)
}

func (store *fakeImageObjectStore) Upload(_ context.Context, input mujianobject.UploadInput) (mujianobject.Object, error) {
	if store.uploadErr != nil {
		return mujianobject.Object{}, store.uploadErr
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return mujianobject.Object{}, err
	}
	if int64(len(data)) != input.SizeBytes {
		return mujianobject.Object{}, errors.New("fake upload size mismatch")
	}
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	object := mujianobject.Object{
		Key: input.ObjectKey, PublicURL: "https://static.mujianai.com/" + input.ObjectKey,
		ContentType: input.ContentType, SizeBytes: input.SizeBytes,
		SHA256: checksum, ETag: "etag-" + input.ObjectKey,
	}
	if err := model.BeginMujianObjectUpload(model.DB, &model.MujianObjectOperation{
		ObjectKey: input.ObjectKey, ContentType: input.ContentType,
		SizeBytes: input.SizeBytes, SHA256: checksum,
	}); err != nil {
		return mujianobject.Object{}, err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.objects[input.ObjectKey] = object
	store.data[input.ObjectKey] = append([]byte(nil), data...)
	return object, nil
}

func (store *fakeImageObjectStore) Get(_ context.Context, key string) (io.ReadCloser, mujianobject.Object, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	object, ok := store.objects[key]
	if !ok {
		return nil, mujianobject.Object{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), store.data[key]...))), object, nil
}

func (store *fakeImageObjectStore) Head(_ context.Context, key string) (mujianobject.Object, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	object, ok := store.objects[key]
	if !ok {
		return mujianobject.Object{}, ErrNotFound
	}
	return object, nil
}

func (store *fakeImageObjectStore) Delete(_ context.Context, key string) error {
	if err := model.EnqueueMujianObjectDeletes(model.DB, []string{key}); err != nil {
		return err
	}
	store.mutex.Lock()
	if store.deleteErr != nil {
		store.mutex.Unlock()
		return store.deleteErr
	}
	delete(store.objects, key)
	delete(store.data, key)
	store.deleteKeys = append(store.deleteKeys, key)
	store.mutex.Unlock()
	return model.DB.Where("object_key = ?", key).Delete(&model.MujianObjectOperation{}).Error
}

func (store *fakeImageObjectStore) PublicURL(key string) (string, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if _, ok := store.objects[key]; !ok {
		return "", ErrNotFound
	}
	return "https://static.mujianai.com/" + key, nil
}

func useFakeImageObjectStorage(t *testing.T) *fakeImageObjectStore {
	t.Helper()
	store := newFakeImageObjectStore()
	originalFromEnvironment := imageObjectStoreFromEnvironment
	originalOpen := openImageObjectStore
	imageObjectStoreFromEnvironment = func() (mujianobject.Store, bool, error) { return store, true, nil }
	openImageObjectStore = func() (mujianobject.Store, error) { return store, nil }
	t.Cleanup(func() {
		imageObjectStoreFromEnvironment = originalFromEnvironment
		openImageObjectStore = originalOpen
	})
	return store
}

func useFakeImageObjectReadsWithDatabaseWrites(t *testing.T) *fakeImageObjectStore {
	t.Helper()
	store := newFakeImageObjectStore()
	originalFromEnvironment := imageObjectStoreFromEnvironment
	originalOpen := openImageObjectStore
	imageObjectStoreFromEnvironment = func() (mujianobject.Store, bool, error) { return nil, false, nil }
	openImageObjectStore = func() (mujianobject.Store, error) { return store, nil }
	t.Cleanup(func() {
		imageObjectStoreFromEnvironment = originalFromEnvironment
		openImageObjectStore = originalOpen
	})
	return store
}

func testPNG(seed byte) []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', seed}
}

func testImageDataURL(seed byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG(seed))
}

func allowTestImageServer(t *testing.T, serverURL string) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	setting := system_setting.GetFetchSetting()
	original := *setting
	original.DomainList = append([]string(nil), setting.DomainList...)
	original.IpList = append([]string(nil), setting.IpList...)
	original.AllowedPorts = append([]string(nil), setting.AllowedPorts...)
	setting.EnableSSRFProtection = true
	setting.AllowPrivateIp = true
	setting.DomainFilterMode = false
	setting.IpFilterMode = false
	setting.DomainList = nil
	setting.IpList = nil
	setting.AllowedPorts = append(append([]string(nil), original.AllowedPorts...), port)
	setting.ApplyIPFilterForDomain = true
	t.Cleanup(func() { *setting = original })
}

func enableTestImageModel(t *testing.T, modelID, protocol string) {
	t.Helper()
	channel := model.Channel{Name: "image-" + modelID + protocol, Key: "provider-key", Status: 1, Models: modelID}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: modelID, UpstreamModelID: modelID, Provider: "test",
		BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.1, Currency: "USD", Available: true,
		ReferenceProtocol: protocol, MaxReferenceImages: MaxImageReferenceCount,
	}).Error)
}

func waitForImageGeneration(t *testing.T, userID int, projectID, sessionID, generationID, status string) *ImageGeneration {
	t.Helper()
	var generation *ImageGeneration
	require.Eventually(t, func() bool {
		var err error
		generation, err = GetImageGeneration(userID, projectID, sessionID, generationID)
		return err == nil && generation.Status == status
	}, 3*time.Second, 10*time.Millisecond)
	return generation
}

func TestProjectImageGenerationPersistsReferencesInSession(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-owner")
	project := createEmptyTestProject(t, user.Id)
	projectID := project.ID
	workspace, err := GetWorkspace(user.Id, projectID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	initialSession, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	initialUpdatedAt := initialSession.UpdatedAt
	enableTestImageModel(t, "nano-banana", mujianprovider.ReferenceProtocolGeminiInline)
	generatedImage := testPNG(8)
	imageServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(generatedImage)
	}))
	defer imageServer.Close()
	allowTestImageServer(t, imageServer.URL)
	providerResultURL := imageServer.URL + "/generated.png?provider_token=secret"

	originalDispatcher := dispatchProjectImage
	dispatchedInputs := make(chan projectImageDispatchInput, 1)
	dispatchProjectImage = func(input projectImageDispatchInput) (projectImageDispatchResult, error) {
		dispatchedInputs <- input
		return projectImageDispatchResult{ResultURL: providerResultURL, RelayRequestID: "request-project-image"}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	created, err := CreateImageGeneration(user.Id, projectID, CreateImageGenerationInput{
		SessionID: sessionID, Prompt: "保持人物一致的电影感画面", Engine: "nano", ModelID: "nano-banana", AspectRatio: "9:16",
		References: []ImageReferenceInput{{Name: "../first.png", Data: testPNG(1)}, {Name: "second.png", Data: testPNG(2)}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, sessionID, created.SessionID)
	var renamedSession model.MujianAgentSession
	require.NoError(t, model.DB.First(&renamedSession, "id = ?", sessionID).Error)
	require.Equal(t, "保持人物一致的电影感画面", renamedSession.Title)
	require.Greater(t, renamedSession.UpdatedAt, initialUpdatedAt)
	require.Len(t, created.References, 2)
	require.Equal(t, "first.png", created.References[0].Name)
	require.NotEmpty(t, created.References[0].ContentURL)

	succeeded := waitForImageGeneration(t, user.Id, projectID, sessionID, created.ID, "succeeded")
	dispatched := <-dispatchedInputs
	require.Equal(t, "nano", dispatched.Engine)
	require.Equal(t, "nano-banana", dispatched.ModelID)
	require.Len(t, dispatched.References, 2)
	require.Equal(t, 0, dispatched.References[0].Position)
	require.Equal(t, 1, dispatched.References[1].Position)
	require.Equal(t, imageGenerationContentURL(projectID, succeeded.ID, sessionID), succeeded.ResultURL)
	require.NotContains(t, succeeded.ResultURL, providerResultURL)
	require.Equal(t, "request-project-image", succeeded.RelayRequestID)
	var storedGeneration model.MujianImageGeneration
	require.NoError(t, model.DB.First(&storedGeneration, "id = ?", succeeded.ID).Error)
	require.Empty(t, storedGeneration.ResultURL)
	require.Equal(t, "image/png", storedGeneration.ResultMIMEType)
	require.Equal(t, generatedImage, storedGeneration.ResultData)
	content, err := GetImageGenerationContent(user.Id, projectID, sessionID, succeeded.ID)
	require.NoError(t, err)
	require.Equal(t, generatedImage, content.Data)
	reference, err := GetImageReferenceContent(user.Id, projectID, sessionID, created.ID, succeeded.References[0].ID)
	require.NoError(t, err)
	require.Equal(t, testPNG(1), reference.Data)
	require.NoError(t, model.DB.Where("task_id = ?", succeeded.TaskID).Delete(&model.Task{}).Error)
	durable, err := GetImageGeneration(user.Id, projectID, sessionID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", durable.Status)

	workspace, err = GetWorkspace(user.Id, projectID, sessionID)
	require.NoError(t, err)
	require.Len(t, workspace.ImageGenerations, 1)
	workspaceJSON, err := common.Marshal(workspace)
	require.NoError(t, err)
	require.NotContains(t, string(workspaceJSON), `"scenes"`)
	require.NotContains(t, string(workspaceJSON), `"shots"`)
	require.NotContains(t, string(workspaceJSON), providerResultURL)

	other := createTestUser(t, "image-other-owner")
	_, err = GetImageReferenceContent(other.Id, projectID, sessionID, created.ID, succeeded.References[0].ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestObjectStorageImageGenerationUsesPublicURLsAndOBSOnlyRows(t *testing.T) {
	setupTestDB(t)
	store := useFakeImageObjectStorage(t)
	user := createTestUser(t, "image-object-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{ResultURL: testImageDataURL(31), RelayRequestID: "object-request"}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	created, err := CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID,
		Prompt:    "OBS 生图",
		Engine:    "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "character.png", Data: testPNG(30)}},
	})
	require.NoError(t, err)
	require.Len(t, created.References, 1)
	require.Equal(t, "https://static.mujianai.com/mujian/prod/public/references/"+created.References[0].ID+".png", created.References[0].ContentURL)

	succeeded := waitForImageGeneration(t, user.Id, project.ID, workspace.ActiveSessionID, created.ID, imageGenerationStatusSucceeded)
	require.Equal(t, "https://static.mujianai.com/mujian/prod/public/generations/"+created.ID+"/result.png", succeeded.ResultURL)

	var storedGeneration model.MujianImageGeneration
	require.NoError(t, model.DB.First(&storedGeneration, "id = ?", created.ID).Error)
	require.Empty(t, storedGeneration.ResultData)
	require.Empty(t, storedGeneration.ResultURL)
	require.NotEmpty(t, storedGeneration.ResultObjectKey)
	require.Equal(t, int64(len(testPNG(31))), storedGeneration.ResultSizeBytes)
	require.NotEmpty(t, storedGeneration.ResultSHA256)
	require.NotEmpty(t, storedGeneration.ResultETag)

	var storedReference model.MujianImageReference
	require.NoError(t, model.DB.First(&storedReference, "generation_id = ?", created.ID).Error)
	require.Empty(t, storedReference.Data)
	require.NotEmpty(t, storedReference.ObjectKey)
	require.Equal(t, int64(len(testPNG(30))), storedReference.ObjectSizeBytes)
	var activeObjects int64
	require.NoError(t, model.DB.Model(&model.MujianObjectOperation{}).
		Where("status = ?", model.MujianObjectStatusActive).Count(&activeObjects).Error)
	require.Equal(t, int64(2), activeObjects)

	resultContent, err := GetImageGenerationContent(user.Id, project.ID, workspace.ActiveSessionID, created.ID)
	require.NoError(t, err)
	require.Equal(t, testPNG(31), resultContent.Data)
	referenceContent, err := GetImageReferenceContent(user.Id, project.ID, workspace.ActiveSessionID, created.ID, storedReference.ID)
	require.NoError(t, err)
	require.Equal(t, testPNG(30), referenceContent.Data)

	regenerated, err := RegenerateImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, created.ID)
	require.NoError(t, err)
	require.NotEqual(t, created.ID, regenerated.ID)
	require.Len(t, regenerated.References, 1)
	require.NotEqual(t, created.References[0].ContentURL, regenerated.References[0].ContentURL)
	waitForImageGeneration(t, user.Id, project.ID, workspace.ActiveSessionID, regenerated.ID, imageGenerationStatusSucceeded)

	store.mutex.Lock()
	require.Len(t, store.objects, 4)
	store.mutex.Unlock()
	require.NoError(t, model.DB.Model(&model.MujianObjectOperation{}).
		Where("status = ?", model.MujianObjectStatusActive).Count(&activeObjects).Error)
	require.Equal(t, int64(4), activeObjects)
}

func TestObjectStorageReferenceUploadFailureDoesNotPersistGeneration(t *testing.T) {
	setupTestDB(t)
	store := useFakeImageObjectStorage(t)
	store.uploadErr = errors.New("OBS unavailable")
	user := createTestUser(t, "image-object-upload-failure")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

	_, err = CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID,
		Prompt:    "OBS 失败",
		Engine:    "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "character.png", Data: testPNG(32)}},
	})
	require.EqualError(t, err, "OBS unavailable")

	var generations, references, tasks int64
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageReference{}).Count(&references).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
	require.Zero(t, generations)
	require.Zero(t, references)
	require.Zero(t, tasks)
}

func TestObjectStoragePersistenceFailureCompensatesUploadedReferences(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		deleteErr       error
		expectedObjects int
		expectedError   string
	}{
		{name: "cleanup succeeds", expectedObjects: 0, expectedError: "database commit failed"},
		{name: "cleanup failure is returned", deleteErr: errors.New("cleanup unavailable"), expectedObjects: 1, expectedError: "cleanup unavailable"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setupTestDB(t)
			store := useFakeImageObjectStorage(t)
			store.deleteErr = testCase.deleteErr
			user := createTestUser(t, "image-object-transaction-"+strings.ReplaceAll(testCase.name, " ", "-"))
			project := createEmptyTestProject(t, user.Id)
			workspace, err := GetWorkspace(user.Id, project.ID)
			require.NoError(t, err)
			enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

			originalPersist := persistProjectImageGeneration
			persistProjectImageGeneration = func(*model.MujianImageGeneration, []model.MujianImageReference, *model.Task) error {
				return errors.New("database commit failed")
			}
			t.Cleanup(func() { persistProjectImageGeneration = originalPersist })

			_, err = CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
				SessionID: workspace.ActiveSessionID,
				Prompt:    "事务补偿",
				Engine:    "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
				References: []ImageReferenceInput{{Name: "character.png", Data: testPNG(33)}},
			})
			require.ErrorContains(t, err, "database commit failed")
			require.ErrorContains(t, err, testCase.expectedError)

			store.mutex.Lock()
			require.Len(t, store.objects, testCase.expectedObjects)
			store.mutex.Unlock()
			var generationCount int64
			require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Count(&generationCount).Error)
			require.Zero(t, generationCount)
		})
	}
}

func TestDatabaseWriteModeStillReadsAndRegeneratesOBSObjects(t *testing.T) {
	setupTestDB(t)
	store := useFakeImageObjectReadsWithDatabaseWrites(t)
	user := createTestUser(t, "image-object-rollback-read")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

	generationID := uuid.NewString()
	referenceID := uuid.NewString()
	upload := func(kind mujianobject.ObjectKind, id string, data []byte) mujianobject.Object {
		key, buildErr := store.BuildKey(kind, id, "image/png")
		require.NoError(t, buildErr)
		object, uploadErr := store.Upload(context.Background(), mujianobject.UploadInput{
			ObjectKey: key, ContentType: "image/png", Body: bytes.NewReader(data), SizeBytes: int64(len(data)),
		})
		require.NoError(t, uploadErr)
		require.NoError(t, model.ActivateMujianObject(model.DB, object.Key, object.ETag))
		return object
	}
	resultObject := upload(mujianobject.ObjectKindGeneration, generationID, testPNG(40))
	referenceObject := upload(mujianobject.ObjectKindReference, referenceID, testPNG(41))
	generation := model.MujianImageGeneration{
		ID: generationID, ProjectID: project.ID, UserID: user.Id, SessionID: workspace.ActiveSessionID,
		Prompt: "rollback read", Engine: "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
		Status: imageGenerationStatusSucceeded, TaskID: model.GenerateTaskID(), ResultMIMEType: "image/png",
		ResultObjectKey: resultObject.Key, ResultSizeBytes: resultObject.SizeBytes,
		ResultSHA256: resultObject.SHA256, ResultETag: resultObject.ETag,
	}
	require.NoError(t, model.DB.Create(&generation).Error)
	require.NoError(t, model.DB.Create(&model.MujianImageReference{
		ID: referenceID, GenerationID: generation.ID, Position: 0, Name: "legacy-obs.png",
		MIMEType: "image/png", SizeBytes: referenceObject.SizeBytes, Data: []byte{},
		ObjectKey: referenceObject.Key, ObjectSizeBytes: referenceObject.SizeBytes,
		ObjectSHA256: referenceObject.SHA256, ObjectETag: referenceObject.ETag,
	}).Error)

	dto, err := GetImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, resultObject.PublicURL, dto.ResultURL)
	require.Equal(t, referenceObject.PublicURL, dto.References[0].ContentURL)
	content, err := GetImageGenerationContent(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, testPNG(40), content.Data)

	originalDispatcher := dispatchProjectImage
	dispatchedReference := make(chan []byte, 1)
	dispatchProjectImage = func(input projectImageDispatchInput) (projectImageDispatchResult, error) {
		dispatchedReference <- append([]byte(nil), input.References[0].Data...)
		return projectImageDispatchResult{ResultURL: testImageDataURL(42)}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })
	regenerated, err := RegenerateImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	waitForImageGeneration(t, user.Id, project.ID, workspace.ActiveSessionID, regenerated.ID, imageGenerationStatusSucceeded)
	require.Equal(t, testPNG(41), <-dispatchedReference)

	var regeneratedModel model.MujianImageGeneration
	require.NoError(t, model.DB.First(&regeneratedModel, "id = ?", regenerated.ID).Error)
	require.Empty(t, regeneratedModel.ResultObjectKey)
	require.Equal(t, testPNG(42), regeneratedModel.ResultData)
	var regeneratedReference model.MujianImageReference
	require.NoError(t, model.DB.First(&regeneratedReference, "generation_id = ?", regenerated.ID).Error)
	require.Empty(t, regeneratedReference.ObjectKey)
	require.Equal(t, testPNG(41), regeneratedReference.Data)
}

func TestRegenerateImageGenerationClonesImmutableInputs(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-regenerate")
	project := createEmptyTestProject(t, user.Id)
	projectID := project.ID
	workspace, err := GetWorkspace(user.Id, projectID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(input projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{ResultURL: testImageDataURL(byte(len(input.ModelID)))}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	created, err := CreateImageGeneration(user.Id, projectID, CreateImageGenerationInput{
		SessionID: sessionID, Prompt: "重绘画面", Engine: "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "character.png", Data: testPNG(3)}},
	})
	require.NoError(t, err)
	waitForImageGeneration(t, user.Id, projectID, sessionID, created.ID, "succeeded")

	regenerated, err := RegenerateImageGeneration(user.Id, projectID, sessionID, created.ID)
	require.NoError(t, err)
	require.NotEqual(t, created.ID, regenerated.ID)
	require.NotEqual(t, created.TaskID, regenerated.TaskID)
	require.Equal(t, created.Prompt, regenerated.Prompt)
	require.Len(t, regenerated.References, 1)
	require.Equal(t, sessionID, regenerated.SessionID)
	waitForImageGeneration(t, user.Id, projectID, sessionID, regenerated.ID, "succeeded")

	var generationCount, referenceCount int64
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("project_id = ?", projectID).Count(&generationCount).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageReference{}).Count(&referenceCount).Error)
	require.Equal(t, int64(2), generationCount)
	require.Equal(t, int64(2), referenceCount)
}

func TestImageGenerationsAreIsolatedBySessionAndOwnership(t *testing.T) {
	setupTestDB(t)
	owner := createTestUser(t, "image-session-owner")
	other := createTestUser(t, "image-session-other")
	project := createEmptyTestProject(t, owner.Id)
	firstWorkspace, err := GetWorkspace(owner.Id, project.ID)
	require.NoError(t, err)
	secondWorkspace, err := CreateAgentSession(owner.Id, project.ID)
	require.NoError(t, err)
	firstSessionID := firstWorkspace.ActiveSessionID
	secondSessionID := secondWorkspace.ActiveSessionID
	enableTestImageModel(t, "nano-banana", mujianprovider.ReferenceProtocolGeminiInline)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{ResultURL: testImageDataURL(6)}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })
	created, err := CreateImageGeneration(owner.Id, project.ID, CreateImageGenerationInput{
		SessionID: firstSessionID, Prompt: "仅属于第一会话", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, err)
	waitForImageGeneration(t, owner.Id, project.ID, firstSessionID, created.ID, imageGenerationStatusSucceeded)

	firstWorkspace, err = GetWorkspace(owner.Id, project.ID, firstSessionID)
	require.NoError(t, err)
	require.Len(t, firstWorkspace.ImageGenerations, 1)
	secondWorkspace, err = GetWorkspace(owner.Id, project.ID, secondSessionID)
	require.NoError(t, err)
	require.Empty(t, secondWorkspace.ImageGenerations)

	_, err = GetImageGeneration(owner.Id, project.ID, secondSessionID, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = RegenerateImageGeneration(owner.Id, project.ID, secondSessionID, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = GetImageGenerationContent(owner.Id, project.ID, secondSessionID, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = CreateImageGeneration(other.Id, project.ID, CreateImageGenerationInput{
		SessionID: firstSessionID, Prompt: "越权生图", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestProjectImageGenerationRejectsUnsupportedReferencesBeforeQueue(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-no-reference-channel")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "nano-banana", "")

	_, err = CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "多图生成", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "reference.png", Data: testPNG(4)}},
	})
	require.EqualError(t, err, "所选模型暂无支持多图参考的可用渠道")
	var taskCount, generationCount int64
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&taskCount).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Count(&generationCount).Error)
	require.Zero(t, taskCount)
	require.Zero(t, generationCount)
}

func TestProjectImageGenerationFailureUpdatesTaskAndGeneration(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-failure")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{RelayRequestID: "failed-request"}, errors.New("provider unavailable")
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	created, err := CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "失败测试", Engine: "gpt", ModelID: "gpt-image-2", AspectRatio: "16:9",
	})
	require.NoError(t, err)
	failed := waitForImageGeneration(t, user.Id, project.ID, workspace.ActiveSessionID, created.ID, "failed")
	require.Equal(t, "provider unavailable", failed.Error)
	require.Equal(t, "failed-request", failed.RelayRequestID)
	task, exists, err := model.GetByTaskId(user.Id, failed.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
}

func TestFinishProjectImageFailureRejectsPartialTransition(t *testing.T) {
	setupTestDB(t)
	task := model.Task{
		TaskID: model.GenerateTaskID(), Platform: constant.TaskPlatformMujianImage,
		UserId: 7, Action: imageGenerationAction, Status: model.TaskStatusInProgress,
	}
	require.NoError(t, model.DB.Create(&task).Error)

	err := finishProjectImageFailure(&task, uuid.NewString(), "provider failed", "request-id")
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusInProgress), task.Status, "task update must roll back when generation update cannot commit")
}

func TestUnsafeRemoteImageResultFailsGeneration(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-unsafe-result")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableTestImageModel(t, "nano-banana", mujianprovider.ReferenceProtocolGeminiInline)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(writer, "provider returned html")
	}))
	defer server.Close()
	allowTestImageServer(t, server.URL)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{ResultURL: server.URL + "/not-image", RelayRequestID: "unsafe-result-request"}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })
	created, err := CreateImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "unsafe result", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, err)
	failed := waitForImageGeneration(t, user.Id, project.ID, workspace.ActiveSessionID, created.ID, imageGenerationStatusFailed)
	require.Equal(t, "生成结果不是支持的图片格式", failed.Error)
	require.Empty(t, failed.ResultURL)
	task, exists, err := model.GetByTaskId(user.Id, failed.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
}

func TestQueuedImageGenerationResumesAndWorkspaceHidesDataURL(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-resume")
	project := createEmptyTestProject(t, user.Id)
	projectID := project.ID
	workspace, err := GetWorkspace(user.Id, projectID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	enableTestImageModel(t, "gpt-image-2", mujianprovider.ReferenceProtocolOpenAIEditMultipart)
	generatedData := testPNG(9)
	rawDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(generatedData)

	originalDispatcher := dispatchProjectImage
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		return projectImageDispatchResult{ResultURL: rawDataURL}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	input, err := validateImageGenerationInput(CreateImageGenerationInput{
		SessionID: sessionID, Prompt: "resume after restart", Engine: "gpt", ModelID: "gpt-image-2", AspectRatio: "1:1",
	})
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, projectID, input)
	require.NoError(t, persistImageGeneration(generation, references, task), "persist without starting simulates a restart before worker launch")

	queued, err := GetImageGeneration(user.Id, projectID, sessionID, generation.ID)
	require.NoError(t, err)
	require.Contains(t, []string{"queued", "running", "succeeded"}, queued.Status)
	succeeded := waitForImageGeneration(t, user.Id, projectID, sessionID, generation.ID, "succeeded")
	require.NotContains(t, succeeded.ResultURL, "base64")
	require.Equal(t, "/api/mujian/projects/"+projectID+"/image-generations/"+generation.ID+"/content?session_id="+sessionID, succeeded.ResultURL)
	content, err := GetImageGenerationContent(user.Id, projectID, sessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, "image/png", content.MIMEType)
	require.Equal(t, generatedData, content.Data)
	other := createTestUser(t, "image-content-other")
	_, err = GetImageGenerationContent(other.Id, projectID, sessionID, generation.ID)
	require.ErrorIs(t, err, ErrNotFound)

	workspace, err = GetWorkspace(user.Id, projectID, sessionID)
	require.NoError(t, err)
	workspaceJSON, err := common.Marshal(workspace)
	require.NoError(t, err)
	require.NotContains(t, string(workspaceJSON), rawDataURL)
	var stored model.MujianImageGeneration
	require.NoError(t, model.DB.First(&stored, "id = ?", generation.ID).Error)
	require.Empty(t, stored.ResultURL)
	require.Equal(t, "image/png", stored.ResultMIMEType)
	require.Equal(t, generatedData, stored.ResultData)

	var storedTask model.Task
	require.NoError(t, model.DB.First(&storedTask, "task_id = ?", generation.TaskID).Error)
	require.Empty(t, storedTask.PrivateData.ResultURL)
	require.NotContains(t, string(storedTask.Data), rawDataURL)

}

func TestStaleInProgressImageGenerationFailsAfterWorkerRestart(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-stale-worker")
	project := createEmptyTestProject(t, user.Id)
	projectID := project.ID
	workspace, err := GetWorkspace(user.Id, projectID)
	require.NoError(t, err)

	generation := model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: projectID, UserID: user.Id, SessionID: workspace.ActiveSessionID, Prompt: "stale", Engine: "nano",
		ModelID: "nano-banana", AspectRatio: "1:1", Status: imageGenerationStatusRunning, TaskID: model.GenerateTaskID(),
	}
	task := model.Task{
		TaskID: generation.TaskID, Platform: constant.TaskPlatformMujianImage, UserId: user.Id, Action: imageGenerationAction,
		Status: model.TaskStatusInProgress, Progress: "10%", SubmitTime: time.Now().Add(-time.Hour).Unix(),
		StartTime: time.Now().Add(-imageGenerationStaleAfter - time.Second).Unix(),
	}
	require.NoError(t, model.DB.Create(&generation).Error)
	require.NoError(t, model.DB.Create(&task).Error)

	result, err := GetImageGeneration(user.Id, projectID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, imageGenerationStatusFailed, result.Status)
	require.Contains(t, result.Error, "服务重启后中断")
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
}

func TestInProgressImageGenerationRemainsActiveWithinExecutionDeadline(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-active-long-running")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)

	generation := model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: workspace.ActiveSessionID,
		Prompt: "slow but valid", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: imageGenerationStatusRunning, TaskID: model.GenerateTaskID(),
	}
	task := model.Task{
		TaskID: generation.TaskID, Platform: constant.TaskPlatformMujianImage, UserId: user.Id,
		Action: imageGenerationAction, Status: model.TaskStatusInProgress, Progress: "10%",
		SubmitTime: time.Now().Add(-7 * time.Minute).Unix(), StartTime: time.Now().Add(-7 * time.Minute).Unix(),
	}
	require.Less(t, 7*time.Minute, model.MujianImageGenerationExecutionTimeout)
	require.NoError(t, model.DB.Create(&generation).Error)
	require.NoError(t, model.DB.Create(&task).Error)

	result, err := GetImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, imageGenerationStatusRunning, result.Status)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusInProgress), task.Status)
}

func TestImageReferenceValidationLimitsAndFormats(t *testing.T) {
	references := make([]ImageReferenceInput, MaxImageReferenceCount+1)
	for index := range references {
		references[index] = ImageReferenceInput{Name: fmt.Sprintf("%d.png", index), Data: testPNG(byte(index))}
	}
	require.EqualError(t, validateImageReferences(references), "参考图最多 3 张")
	require.EqualError(t, validateImageReferences([]ImageReferenceInput{{Name: "fake.png", Data: []byte("not an image")}}), "参考图仅支持 PNG、JPEG 或 WebP")
	require.EqualError(t, validateImageReferences([]ImageReferenceInput{{Name: "large.png", Data: make([]byte, MaxImageReferenceBytes+1)}}), "单张参考图不能超过 10MB")
	require.Equal(t, "unsafe.png", safeReferenceName("../unsafe\n.png", "image/png", 0))
}

func TestImageGenerationRejectsOversizedPrompt(t *testing.T) {
	_, err := validateImageGenerationInput(CreateImageGenerationInput{
		SessionID: "session", Prompt: strings.Repeat("画", MaxImagePromptRunes+1), Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.EqualError(t, err, "图片提示词不能超过 8000 个字符")
}

func TestGeneratedImageContentValidatesMIMEEncodingAndSize(t *testing.T) {
	_, err := decodeBase64ImageDataURL("data:text/plain;base64,dGV4dA==", 100)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = decodeBase64ImageDataURL("data:image/png;base64,not-base64", 100)
	require.EqualError(t, err, "生成图片数据无效")
	encoded := base64.StdEncoding.EncodeToString([]byte("12345"))
	_, err = decodeBase64ImageDataURL("data:image/png;base64,"+encoded, 4)
	require.EqualError(t, err, "生成图片超过可读取大小限制")
}

func TestRemoteImageResultIsDownloadedWithSafetyLimits(t *testing.T) {
	t.Run("supported image", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(testPNG(21))
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)

		controlled, err := materializeImageGenerationResult(server.URL + "/image?token=private")
		require.NoError(t, err)
		require.Equal(t, "image/png", controlled.MIMEType)
		require.Equal(t, testPNG(21), controlled.Data)
	})

	t.Run("non image", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(writer, "not an image")
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)

		_, err := materializeImageGenerationResult(server.URL)
		require.EqualError(t, err, "生成结果不是支持的图片格式")
	})

	t.Run("oversized image", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("Content-Length", strconv.Itoa(MaxGeneratedImageBytes+1))
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)

		_, err := materializeImageGenerationResult(server.URL)
		require.EqualError(t, err, "生成图片超过可读取大小限制")
	})

	t.Run("oversized chunked image", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(testPNG(24))
			chunk := make([]byte, 1<<20)
			for index := 0; index < 25; index++ {
				if _, err := writer.Write(chunk); err != nil {
					return
				}
			}
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)

		_, err := materializeImageGenerationResult(server.URL)
		require.EqualError(t, err, "生成图片超过可读取大小限制")
	})

	t.Run("private address rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(testPNG(22))
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)
		system_setting.GetFetchSetting().AllowPrivateIp = false

		_, err := materializeImageGenerationResult(server.URL)
		require.EqualError(t, err, "生成图片地址不安全")
	})

	t.Run("redirect is revalidated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		}))
		defer server.Close()
		allowTestImageServer(t, server.URL)
		setting := system_setting.GetFetchSetting()
		setting.IpFilterMode = true
		setting.IpList = []string{"127.0.0.1/32", "::1/128"}

		_, err := materializeImageGenerationResult(server.URL)
		require.EqualError(t, err, "下载生成图片失败")
	})

	_, err := materializeImageGenerationResult("file:///tmp/provider-result.png")
	require.EqualError(t, err, "生成图片地址无效")
}

func TestImageGenerationDialerChecksAndPinsResolvedIP(t *testing.T) {
	t.Run("mixed safe and private answers are rejected before dialing", func(t *testing.T) {
		dialed := false
		dialer := imageGenerationDialer{
			ssrfEnabled: true,
			protection:  &common.SSRFProtection{AllowPrivateIp: false, IpFilterMode: false},
			lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("169.254.169.254")}}, nil
			},
			dialContext: func(context.Context, string, string) (net.Conn, error) {
				dialed = true
				return nil, errors.New("unexpected dial")
			},
		}
		_, err := dialer.DialContext(context.Background(), "tcp", "provider.example:443")
		require.EqualError(t, err, "生成图片地址不安全")
		require.False(t, dialed)
	})

	t.Run("approved answer is dialed by literal IP", func(t *testing.T) {
		clientConnection, serverConnection := net.Pipe()
		t.Cleanup(func() {
			_ = clientConnection.Close()
			_ = serverConnection.Close()
		})
		dialedAddress := ""
		dialer := imageGenerationDialer{
			ssrfEnabled: true,
			protection:  &common.SSRFProtection{AllowPrivateIp: false, IpFilterMode: false},
			lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
			},
			dialContext: func(_ context.Context, _, address string) (net.Conn, error) {
				dialedAddress = address
				return clientConnection, nil
			},
		}
		connection, err := dialer.DialContext(context.Background(), "tcp", "provider.example:443")
		require.NoError(t, err)
		require.Equal(t, clientConnection, connection)
		require.Equal(t, "8.8.8.8:443", dialedAddress)
	})
}

func TestLegacyRemoteImageDTOUsesAuthenticatedContentEndpoint(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "legacy-remote-image-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	image := testPNG(23)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(image)
	}))
	defer server.Close()
	allowTestImageServer(t, server.URL)
	providerURL := server.URL + "/legacy.png?provider_token=secret"
	generation := model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: sessionID,
		Prompt: "legacy", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: imageGenerationStatusSucceeded, TaskID: model.GenerateTaskID(), ResultURL: providerURL,
	}
	require.NoError(t, model.DB.Create(&generation).Error)
	serializedModel, err := common.Marshal(generation)
	require.NoError(t, err)
	require.NotContains(t, string(serializedModel), providerURL)

	dto, err := getImageGeneration(user.Id, project.ID, sessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, imageGenerationContentURL(project.ID, generation.ID, sessionID), dto.ResultURL)
	serialized, err := common.Marshal(dto)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), providerURL)
	content, err := GetImageGenerationContent(user.Id, project.ID, sessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, image, content.Data)
	_, err = GetImageGenerationContent(user.Id, project.ID, "wrong-session", generation.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestProjectImageTaskMissingGenerationFailsDeterministically(t *testing.T) {
	setupTestDB(t)
	task := model.Task{
		TaskID: model.GenerateTaskID(), Platform: "mujian_image", UserId: 7, Action: imageGenerationAction,
		Status: model.TaskStatusSubmitted, Progress: "0%", SubmitTime: time.Now().Unix(),
	}
	require.NoError(t, model.DB.Create(&task).Error)

	runProjectImageTask(task.UserId, "missing-project", "missing-generation", task.TaskID)

	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
	require.Equal(t, "生图记录不存在或已删除", task.FailReason)
}

func TestRelayImageEditPreservesReferenceOrder(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "image-relay")
	require.NoError(t, EnsureOnboarded(user.Id))
	type capturedRequest struct {
		model  string
		prompt string
		names  []string
		data   [][]byte
		err    error
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		value := capturedRequest{}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			value.err = err
			captured <- value
			http.Error(writer, "invalid multipart", http.StatusBadRequest)
			return
		}
		value.model = request.FormValue("model")
		value.prompt = request.FormValue("prompt")
		for _, header := range request.MultipartForm.File["image[]"] {
			file, err := header.Open()
			if err != nil {
				value.err = err
				break
			}
			data, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil {
				value.err = readErr
				break
			}
			if closeErr != nil {
				value.err = closeErr
				break
			}
			value.names = append(value.names, header.Filename)
			value.data = append(value.data, data)
		}
		captured <- value
		writer.Header().Set(common.RequestIdKey, "relay-request")
		_, _ = io.WriteString(writer, `{"data":[{"url":"https://example.com/result.png"}]}`)
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	body, requestID, err := relayImageEditRequest(projectImageDispatchInput{
		Context: context.Background(), UserID: user.Id, ModelID: "gpt-image-2", Prompt: "ordered references", AspectRatio: "1:1",
		References: []model.MujianImageReference{
			{Name: "first.png", MIMEType: "image/png", Data: testPNG(1)},
			{Name: "second.png", MIMEType: "image/png", Data: testPNG(2)},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "relay-request", requestID)
	require.Contains(t, string(body), "https://example.com/result.png")
	request := <-captured
	require.NoError(t, request.err)
	require.Equal(t, "gpt-image-2", request.model)
	require.Equal(t, "ordered references", request.prompt)
	require.Equal(t, []string{"first.png", "second.png"}, request.names)
	require.Equal(t, [][]byte{testPNG(1), testPNG(2)}, request.data)
}
