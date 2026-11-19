package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatibleImagesGatewayMappedAliasGenerationAndEdit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{openAIImagesGenerationsEndpoint, openAIImagesEditsEndpoint} {
		t.Run(endpoint, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat","size":"1024x1024","response_format":"b64_json"}`)
			contentType := "application/json"
			if endpoint == openAIImagesEditsEndpoint {
				var buffer bytes.Buffer
				writer := multipart.NewWriter(&buffer)
				for k, v := range map[string]string{"model": "gpt-image-1", "prompt": "edit cat", "size": "1024x1024"} {
					require.NoError(t, writer.WriteField(k, v))
				}
				part, err := writer.CreateFormFile("image", "cat.png")
				require.NoError(t, err)
				_, err = part.Write([]byte("image-bytes"))
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				body = buffer.Bytes()
				contentType = writer.FormDataContentType()
			}
			req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req
			transport := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"created":1710000007,"data":[{"b64_json":"aGVsbG8="}]}`))}}
			svc := &OpenAIGatewayService{httpUpstream: transport}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "internal-key", "base_url": "https://litellm.example/v1", "model_mapping": map[string]any{"gpt-image-1": "image-gpt"}}}
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, "https://litellm.example"+endpoint, transport.lastReq.URL.String())
			require.Equal(t, "Bearer internal-key", transport.lastReq.Header.Get("Authorization"))
			require.Equal(t, "gpt-image-1", result.Model)
			require.Equal(t, "image-gpt", result.UpstreamModel)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, "1K", result.ImageSize)
			if endpoint == openAIImagesGenerationsEndpoint {
				require.Equal(t, "image-gpt", gjson.GetBytes(transport.lastBody, "model").String())
			} else {
				mapped, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				require.Equal(t, "gpt-image-1", mapped.Model)
				// Decode forwarded multipart without applying the public-model validator.
				req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(transport.lastBody))
				req.Header.Set("Content-Type", transport.lastReq.Header.Get("Content-Type"))
				require.NoError(t, req.ParseMultipartForm(1024))
				defer req.MultipartForm.RemoveAll()
				require.Equal(t, "image-gpt", req.FormValue("model"))
				file, _, err := req.FormFile("image")
				require.NoError(t, err)
				defer file.Close()
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.Equal(t, "image-bytes", string(data))
			}
		})
	}
}
