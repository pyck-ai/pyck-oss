package httpclient

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func MakeRequest(method string, baseUrl string, headers map[string]string, queryParams map[string]string, body []byte, secure bool) ([]byte, error) {
	requestUrl, err := url.Parse(baseUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %s", err)
	}

	if queryParams != nil {
		query := requestUrl.Query()
		for key, value := range queryParams {
			query.Set(key, value)
		}
		requestUrl.RawQuery = query.Encode()
	}

	request, err := http.NewRequest(method, requestUrl.String(), bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %s", err)
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}

	requestClient := http.DefaultClient
	if !secure {
		// WARNING: Disabling SSL/TLS verification can be insecure and should only be used for testing or connecting to trusted servers with self-signed certificates.
		requestClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	response, err := requestClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %s", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("request failed with status code %d", response.StatusCode)
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %s", err)
	}
	return responseBody, nil
}
