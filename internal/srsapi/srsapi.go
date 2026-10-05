// Package srsapi, bir SRS'in yönetim API'sinden bağlantıları listeler ve bağlantı keser.
// Davranış SRS 5.0.225'e karşı ölçülmüştür (bkz. docs/srs-findings.md).
package srsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// maxClients, tek istekte istenen bağlantı sayısıdır; SRS varsayılan olarak yalnızca 10 döner.
const maxClients = 100000

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Client struct {
	base string
	http *http.Client
}

// New: baseURL, SRS'in API adresidir (ör. http://srs-ts:1985).
func New(baseURL string) *Client {
	return &Client{base: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

// ClientIDs, SRS'e bağlı tüm istemcilerin (izleyici ve yayıncı) kimliklerini döner.
func (c *Client) ClientIDs(ctx context.Context) ([]string, error) {
	var body struct {
		Clients []struct {
			ID string `json:"id"`
		} `json:"clients"`
	}
	// Sondaki / gerekli: SRS onsuz yönlendirme döner.
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/clients/?count=%d", maxClients), &body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Clients))
	for _, cl := range body.Clients {
		ids = append(ids, cl.ID)
	}
	return ids, nil
}

// Kick, verilen kimlikli bağlantıyı keser.
func (c *Client) Kick(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("srsapi: geçersiz bağlantı kimliği %q", id)
	}
	return c.do(ctx, http.MethodDelete, "/api/v1/clients/"+id, nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("srsapi: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("srsapi: %s %s: yanıt çözülemedi: %w", method, path, err)
	}
	var status struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("srsapi: %s %s: yanıt çözülemedi: %w", method, path, err)
	}
	if status.Code != 0 {
		return fmt.Errorf("srsapi: %s %s: kod %d", method, path, status.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
