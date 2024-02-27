package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHappyPath(t *testing.T) {
	env := &conf.Env{
		DiscordGuildID:  "test-guild",
		DiscordBotToken: "test-bot-token",
	}
	c := NewCache(env)

	// Serve a fake discord API
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v10/guilds/test-guild/scheduled-events", r.URL.Path)

		file, err := os.Open("fixtures/events.json")
		require.NoError(t, err)
		io.Copy(w, file)
		file.Close()
	}))
	t.Cleanup(svr.Close)
	c.baseURL = svr.URL

	c.fillCache(context.Background())

	// Get 30 days of events from when the fixture was captured
	until := time.Unix(1709006369, 0).Add(time.Hour * 24 * 30)
	events, err := c.GetEvents(until)
	require.NoError(t, err)

	by, err := json.Marshal(&events)
	require.NoError(t, err)

	// Compare the cache against a fixture
	expect, err := os.ReadFile("fixtures/events.exp.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(expect), string(by))
}
