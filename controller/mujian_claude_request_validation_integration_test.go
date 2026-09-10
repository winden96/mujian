package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestMalformedOpenAIClaudeFieldsAreRejectedBeforeReservation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "numeric tool schema type",
			body: `{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":1}}}]}`,
			want: "function.parameters.type",
		},
		{
			name: "numeric stop sequence",
			body: `{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"x"}],"stop":[1]}`,
			want: "stop sequence at index 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaudeRelayIntegrationFixture(
				t,
				func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusInternalServerError) },
				func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusInternalServerError) },
			)

			response := fixture.relayRaw(t, "/v1/chat/completions", test.body)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), test.want)
			require.NotContains(t, response.Body.String(), fixture.zenChannel.Key)
			require.NotContains(t, response.Body.String(), fixture.tabChannel.Key)
			require.Empty(t, fixture.zenCapture.snapshot())
			require.Empty(t, fixture.tabCapture.snapshot())

			var user model.User
			require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
			require.Equal(t, claudeRelayInitialQuota, user.Quota)
			require.Zero(t, user.UsedQuota)
			var token model.Token
			require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
			require.Equal(t, claudeRelayInitialQuota, token.RemainQuota)
			require.Zero(t, token.UsedQuota)

			var ledgerRows int64
			require.NoError(t, fixture.db.Model(&model.SubscriptionPreConsumeRecord{}).
				Where("user_id = ?", fixture.user.Id).Count(&ledgerRows).Error)
			require.Zero(t, ledgerRows, "malformed input must fail before strict pre-consumption")
			var consumeLogs int64
			require.NoError(t, fixture.db.Model(&model.Log{}).
				Where("type = ?", model.LogTypeConsume).Count(&consumeLogs).Error)
			require.Zero(t, consumeLogs)
		})
	}
}
