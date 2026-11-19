package handler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplicateMusicOutputURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"single URL", `"https://cdn.example/audio.mp3"`, "https://cdn.example/audio.mp3"},
		{"URL list", `["https://cdn.example/audio.mp3"]`, "https://cdn.example/audio.mp3"},
		{"audio object", `{"audio_url":"https://cdn.example/audio.mp3"}`, "https://cdn.example/audio.mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := replicateMusicOutputURL(json.RawMessage(tc.raw))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	_, err := replicateMusicOutputURL(json.RawMessage(`{"video":"https://cdn.example/video.mp4"}`))
	require.Error(t, err)
}
