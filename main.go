package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
)

var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	cmd.RunWebhookServer(GroupName, &dnscaleSolver{})
}

// dnscaleSolver implements the webhook.Solver interface for DNScale DNS.
type dnscaleSolver struct {
	client kubernetes.Interface
}

// secretKeySelector references a key in a Kubernetes Secret.
type secretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// dnscaleConfig holds per-issuer configuration for the DNScale solver.
type dnscaleConfig struct {
	APIUrl            string            `json:"apiUrl"`
	APITokenSecretRef secretKeySelector `json:"apiTokenSecretRef"`
}

func (d *dnscaleSolver) Name() string {
	return "dnscale"
}

// Present creates a TXT record for the dns-01 challenge.
func (d *dnscaleSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	if err := validateChallenge(ch); err != nil {
		return err
	}
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return fmt.Errorf("dnscale: load config: %w", err)
	}

	apiToken, err := d.getAPIToken(cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("dnscale: get API token: %w", err)
	}

	client := newDNScaleClient(cfg.APIUrl, apiToken)

	zoneID, _, err := client.findZoneByFQDN(ch.ResolvedZone)
	if err != nil {
		return fmt.Errorf("dnscale: find zone: %w", err)
	}

	recordName := strings.TrimSuffix(ch.ResolvedFQDN, ".")

	err = client.createTXTRecord(zoneID, recordName, ch.Key)
	if err != nil {
		return fmt.Errorf("dnscale: create TXT record: %w", err)
	}

	return nil
}

// CleanUp deletes the TXT record after challenge validation.
func (d *dnscaleSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	if err := validateChallenge(ch); err != nil {
		return err
	}
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return fmt.Errorf("dnscale: load config: %w", err)
	}

	apiToken, err := d.getAPIToken(cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("dnscale: get API token: %w", err)
	}

	client := newDNScaleClient(cfg.APIUrl, apiToken)

	zoneID, _, err := client.findZoneByFQDN(ch.ResolvedZone)
	if err != nil {
		return fmt.Errorf("dnscale: find zone: %w", err)
	}

	recordName := strings.TrimSuffix(ch.ResolvedFQDN, ".")

	err = client.deleteTXTRecord(zoneID, recordName, ch.Key)
	if err != nil {
		return fmt.Errorf("dnscale: delete TXT record: %w", err)
	}

	return nil
}

// Initialize sets up the Kubernetes clientset.
func (d *dnscaleSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}
	d.client = cl
	return nil
}

// getAPIToken retrieves the API token from the referenced Kubernetes Secret.
func (d *dnscaleSolver) getAPIToken(cfg dnscaleConfig, namespace string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secret, err := d.client.CoreV1().Secrets(namespace).Get(ctx, cfg.APITokenSecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, cfg.APITokenSecretRef.Name, err)
	}

	tokenBytes, ok := secret.Data[cfg.APITokenSecretRef.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", cfg.APITokenSecretRef.Key, namespace, cfg.APITokenSecretRef.Name)
	}

	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return "", fmt.Errorf("key %q in secret %s/%s is empty", cfg.APITokenSecretRef.Key, namespace, cfg.APITokenSecretRef.Name)
	}
	return token, nil
}

func validateChallenge(ch *v1alpha1.ChallengeRequest) error {
	if ch == nil || ch.ResourceNamespace == "" || ch.ResolvedZone == "" || ch.ResolvedFQDN == "" || strings.TrimSpace(ch.Key) == "" {
		return fmt.Errorf("dnscale: challenge requires resource namespace, resolved zone, resolved FQDN, and key")
	}
	return nil
}

func loadConfig(cfgJSON *extapi.JSON) (dnscaleConfig, error) {
	cfg := dnscaleConfig{
		APIUrl: "https://api.dnscale.eu",
	}

	if cfgJSON == nil {
		return cfg, fmt.Errorf("apiTokenSecretRef.name and apiTokenSecretRef.key are required")
	}

	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decode solver config: %w", err)
	}
	if cfg.APITokenSecretRef.Name == "" || cfg.APITokenSecretRef.Key == "" {
		return cfg, fmt.Errorf("apiTokenSecretRef.name and apiTokenSecretRef.key are required")
	}
	u, err := url.Parse(cfg.APIUrl)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return cfg, fmt.Errorf("apiUrl must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}

	return cfg, nil
}

// --- DNScale API client ---

type dnscaleClient struct {
	apiURL   string
	apiToken string
	http     *http.Client
}

type zoneEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type zonesResponse struct {
	Zones []zoneEntry `json:"zones"`
	Data  *struct {
		Zones      []zoneEntry `json:"zones"`
		Pagination *struct {
			HasMore bool `json:"has_more"`
		} `json:"pagination"`
	} `json:"data"`
}

