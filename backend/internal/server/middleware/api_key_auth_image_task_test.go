package middleware

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAsyncImageTaskRead(t *testing.T) {
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/tasks/imgtask_123"))
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodPost, "/v1/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/generations"))
}

func TestIsCompatibleVideoRead(t *testing.T) {
	for _, path := range []string{"/v1/videos/mvid_123", "/videos/mvid_123/content", "/v1/videos/generations/mvid_123"} {
		require.True(t, isCompatibleVideoRead(http.MethodGet, path))
		require.False(t, isCompatibleVideoRead(http.MethodPost, path))
		require.False(t, isCompatibleVideoRead(http.MethodDelete, path))
	}
	for _, path := range []string{"/v1/videos", "/v1/videos/generations", "/v1/videos/provider-id", "/v1/images/mvid_123"} {
		require.False(t, isCompatibleVideoRead(http.MethodGet, path))
	}
}

func TestIsReplicateMusicTaskOperation(t *testing.T) {
	for _, path := range []string{"/v1/music/rmus_123", "/v1/music/rmus_123/content", "/music/rmus_123"} {
		require.True(t, isReplicateMusicTaskOperation(http.MethodGet, path), path)
		require.True(t, isReplicateMusicTaskOperation(http.MethodDelete, path), path)
	}
	require.False(t, isReplicateMusicTaskOperation(http.MethodPost, "/v1/music/rmus_123"))
	require.False(t, isReplicateMusicTaskOperation(http.MethodGet, "/v1/music/not-a-task"))
}
