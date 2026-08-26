package openapi

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

var openAPIMethods = map[string]struct{}{
	"get": {}, "post": {}, "put": {}, "patch": {}, "delete": {}, "head": {}, "options": {}, "trace": {},
}

func decodeRelayDocument(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	require.NoError(t, common.Unmarshal(RelaySpec(), &document))
	return document
}

func TestRelaySpecIsStandardOpenAPIWithUniqueOperations(t *testing.T) {
	document := decodeRelayDocument(t)
	require.Equal(t, "3.0.3", document["openapi"])

	components := document["components"].(map[string]any)
	securitySchemes := components["securitySchemes"].(map[string]any)
	require.Len(t, securitySchemes, 1)
	bearer := securitySchemes["BearerAuth"].(map[string]any)
	require.Equal(t, "http", bearer["type"])
	require.Equal(t, "bearer", bearer["scheme"])

	rootSecurity := document["security"].([]any)
	require.NotEmpty(t, rootSecurity)
	_, hasBearer := rootSecurity[0].(map[string]any)["BearerAuth"]
	require.True(t, hasBearer)

	paths := document["paths"].(map[string]any)
	operationIDs := make(map[string]string)
	operationCount := 0
	for path, rawPathItem := range paths {
		pathItem := rawPathItem.(map[string]any)
		for method, rawOperation := range pathItem {
			if _, ok := openAPIMethods[method]; !ok {
				continue
			}
			operationCount++
			operation := rawOperation.(map[string]any)
			operationID, ok := operation["operationId"].(string)
			require.True(t, ok && operationID != "", "%s %s must define operationId", method, path)
			if previous, duplicate := operationIDs[operationID]; duplicate {
				t.Fatalf("duplicate operationId %q on %s %s and %s", operationID, method, path, previous)
			}
			operationIDs[operationID] = method + " " + path
		}
	}
	require.Equal(t, 71, operationCount)
}

func TestRelaySpecLocalReferencesResolve(t *testing.T) {
	document := decodeRelayDocument(t)
	walkOpenAPIValue(t, document, document)
}

func walkOpenAPIValue(t *testing.T, document map[string]any, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			require.True(t, strings.HasPrefix(ref, "#/"), "only local references are allowed: %s", ref)
			_, found := resolveJSONPointer(document, ref)
			require.True(t, found, "unresolved OpenAPI reference: %s", ref)
		}
		for _, child := range typed {
			walkOpenAPIValue(t, document, child)
		}
	case []any:
		for _, child := range typed {
			walkOpenAPIValue(t, document, child)
		}
	}
}

