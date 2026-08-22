package mujian

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func encodedAttachment(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func testDOCX(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	contentTypes, err := writer.Create("[Content_Types].xml")
	require.NoError(t, err)
	_, err = contentTypes.Write([]byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`))
	require.NoError(t, err)
	document, err := writer.Create("word/document.xml")
	require.NoError(t, err)
	_, err = document.Write([]byte(documentXML))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

func TestPrepareAgentAttachmentsBuildsClaudeContent(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nplaceholder")
	pdf := []byte("%PDF-1.7\nplaceholder")
	attachments, err := prepareAgentAttachments([]AgentAttachment{
		{Name: "reference.png", Data: encodedAttachment(png)},
		{Name: "brief.pdf", Data: encodedAttachment(pdf)},
		{Name: "notes.txt", Data: encodedAttachment([]byte("主角必须保持沉默"))},
	})

	require.NoError(t, err)
	require.Len(t, attachments, 3)
	turn := &agentTurn{Content: "根据附件修改", Attachments: attachments}
	parts, ok := agentUserContent(turn).([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, parts, 4)
	require.Equal(t, "text", parts[0]["type"])
	require.Equal(t, "image_url", parts[1]["type"])
	require.Equal(t, "file", parts[2]["type"])
	require.Equal(t, "text", parts[3]["type"])
	require.Contains(t, parts[3]["text"], "主角必须保持沉默")
	require.Equal(t, "根据附件修改\n📎 reference.png\n📎 brief.pdf\n📎 notes.txt", agentUserDisplayContent(turn.Content, attachments))
}

func TestPrepareAgentAttachmentsExtractsDOCXText(t *testing.T) {
	docx := testDOCX(t, `<?xml version="1.0"?><w:document xmlns:w="urn:test"><w:body><w:p><w:r><w:t>第一段</w:t></w:r></w:p><w:p><w:r><w:t>第二段</w:t></w:r></w:p></w:body></w:document>`)
	attachments, err := prepareAgentAttachments([]AgentAttachment{{
		Name: "story.docx", Data: encodedAttachment(docx),
	}})

	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.Equal(t, "第一段\n第二段", attachments[0].Text)
	require.Equal(t, "text", attachments[0].Kind)
}

func TestPrepareAgentAttachmentsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		attachments []AgentAttachment
		message     string
	}{
		{name: "unsupported", attachments: []AgentAttachment{{Name: "script.exe", Data: encodedAttachment([]byte("value"))}}, message: "不支持附件"},
		{name: "bad base64", attachments: []AgentAttachment{{Name: "notes.txt", Data: "not-base64"}}, message: "数据无效"},
		{name: "fake image", attachments: []AgentAttachment{{Name: "photo.png", Data: encodedAttachment([]byte("not an image"))}}, message: "格式与内容不匹配"},
		{name: "fake pdf", attachments: []AgentAttachment{{Name: "brief.pdf", Data: encodedAttachment([]byte("not a pdf"))}}, message: "不是有效的 PDF"},
		{name: "fake docx", attachments: []AgentAttachment{{Name: "brief.docx", Data: encodedAttachment([]byte("not a zip"))}}, message: "无法解析"},
		{name: "too many", attachments: []AgentAttachment{{Name: "1.txt", Data: "MQ=="}, {Name: "2.txt", Data: "Mg=="}, {Name: "3.txt", Data: "Mw=="}, {Name: "4.txt", Data: "NA=="}, {Name: "5.txt", Data: "NQ=="}}, message: "最多上传"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareAgentAttachments(test.attachments)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestAgentUserContentRemainsStringWithoutAttachments(t *testing.T) {
	turn := &agentTurn{Content: "普通消息"}
	require.Equal(t, "普通消息", agentUserContent(turn))
	require.Equal(t, "请分析附件内容。", agentUserDisplayContent("", nil))
}
