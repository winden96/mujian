package controller

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/gin-gonic/gin"
)

func CreateMujianImageGeneration(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mujianservice.MaxImageGenerationBodyBytes)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": "参考图请求过大，总大小不能超过 14MB"})
			return
		}
		mujianError(c, err)
		return
	}
	if c.Request.MultipartForm == nil {
		mujianError(c, errors.New("请使用 multipart/form-data 提交生图请求"))
		return
	}
	defer c.Request.MultipartForm.RemoveAll()

	files := append([]*multipart.FileHeader{}, c.Request.MultipartForm.File["reference_images"]...)
	files = append(files, c.Request.MultipartForm.File["reference_images[]"]...)
	references, err := readMujianImageReferences(files)
	if err != nil {
		mujianError(c, err)
		return
	}
	result, err := mujianservice.CreateImageGenerationWithContext(c.Request.Context(), c.GetInt("id"), c.Param("projectId"), mujianservice.CreateImageGenerationInput{
		SessionID: c.PostForm("session_id"), Prompt: c.PostForm("prompt"), Engine: c.PostForm("engine"), ModelID: c.PostForm("model_id"),
		AspectRatio: c.PostForm("aspect_ratio"), References: references,
	})
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
}

func GetMujianImageGeneration(c *gin.Context) {
	result, err := mujianservice.GetImageGeneration(c.GetInt("id"), c.Param("projectId"), c.Query("session_id"), c.Param("generationId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func RegenerateMujianImageGeneration(c *gin.Context) {
	result, err := mujianservice.RegenerateImageGenerationWithContext(c.Request.Context(), c.GetInt("id"), c.Param("projectId"), c.Query("session_id"), c.Param("generationId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
}

func GetMujianImageReferenceContent(c *gin.Context) {
	reference, err := mujianservice.GetImageReferenceContentWithContext(
		c.Request.Context(),
		c.GetInt("id"), c.Param("projectId"), c.Query("session_id"), c.Param("generationId"), c.Param("referenceId"),
	)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": reference.Name}))
	c.Data(http.StatusOK, reference.MIMEType, reference.Data)
}

func GetMujianImageGenerationContent(c *gin.Context) {
	content, err := mujianservice.GetImageGenerationContentWithContext(c.Request.Context(), c.GetInt("id"), c.Param("projectId"), c.Query("session_id"), c.Param("generationId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, content.MIMEType, content.Data)
}

func readMujianImageReferences(files []*multipart.FileHeader) ([]mujianservice.ImageReferenceInput, error) {
	if len(files) > mujianservice.MaxImageReferenceCount {
		return nil, errors.New("参考图最多 3 张")
	}
	references := make([]mujianservice.ImageReferenceInput, 0, len(files))
	total := int64(0)
	for _, header := range files {
		if header.Size > mujianservice.MaxImageReferenceBytes {
			return nil, errors.New("单张参考图不能超过 10MB")
		}
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, mujianservice.MaxImageReferenceBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > mujianservice.MaxImageReferenceBytes {
			return nil, errors.New("单张参考图不能超过 10MB")
		}
		total += int64(len(data))
		if total > mujianservice.MaxImageReferenceTotalBytes {
			return nil, errors.New("参考图总大小不能超过 14MB")
		}
		references = append(references, mujianservice.ImageReferenceInput{Name: header.Filename, Data: data})
	}
	return references, nil
}
