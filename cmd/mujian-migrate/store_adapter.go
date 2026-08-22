package main

import (
	"context"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/service/mujianobject"
)

type objectStoreAdapter struct {
	store mujianobject.Store
}

func (adapter objectStoreAdapter) BuildKey(kind, id, contentType string) (string, error) {
	var objectKind mujianobject.ObjectKind
	switch kind {
	case recordKindGeneration:
		objectKind = mujianobject.ObjectKindGeneration
	case recordKindReference:
		objectKind = mujianobject.ObjectKindReference
	case recordKindShot:
		objectKind = mujianobject.ObjectKindShot
	default:
		return "", fmt.Errorf("unsupported image record kind %q", kind)
	}
	return adapter.store.BuildKey(objectKind, id, contentType)
}

func (adapter objectStoreAdapter) Upload(ctx context.Context, key, contentType string, body io.Reader, sizeBytes int64) (storedObject, error) {
	object, err := adapter.store.Upload(ctx, mujianobject.UploadInput{
		ObjectKey: key, ContentType: contentType, Body: body, SizeBytes: sizeBytes,
	})
	return fromMujianObject(object), err
}

func (adapter objectStoreAdapter) Head(ctx context.Context, key string) (storedObject, error) {
	object, err := adapter.store.Head(ctx, key)
	return fromMujianObject(object), err
}

func (adapter objectStoreAdapter) Delete(ctx context.Context, key string) error {
	return adapter.store.Delete(ctx, key)
}

func fromMujianObject(object mujianobject.Object) storedObject {
	return storedObject{
		Key: object.Key, ContentType: object.ContentType, SizeBytes: object.SizeBytes,
		SHA256: object.SHA256, ETag: object.ETag,
	}
}
