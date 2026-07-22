package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const maxResponseBodyBytes = int64(64 << 10)

// Client delivers already marshaled messages to one Feishu custom-bot
// webhook. It performs one attempt only; retry policy belongs to Alertmanager.
type Client struct {
	webhookURL string
	httpClient *http.Client
}

// NewClient constructs a Feishu client. Production configuration validates
// HTTPS separately; accepting HTTP here keeps local unit testing practical.
func NewClient(webhookURL string, httpClient *http.Client) (*Client, error) {
	parsedURL, err := url.Parse(webhookURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil {
		return nil, errors.New("invalid Feishu webhook URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	if clientCopy.CheckRedirect == nil {
		clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return &Client{webhookURL: webhookURL, httpClient: &clientCopy}, nil
}

// DeliveryError describes whether retrying the same delivery may succeed.
// Error messages contain only status and numeric business codes, never the
// webhook URL or response body.
type DeliveryError struct {
	permanent    bool
	httpStatus   int
	businessCode int
	detailCode   int
}

func (e *DeliveryError) Error() string {
	switch {
	case e.httpStatus != 0:
		return fmt.Sprintf("Feishu delivery failed with HTTP status %d", e.httpStatus)
	case e.businessCode != 0 || e.detailCode != 0:
		return fmt.Sprintf("Feishu rejected message with business code %d (detail %d)", e.businessCode, e.detailCode)
	default:
		return "Feishu delivery failed"
	}
}

// Permanent reports whether retrying an unchanged message is not expected to
// help. HTTP 408/429, all 5xx responses, network failures, and unknown business
// errors are treated as temporary.
func (e *DeliveryError) Permanent() bool { return e.permanent }

// IsPermanent is false for unknown error types, preserving delivery retries.
func IsPermanent(err error) bool {
	var deliveryError *DeliveryError
	return errors.As(err, &deliveryError) && deliveryError.Permanent()
}

// Send posts a complete Feishu custom-bot request body. A successful HTTP
// response is not sufficient: Feishu must explicitly return business code 0.
func (c *Client) Send(ctx context.Context, message []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(message))
	if err != nil {
		return &DeliveryError{}
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "alertmanager-feishu-adapter/1")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return &DeliveryError{}
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if readErr != nil {
		return &DeliveryError{}
	}
	if int64(len(responseBody)) > maxResponseBodyBytes {
		return &DeliveryError{}
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &DeliveryError{
			permanent: response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError &&
				response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests,
			httpStatus: response.StatusCode,
		}
	}

	var result webhookResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return &DeliveryError{}
	}
	if result.Code != nil && *result.Code == 0 {
		return nil
	}
	if result.Code == nil && result.StatusCode != nil && *result.StatusCode == 0 {
		return nil
	}

	businessCode := numericValue(result.Code)
	if result.Code == nil {
		businessCode = numericValue(result.StatusCode)
	}
	detailCode := numericValue(result.Data.ErrCode)
	if detailCode == 0 {
		detailCode = numericValue(result.ErrCode)
	}
	return &DeliveryError{
		permanent:    isPermanentCardError(businessCode, detailCode),
		businessCode: businessCode,
		detailCode:   detailCode,
	}
}

type webhookResponse struct {
	Code       *int `json:"code"`
	StatusCode *int `json:"StatusCode"`
	ErrCode    *int `json:"err_code"`
	Data       struct {
		ErrCode *int `json:"err_code"`
	} `json:"data"`
}

func numericValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func isPermanentCardError(businessCode, detailCode int) bool {
	// Feishu returns this pair for malformed Card JSON. Re-sending an unchanged
	// body cannot succeed, so surface it to Alertmanager as a permanent error.
	return businessCode == 11246 || detailCode == 200621
}
