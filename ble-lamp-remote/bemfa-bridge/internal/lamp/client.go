package lamp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/command"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return newClient(baseURL, token, &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
}

func newClient(baseURL, token string, httpClient *http.Client) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

func (c *Client) Execute(ctx context.Context, cmd command.Command) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+cmd.Path, http.NoBody)
	if err != nil {
		return fmt.Errorf("build lamp request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call lamp gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("lamp gateway returned %s", resp.Status)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("read lamp response: %w", err)
	}
	return nil
}
