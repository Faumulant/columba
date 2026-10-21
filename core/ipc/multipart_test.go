package ipc

// 测试用的 multipart 请求构造（handleSendFiles 需要它才能走到互斥判定）。

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newMultipartRequest(t *testing.T, name string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", name)
	if err != nil {
		t.Fatalf("创建表单文件: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("写入表单: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/send-files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}