func resolveJSONPointer(document map[string]any, ref string) (any, bool) {
	var current any = document
	for _, encodedPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encodedPart, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func TestRelaySpecDocumentsSupportedSurfaceOnly(t *testing.T) {
	document := decodeRelayDocument(t)
	paths := document["paths"].(map[string]any)

	for _, requiredPath := range []string{
		"/v1/models/{model}",
		"/v1/responses",
		"/v1/images/edits",
		"/v1beta/models/{model}:generateContent",
		"/v1beta/models/{model}:batchEmbedContents",
		"/v1/videos/{video_id}/remix",
		"/jimeng/",
		"/mj/submit/imagine",
		"/{mode}/mj/submit/imagine",
		"/suno/submit/{action}",
	} {
		require.Contains(t, paths, requiredPath)
	}

	for _, excludedPath := range []string{
		"/pg/chat/completions",
		"/v1/engines/{model}/embeddings",
		"/v1/images/variations",
		"/v1/files",
		"/v1/fine-tunes",
		"/mj/submit/edits",
		"/{mode}/mj/submit/edits",
	} {
		require.NotContains(t, paths, excludedPath)
	}

	modelOperations := paths["/v1/models/{model}"].(map[string]any)
	require.NotContains(t, modelOperations, "delete")
}

func TestRelaySpecCapturesCorrectedRequestContracts(t *testing.T) {
	document := decodeRelayDocument(t)
	paths := document["paths"].(map[string]any)

	legacyEdit := paths["/v1/edits"].(map[string]any)["post"].(map[string]any)
	require.Equal(t, []any{"文本"}, legacyEdit["tags"])
	require.Equal(t, "#/components/requestBodies/LegacyEdit", legacyEdit["requestBody"].(map[string]any)["$ref"])

	realtime := paths["/v1/realtime"].(map[string]any)["get"].(map[string]any)
	realtimeParameters := realtime["parameters"].([]any)
	require.Len(t, realtimeParameters, 1)
	require.Equal(t, "model", realtimeParameters[0].(map[string]any)["name"])
	require.Equal(t, true, realtimeParameters[0].(map[string]any)["required"])

	suno := paths["/suno/submit/{action}"].(map[string]any)["post"].(map[string]any)
	sunoAction := suno["parameters"].([]any)[0].(map[string]any)
	require.Equal(t, []any{"music", "lyrics"}, sunoAction["schema"].(map[string]any)["enum"])

	gemini := paths["/v1beta/models/{model}:generateContent"].(map[string]any)["post"].(map[string]any)
	require.Equal(t, "#/components/parameters/GeminiModelPath", gemini["parameters"].([]any)[0].(map[string]any)["$ref"])

	components := document["components"].(map[string]any)
	parameters := components["parameters"].(map[string]any)
	require.Equal(t, "gemini-2.0-flash", parameters["GeminiModelPath"].(map[string]any)["example"])
	require.Equal(t, "mj-fast", parameters["ModePath"].(map[string]any)["example"])
	require.Equal(t, []any{"sse"}, parameters["GeminiAltQuery"].(map[string]any)["schema"].(map[string]any)["enum"])

	schemas := components["schemas"].(map[string]any)
	requestBodies := components["requestBodies"].(map[string]any)
	imageEditContent := requestBodies["ImageEdit"].(map[string]any)["content"].(map[string]any)
	require.Equal(t, "#/components/schemas/ImageEditJSONRequest", imageEditContent["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"])
	kling := schemas["KlingTextVideoRequest"].(map[string]any)
	require.Equal(t, []any{map[string]any{"required": []any{"model_name"}}, map[string]any{"required": []any{"model"}}}, kling["anyOf"])
	require.Equal(t, []any{"req_key", "prompt"}, schemas["JimengSubmitRequest"].(map[string]any)["required"])
	require.Equal(t, []any{"task_id"}, schemas["JimengFetchRequest"].(map[string]any)["required"])
	chatMessage := schemas["ChatMessage"].(map[string]any)
	require.Equal(t, []any{"role"}, chatMessage["required"])
	require.Equal(t, []any{
		map[string]any{"required": []any{"content"}},
		map[string]any{"required": []any{"tool_calls"}},
	}, chatMessage["anyOf"])
	actionSchema := schemas["MidjourneyActionRequest"].(map[string]any)["allOf"].([]any)[1].(map[string]any)
	require.Equal(t, []any{"taskId", "customId"}, actionSchema["required"])
	require.Equal(t, []any{"prompt"}, midjourneyConstraint(schemas, "MidjourneyShortenRequest")["required"])
	require.Equal(t, []any{"base64"}, midjourneyConstraint(schemas, "MidjourneyDescribeRequest")["required"])
	blendSchema := midjourneyConstraint(schemas, "MidjourneyBlendRequest")
	require.Equal(t, []any{"base64Array"}, blendSchema["required"])
	blendImages := blendSchema["properties"].(map[string]any)["base64Array"].(map[string]any)
	require.Equal(t, float64(2), blendImages["minItems"])
	require.Equal(t, float64(5), blendImages["maxItems"])
	uploadSchema := midjourneyConstraint(schemas, "MidjourneyUploadRequest")
	require.Equal(t, []any{"base64Array"}, uploadSchema["required"])
	for _, contract := range []struct {
		path       string
		requestRef string
	}{
		{path: "/mj/submit/action", requestRef: "#/components/requestBodies/MidjourneyAction"},
		{path: "/mj/submit/shorten", requestRef: "#/components/requestBodies/MidjourneyShorten"},
		{path: "/mj/submit/modal", requestRef: "#/components/requestBodies/MidjourneyModal"},
		{path: "/mj/submit/imagine", requestRef: "#/components/requestBodies/MidjourneyImagine"},
		{path: "/mj/submit/change", requestRef: "#/components/requestBodies/MidjourneyChange"},
		{path: "/mj/submit/simple-change", requestRef: "#/components/requestBodies/MidjourneySimpleChange"},
		{path: "/mj/submit/describe", requestRef: "#/components/requestBodies/MidjourneyDescribe"},
		{path: "/mj/submit/blend", requestRef: "#/components/requestBodies/MidjourneyBlend"},
		{path: "/mj/submit/video", requestRef: "#/components/requestBodies/MidjourneyVideo"},
		{path: "/mj/task/list-by-condition", requestRef: "#/components/requestBodies/TaskIDs"},
		{path: "/mj/submit/upload-discord-images", requestRef: "#/components/requestBodies/MidjourneyUpload"},
		{path: "/{mode}/mj/submit/action", requestRef: "#/components/requestBodies/MidjourneyAction"},
		{path: "/{mode}/mj/submit/shorten", requestRef: "#/components/requestBodies/MidjourneyShorten"},
		{path: "/{mode}/mj/submit/modal", requestRef: "#/components/requestBodies/MidjourneyModal"},
		{path: "/{mode}/mj/submit/imagine", requestRef: "#/components/requestBodies/MidjourneyImagine"},
		{path: "/{mode}/mj/submit/change", requestRef: "#/components/requestBodies/MidjourneyChange"},
		{path: "/{mode}/mj/submit/simple-change", requestRef: "#/components/requestBodies/MidjourneySimpleChange"},
		{path: "/{mode}/mj/submit/describe", requestRef: "#/components/requestBodies/MidjourneyDescribe"},
		{path: "/{mode}/mj/submit/blend", requestRef: "#/components/requestBodies/MidjourneyBlend"},
		{path: "/{mode}/mj/submit/video", requestRef: "#/components/requestBodies/MidjourneyVideo"},
		{path: "/{mode}/mj/task/list-by-condition", requestRef: "#/components/requestBodies/TaskIDs"},
		{path: "/{mode}/mj/submit/upload-discord-images", requestRef: "#/components/requestBodies/MidjourneyUpload"},
	} {
		operation := paths[contract.path].(map[string]any)["post"].(map[string]any)
		require.Equal(t, contract.requestRef, operation["requestBody"].(map[string]any)["$ref"])
	}

	require.Equal(t, "#/components/responses/MidjourneyTask", responseRef(paths, "/mj/task/{id}/fetch", "get"))
	require.Equal(t, "#/components/responses/MidjourneyTaskList", responseRef(paths, "/mj/task/list-by-condition", "post"))
	require.Equal(t, "#/components/responses/MidjourneyTask", responseRef(paths, "/{mode}/mj/task/{id}/fetch", "get"))
	require.Equal(t, "#/components/responses/MidjourneyTaskList", responseRef(paths, "/{mode}/mj/task/list-by-condition", "post"))
	require.Equal(t, "#/components/responses/Midjourney", responseRef(paths, "/mj/insight-face/swap", "post"))
	require.Equal(t, "#/components/responses/Midjourney", responseRef(paths, "/{mode}/mj/insight-face/swap", "post"))
	require.Equal(t, "#/components/responses/MidjourneyUpload", responseRef(paths, "/mj/submit/upload-discord-images", "post"))
	require.Equal(t, "#/components/responses/MidjourneyUpload", responseRef(paths, "/{mode}/mj/submit/upload-discord-images", "post"))

	for _, path := range []string{"/mj/image/{id}", "/{mode}/mj/image/{id}"} {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		require.NotContains(t, operation, "security")
	}

	videoCreate := requestBodies["VideoCreate"].(map[string]any)["content"].(map[string]any)
	require.Equal(t, "#/components/schemas/OpenAIVideoCreateJSONRequest", videoCreate["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"])
	require.Equal(t, "#/components/schemas/OpenAIVideoCreateMultipartRequest", videoCreate["multipart/form-data"].(map[string]any)["schema"].(map[string]any)["$ref"])
}

func responseRef(paths map[string]any, path, method string) string {
	operation := paths[path].(map[string]any)[method].(map[string]any)
	return operation["responses"].(map[string]any)["200"].(map[string]any)["$ref"].(string)
}

func midjourneyConstraint(schemas map[string]any, name string) map[string]any {
	return schemas[name].(map[string]any)["allOf"].([]any)[1].(map[string]any)
}
