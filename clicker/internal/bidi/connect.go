package bidi

import (
	"net/http"
	"net/url"
	"strings"
)

// ConnectRemote connects to a remote BiDi endpoint, creates a client,
// and either establishes a new session or attaches to an existing one
// if the URL already contains /session/<sessionId>/.
func ConnectRemote(rawURL string, headers http.Header) (*Connection, *Client, string, error) {
	conn, err := ConnectWithHeaders(rawURL, headers)
	if err != nil {
		return nil, nil, "", err
	}

	client := NewClient(conn)

	// If this is an existing-session BiDi URL like:
	// ws://127.0.0.1:4444/session/<sessionId>/se/bidi
	// then reuse that session ID instead of creating a new session.
	if sessionID, ok := sessionIDFromBiDiURL(rawURL); ok {
		return conn, client, sessionID, nil
	}

	// Otherwise create a new session.
	result, err := client.SessionNew(map[string]interface{}{})
	if err != nil {
		conn.Close()
		return nil, nil, "", err
	}

	return conn, client, result.SessionID, nil
}

func sessionIDFromBiDiURL(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "session" && parts[i+1] != "" {
			return parts[i+1], true
		}
	}

	return "", false
}
