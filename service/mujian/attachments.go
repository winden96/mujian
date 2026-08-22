package mujian

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxAgentAttachments          = 4
	MaxAgentAttachmentBytes      = 10 << 20
	MaxAgentAttachmentTotalBytes = 20 << 20
	MaxAgentRequestBodyBytes     = 30 << 20

	maxAgentTextBytes = 1 << 20
	maxDOCXXMLBytes   = 8 << 20
)

type AgentAttachment struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type preparedAgentAttachment struct {
	Name     string
	Kind     string
	MIMEType string
	Data     string
	Text     string
}

func prepareAgentAttachments(inputs []AgentAttachment) ([]preparedAgentAttachment, error) {
	if len(inputs) > MaxAgentAttachments {
		return nil, fmt.Errorf("每次最多上传 %d 个附件", MaxAgentAttachments)
	}
	prepared := make([]preparedAgentAttachment, 0, len(inputs))
	totalBytes := 0
	for _, input := range inputs {
		name, err := safeAttachmentName(input.Name)
		if err != nil {
			return nil, err
		}
		if len(input.Data) > base64.StdEncoding.EncodedLen(MaxAgentAttachmentBytes)+2 {
			return nil, fmt.Errorf("附件 %s 超过 10MB", name)
		}
		data, err := base64.StdEncoding.DecodeString(input.Data)
		if err != nil {
			return nil, fmt.Errorf("附件 %s 数据无效", name)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("附件 %s 为空", name)
		}
		if len(data) > MaxAgentAttachmentBytes {
			return nil, fmt.Errorf("附件 %s 超过 10MB", name)
		}
		totalBytes += len(data)
		if totalBytes > MaxAgentAttachmentTotalBytes {
			return nil, errors.New("附件总大小不能超过 20MB")
		}

		attachment, err := prepareAgentAttachment(name, data)
		if err != nil {
			return nil, err
		}
		attachment.Data = input.Data
		prepared = append(prepared, attachment)
	}
	return prepared, nil
}

func safeAttachmentName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "" || name == "." || len(name) > 180 {
		return "", errors.New("附件名称无效")
	}
	for _, character := range name {
		if character < 32 || character == 127 {
			return "", errors.New("附件名称无效")
		}
	}
	return name, nil
}

func prepareAgentAttachment(name string, data []byte) (preparedAgentAttachment, error) {
	extension := strings.ToLower(filepath.Ext(name))
	switch extension {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		mimeType, ok := validatedImageMIME(extension, data)
		if !ok {
			return preparedAgentAttachment{}, fmt.Errorf("附件 %s 的图片格式与内容不匹配", name)
		}
		return preparedAgentAttachment{Name: name, Kind: "image", MIMEType: mimeType}, nil
	case ".pdf":
		prefix := data
		if len(prefix) > 1024 {
			prefix = prefix[:1024]
		}
		if !bytes.Contains(prefix, []byte("%PDF-")) {
			return preparedAgentAttachment{}, fmt.Errorf("附件 %s 不是有效的 PDF", name)
		}
		return preparedAgentAttachment{Name: name, Kind: "file", MIMEType: "application/pdf"}, nil
	case ".txt", ".md":
		if len(data) > maxAgentTextBytes {
			return preparedAgentAttachment{}, fmt.Errorf("文本附件 %s 不能超过 1MB", name)
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return preparedAgentAttachment{}, fmt.Errorf("文本附件 %s 必须是 UTF-8 编码", name)
		}
		return preparedAgentAttachment{Name: name, Kind: "text", MIMEType: "text/plain", Text: string(data)}, nil
	case ".docx":
		text, err := extractDOCXText(data)
		if err != nil {
			return preparedAgentAttachment{}, fmt.Errorf("Word 附件 %s 无法解析: %w", name, err)
		}
		return preparedAgentAttachment{Name: name, Kind: "text", MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Text: text}, nil
	default:
		return preparedAgentAttachment{}, fmt.Errorf("不支持附件 %s，仅支持图片、PDF、Word、TXT 和 Markdown", name)
	}
}

func validatedImageMIME(extension string, data []byte) (string, bool) {
	detected := http.DetectContentType(data)
	switch extension {
	case ".png":
		return "image/png", detected == "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg", detected == "image/jpeg"
	case ".gif":
		return "image/gif", detected == "image/gif"
	case ".webp":
		valid := len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
		return "image/webp", valid
	default:
		return "", false
	}
}

func extractDOCXText(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("文件不是有效的 DOCX")
	}
	hasContentTypes := false
	var document *zip.File
	for _, file := range reader.File {
		switch file.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "word/document.xml":
			document = file
		}
	}
	if !hasContentTypes || document == nil {
		return "", errors.New("缺少 Word 正文")
	}
	if document.UncompressedSize64 > maxDOCXXMLBytes {
		return "", errors.New("Word 正文过大")
	}
	file, err := document.Open()
	if err != nil {
		return "", errors.New("无法读取 Word 正文")
	}
	defer file.Close()

	decoder := xml.NewDecoder(io.LimitReader(file, maxDOCXXMLBytes+1))
	var text strings.Builder
	for {
		token, decodeErr := decoder.Token()
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			return "", errors.New("Word 正文 XML 无效")
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "t":
				var value string
				if err = decoder.DecodeElement(&value, &element); err != nil {
					return "", errors.New("Word 正文 XML 无效")
				}
				text.WriteString(value)
			case "tab":
				text.WriteByte('\t')
			case "br":
				text.WriteByte('\n')
			}
		case xml.EndElement:
			if element.Name.Local == "p" {
				text.WriteByte('\n')
			}
		}
		if text.Len() > maxAgentTextBytes {
			return "", errors.New("Word 文本不能超过 1MB")
		}
	}
	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", errors.New("Word 正文为空")
	}
	return result, nil
}

func agentUserContent(turn *agentTurn) interface{} {
	if len(turn.Attachments) == 0 {
		return turn.Content
	}
	parts := []map[string]interface{}{{"type": "text", "text": turn.Content}}
	for _, attachment := range turn.Attachments {
		switch attachment.Kind {
		case "image":
			parts = append(parts, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]string{
					"url":    "data:" + attachment.MIMEType + ";base64," + attachment.Data,
					"detail": "high",
				},
			})
		case "file":
			parts = append(parts, map[string]interface{}{
				"type": "file",
				"file": map[string]string{
					"filename":  attachment.Name,
					"file_data": attachment.Data,
				},
			})
		case "text":
			parts = append(parts, map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("\n\n--- 附件 %q 开始 ---\n%s\n--- 附件结束 ---", attachment.Name, attachment.Text),
			})
		}
	}
	return parts
}

func agentUserDisplayContent(content string, attachments []preparedAgentAttachment) string {
	if content == "" {
		content = "请分析附件内容。"
	}
	if len(attachments) == 0 {
		return content
	}
	var display strings.Builder
	display.WriteString(content)
	for _, attachment := range attachments {
		display.WriteString("\n📎 ")
		display.WriteString(attachment.Name)
	}
	return display.String()
}