func newDNScaleClient(apiURL, apiToken string) *dnscaleClient {
	return &dnscaleClient{
		apiURL:   strings.TrimSuffix(apiURL, "/"),
		apiToken: apiToken,
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *dnscaleClient) findZoneByFQDN(fqdn string) (string, string, error) {
	zones, err := c.listZones()
	if err != nil {
		return "", "", err
	}

	name := strings.TrimSuffix(fqdn, ".")

	for {
		for _, z := range zones {
			zoneName := strings.TrimSuffix(z.Name, ".")
			if strings.EqualFold(name, zoneName) {
				return z.ID, z.Name, nil
			}
		}
		idx := strings.Index(name, ".")
		if idx < 0 {
			break
		}
		name = name[idx+1:]
	}

	return "", "", fmt.Errorf("no zone found for %s", fqdn)
}

func (c *dnscaleClient) listZones() ([]zoneEntry, error) {
	var all []zoneEntry
	offset := 0

	for {
		url := fmt.Sprintf("%s/v1/zones?offset=%d&limit=100", c.apiURL, offset)

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read zones: %w", readErr)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("list zones: %d %s", resp.StatusCode, string(body))
		}

		var result zonesResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		page := result.Zones
		var hasMore bool
		if result.Data != nil {
			page = result.Data.Zones
		}
		if page == nil {
			return nil, fmt.Errorf("list zones: response does not contain a zones array")
		}
		hasMore = len(page) == 100
		if result.Data != nil && result.Data.Pagination != nil {
			hasMore = result.Data.Pagination.HasMore
		}
		all = append(all, page...)
		if !hasMore {
			break
		}
		if len(page) == 0 {
			return nil, fmt.Errorf("list zones: empty page with has_more=true")
		}
		offset += len(page)
	}

	return all, nil
}

func (c *dnscaleClient) createTXTRecord(zoneID, name, value string) error {
	payload, _ := json.Marshal(map[string]any{
		"name":    name,
		"type":    "TXT",
		"content": value,
		"ttl":     300,
	})

	url := fmt.Sprintf("%s/v1/zones/%s/records", c.apiURL, zoneID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("create record: %d %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *dnscaleClient) deleteTXTRecord(zoneID, name, value string) error {
	// Never send an unfiltered delete: it would remove every TXT value at this owner.
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("delete record: challenge value must not be empty")
	}
	endpoint := fmt.Sprintf("%s/v1/zones/%s/records/by-name/%s/TXT?%s", c.apiURL, url.PathEscape(zoneID), url.PathEscape(name), url.Values{"content": {value}}.Encode())
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 204 && resp.StatusCode != 404 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		// Some API versions return 500 when the requested content is already
		// absent. Confirm that state through a separate read before accepting
		// cleanup; never hide a failed delete while the challenge still exists.
		if resp.StatusCode == http.StatusInternalServerError {
			exists, err := c.txtRecordExists(zoneID, name, value)
			if err == nil && !exists {
				return nil
			}
		}
		return fmt.Errorf("delete record: %d %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *dnscaleClient) txtRecordExists(zoneID, name, value string) (bool, error) {
	for offset := 0; ; {
		endpoint := fmt.Sprintf("%s/v1/zones/%s/records?offset=%d&limit=1000", c.apiURL, url.PathEscape(zoneID), offset)
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return false, err
		}
		c.setHeaders(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return false, err
		}
		var result struct {
			Data *struct {
				Records    []struct{ Name, Type, Content string } `json:"records"`
				Pagination *struct {
					HasMore bool `json:"has_more"`
				} `json:"pagination"`
			} `json:"data"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil || result.Data == nil || result.Data.Records == nil {
			return false, fmt.Errorf("cannot verify TXT record absence (HTTP %d)", resp.StatusCode)
		}
		for _, record := range result.Data.Records {
			content := record.Content
			if unquoted, err := strconv.Unquote(content); err == nil {
				content = unquoted
			}
			if record.Type == "TXT" && strings.EqualFold(strings.TrimSuffix(record.Name, "."), strings.TrimSuffix(name, ".")) && content == value {
				return true, nil
			}
		}
		hasMore := len(result.Data.Records) == 1000
		if result.Data.Pagination != nil {
			hasMore = result.Data.Pagination.HasMore
		}
		if !hasMore {
			return false, nil
		}
		if len(result.Data.Records) == 0 {
			return false, fmt.Errorf("cannot verify TXT record absence: empty page with has_more=true")
		}
		offset += len(result.Data.Records)
	}
}

func (c *dnscaleClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
}
