package mujianobject

import (
	"context"
	"errors"
	"io"

	"gorm.io/gorm"
)

const MaxUploadBytes int64 = 64 << 20

var (
	ErrObjectAlreadyExists = errors.New("object already exists")
	ErrObjectNotFound      = errors.New("object not found")
)

type ObjectKind string

const (
	ObjectKindGeneration ObjectKind = "generation"
	ObjectKindReference  ObjectKind = "reference"
	ObjectKindShot       ObjectKind = "shot"
)

type Object struct {
	Key         string
	PublicURL   string
	ContentType string
	SizeBytes   int64
	SHA256      string
	ETag        string
}

type UploadInput struct {
	ObjectKey   string
	ContentType string
	Body        io.Reader
	SizeBytes   int64
}

type PutInput struct {
	Key         string
	ContentType string
	Body        io.Reader
	SizeBytes   int64
	SHA256      string
}

// Store is the application-facing object store. Its narrow interface allows
// callers to inject an in-memory fake without loading the Huawei SDK.
type Store interface {
	BuildKey(kind ObjectKind, id, contentType string) (string, error)
	Upload(ctx context.Context, input UploadInput) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	Head(ctx context.Context, key string) (Object, error)
	Delete(ctx context.Context, key string) error
	PublicURL(key string) (string, error)
}

// Backend is the provider boundary used by the durable Store implementation.
type Backend interface {
	BuildKey(kind ObjectKind, id, contentType string) (string, error)
	Put(ctx context.Context, input PutInput) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	Head(ctx context.Context, key string) (Object, error)
	Delete(ctx context.Context, key string) error
	PublicURL(key string) (string, error)
}

func NewStore(db *gorm.DB, backend Backend) (Store, error) {
	return newDurableStore(db, backend)
}
