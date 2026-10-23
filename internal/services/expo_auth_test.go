package services

import (
	"errors"
	"net/http"
	"testing"

	"expo-open-ota/internal/types"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"
)

func TestValidateExpoAuth(t *testing.T) {
	const user = `{"data":{"meActor":{"__typename":"User","id":"user-id","username":"publisher","email":"publisher@example.com"}}}`
	const robot = `{"data":{"me":null,"meActor":{"__typename":"Robot","id":"robot-id"}}}`

	tests := []struct {
		name          string
		callerBody    string
		serverBody    string
		callerStatus  int
		serverStatus  int
		transportFail bool
		noAuth        bool
		noServerToken bool
		session       bool
		wantID        string
		wantUsername  string
		wantError     string
	}{
		{name: "personal tokens for same user", callerBody: user, serverBody: user, wantID: "user-id", wantUsername: "publisher"},
		{name: "tokens for same Robot without usernames", callerBody: robot, serverBody: robot, wantID: "robot-id"},
		{name: "user session", callerBody: user, serverBody: user, session: true, wantID: "user-id", wantUsername: "publisher"},
		{
			name:       "same ID with different username",
			callerBody: user,
			serverBody: `{"data":{"meActor":{"__typename":"User","id":"user-id","username":"renamed"}}}`,
			wantID:     "user-id", wantUsername: "publisher",
		},
		{
			name:       "different IDs with same username",
			callerBody: `{"data":{"meActor":{"__typename":"User","id":"other-id","username":"publisher"}}}`,
			serverBody: user, wantError: "expo actor does not match server expo actor",
		},
		{
			name:       "different Robots with missing usernames",
			callerBody: `{"data":{"me":null,"meActor":{"__typename":"Robot","id":"other-robot-id"}}}`,
			serverBody: robot, wantError: "expo actor does not match server expo actor",
		},
		{name: "user cannot publish as server Robot", callerBody: user, serverBody: robot, wantError: "expo actor does not match server expo actor"},
		{name: "Robot cannot publish as server user", callerBody: robot, serverBody: user, wantError: "expo actor does not match server expo actor"},
		{name: "missing credentials", noAuth: true, wantError: "no valid Expo auth provided"},
		{name: "missing server token", callerBody: user, noServerToken: true, wantError: "server Expo access token is not configured"},
		{name: "null caller actor", callerBody: `{"data":{"meActor":null}}`, serverBody: robot, wantError: "no Expo actor ID found"},
		{name: "missing caller data", callerBody: `{}`, serverBody: user, wantError: "no Expo actor ID found"},
		{name: "empty caller ID", callerBody: `{"data":{"meActor":{"id":""}}}`, serverBody: user, wantError: "no Expo actor ID found"},
		{name: "blank caller ID", callerBody: `{"data":{"meActor":{"id":" "}}}`, serverBody: user, wantError: "no Expo actor ID found"},
		{name: "null server actor", callerBody: user, serverBody: `{"data":{"meActor":null}}`, wantError: "could not resolve server Expo actor: no Expo actor ID found"},
		{name: "both actors empty", callerBody: `{"data":{"meActor":{}}}`, serverBody: `{"data":{"meActor":{}}}`, wantError: "no Expo actor ID found"},
		{
			name:       "caller GraphQL error with partial actor data",
			callerBody: `{"data":{"meActor":{"id":"user-id"}},"errors":[{"message":"Unauthorized"}]}`,
			serverBody: user, wantError: "Expo actor query returned GraphQL errors",
		},
		{
			name:       "server GraphQL error with partial actor data",
			callerBody: user,
			serverBody: `{"data":{"meActor":{"id":"user-id"}},"errors":[{"message":"Unauthorized"}]}`,
			wantError:  "could not resolve server Expo actor: Expo actor query returned GraphQL errors",
		},
		{name: "revoked caller token", callerBody: `Unauthorized`, callerStatus: http.StatusUnauthorized, serverBody: user, wantError: "GraphQL request failed with status: 401"},
		{name: "server HTTP failure", callerBody: user, serverBody: `Unavailable`, serverStatus: http.StatusServiceUnavailable, wantError: "could not resolve server Expo actor: GraphQL request failed with status: 503"},
		{name: "malformed actor response", callerBody: `not JSON`, serverBody: user, wantError: "invalid character"},
		{name: "Expo network failure", transportFail: true, wantError: "Expo unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EXPO_ACCESS_TOKEN", "server-token")
			if tt.noServerToken {
				t.Setenv("EXPO_ACCESS_TOKEN", "")
			}
			httpmock.Activate()
			t.Cleanup(httpmock.DeactivateAndReset)
			httpmock.RegisterResponder(http.MethodPost, "https://api.expo.dev/graphql", func(req *http.Request) (*http.Response, error) {
				if tt.transportFail {
					return nil, errors.New("Expo unavailable")
				}
				body, status := tt.callerBody, tt.callerStatus
				if req.Header.Get("Authorization") == "Bearer server-token" {
					body, status = tt.serverBody, tt.serverStatus
				} else if tt.session {
					require.Equal(t, "session-secret", req.Header.Get("expo-session"))
				} else {
					require.Equal(t, "Bearer client-token", req.Header.Get("Authorization"))
				}
				if status == 0 {
					status = http.StatusOK
				}
				return httpmock.NewStringResponse(status, body), nil
			})

			token, session := "client-token", "session-secret"
			auth := types.ExpoAuth{Token: &token}
			if tt.session {
				auth = types.ExpoAuth{SessionSecret: &session}
			}
			if tt.noAuth {
				auth = types.ExpoAuth{}
			}
			account, err := ValidateExpoAuth(auth)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				require.Nil(t, account)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantID, account.Id)
			require.Equal(t, tt.wantUsername, account.Username)
			if tt.wantUsername != "" {
				require.Equal(t, "publisher@example.com", account.Email)
			}
		})
	}
}
