package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"
)

func TestJPEGAttachmentUsesSupportedFilename(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	uploads := 0
	service.SetHost(func(method string, payload any, out any) error {
		uploads++
		var wire struct {
			Headers http.Header `json:"headers"`
			Body    []byte      `json:"body"`
		}
		if method != "host.http.do" {
			t.Fatalf("unexpected host method: %s", method)
		}
		if err := json.Unmarshal(jsonBytes(payload), &wire); err != nil {
			t.Fatal(err)
		}
		mediaType, params, err := mime.ParseMediaType(wire.Headers.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatal("invalid multipart upload")
		}
		reader := multipart.NewReader(bytes.NewReader(wire.Body), params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if part.FileName() != "image.jpg" {
			t.Fatalf("JPEG filename = %q, want image.jpg; upstream rejects .jfif and .jpe", part.FileName())
		}
		data, err := io.ReadAll(part)
		if err != nil || !bytes.Equal(data, encoded.Bytes()) || part.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatal("JPEG bytes or media type changed during upload")
		}
		*out.(*upstreamResponse) = upstreamResponse{StatusCode: 200, Body: jsonBytes(map[string]any{"openai_file_id": "jpeg-uploaded"})}
		return nil
	})
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	request := imageRequest(map[string]any{"type": "input_image", "image_url": dataURL, "detail": "high"})
	body, _, err := service.prepareRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	part := objectValue(lastUserContent(body)[0])
	if uploads != 1 || part["file_id"] != "jpeg-uploaded" || part["image_url"] != nil || part["detail"] != "high" {
		t.Fatal("JPEG attachment reference was not preserved")
	}
}
