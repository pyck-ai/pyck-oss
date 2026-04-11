package workflowsdk

import (
	"net/http"
	"time"
)

func NewDefaultHTTPClient(authToken string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &HTTPAuthTransport{
			Token: authToken,
		},
	}
}

type HTTPAuthTransport struct {
	Token     string
	Transport http.RoundTripper // The original transport (usually http.DefaultTransport)
}

// RoundTrip implements the http.RoundTripper interface.
func (t *HTTPAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())

	if t.Token != "" {
		newReq.Header.Set("Authorization", "Bearer "+t.Token)
	}

	transport := t.Transport

	if transport == nil {
		transport = http.DefaultTransport
	}

	return transport.RoundTrip(newReq)
}
