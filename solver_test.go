package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func solverForTest(apiURL string) (*dnscaleSolver, *v1alpha1.ChallengeRequest) {
	solver := &dnscaleSolver{client: fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dnscale-api-token", Namespace: "issuer-namespace"},
		Data:       map[string][]byte{"api-token": []byte(" test-token\n")},
	})}
	config, _ := json.Marshal(dnscaleConfig{
		APIUrl:            apiURL,
		APITokenSecretRef: secretKeySelector{Name: "dnscale-api-token", Key: "api-token"},
	})
	return solver, &v1alpha1.ChallengeRequest{
		ResourceNamespace: "issuer-namespace",
		ResolvedZone:      "example.com.",
		ResolvedFQDN:      "_acme-challenge.example.com.",
		Key:               "first-challenge",
		Config:            &extapi.JSON{Raw: config},
	}
}

// Exercise the actual Solver methods, including Kubernetes Secret lookup. The
// API fixtures use the documented wire format, not the client's response type.
func TestSolverLifecyclePreservesOtherTXTValues(t *testing.T) {
	var mu sync.Mutex
	values := map[string]bool{"unrelated-value": true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/zones":
			io.WriteString(w, `{"status":"success","data":{"zones":[{"id":"z1","name":"example.com"}],"pagination":{"has_more":false}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/zones/z1/records":
			var record struct {
				Name, Type, Content string
				TTL                 int
			}
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if record.Name != "_acme-challenge.example.com" || record.Type != "TXT" || record.TTL != 300 || record.Content == "" {
				t.Errorf("invalid record: %+v", record)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			values[record.Content] = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/zones/z1/records/by-name/_acme-challenge.example.com/TXT":
			value := r.URL.Query().Get("content")
			if value == "" {
				t.Error("unfiltered TXT deletion")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !values[value] {
				http.Error(w, `{"status":"error","error":{"code":"INTERNAL_ERROR","message":"Internal server error"}}`, http.StatusInternalServerError)
				return
			}
			delete(values, value)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/zones/z1/records":
			records := []map[string]string{}
			for value := range values {
				records = append(records, map[string]string{"name": "_acme-challenge.example.com.", "type": "TXT", "content": value})
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"records": records, "pagination": map[string]any{"has_more": false}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	solver, first := solverForTest(server.URL)
	second := first.DeepCopy()
	second.Key = "second+challenge&value=escaped/?"
	for _, ch := range []*v1alpha1.ChallengeRequest{first, first, second} {
		if err := solver.Present(ch); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := solver.CleanUp(first); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	preserved := len(values) == 2 && values["unrelated-value"] && values[second.Key]
	mu.Unlock()
	if !preserved {
		t.Fatal("cleanup removed another challenge or unrelated TXT value")
	}
	if err := solver.CleanUp(second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(values) != 1 || !values["unrelated-value"] {
		t.Fatalf("unexpected remaining values: %v", values)
	}
}

func TestCleanupDoesNotHideFailedDeletes(t *testing.T) {
	for _, body := range []string{
		`{"data":{"records":[{"name":"_acme-challenge.example.com.","type":"TXT","content":"\"challenge\""}],"pagination":{"has_more":false}}}`,
		`{"status":"error"}`,
		`{"data":{"records":[],"pagination":{"has_more":true}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					http.Error(w, "backend failure", 500)
					return
				}
				io.WriteString(w, body)
			}))
			defer server.Close()
			if err := newDNScaleClient(server.URL, "test").deleteTXTRecord("z1", "_acme-challenge.example.com", "challenge"); err == nil {
				t.Fatal("failed delete must not be accepted when the value remains or absence cannot be verified")
			}
		})
	}
}

func TestCleanupAbsenceCheckPaginates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, "backend failure", 500)
			return
		}
		if r.URL.Query().Get("offset") == "0" {
			io.WriteString(w, `{"data":{"records":[{"name":"other.example.com.","type":"TXT","content":"other"}],"pagination":{"has_more":true}}}`)
		} else {
			io.WriteString(w, `{"data":{"records":[{"name":"_acme-challenge.example.com.","type":"TXT","content":"challenge"}],"pagination":{"has_more":false}}}`)
		}
	}))
	defer server.Close()
	if err := newDNScaleClient(server.URL, "test").deleteTXTRecord("z1", "_acme-challenge.example.com", "challenge"); err == nil {
		t.Fatal("must find the challenge on later pages before deciding it is absent")
	}
}

func TestSolverRejectsMissingSecretAndEmptyChallenge(t *testing.T) {
	solver, ch := solverForTest("https://api.dnscale.eu")
	ch.ResourceNamespace = "wrong-namespace"
	if err := solver.Present(ch); err == nil || !strings.Contains(err.Error(), "get secret") {
		t.Fatalf("expected Secret lookup error, got %v", err)
	}
	ch.Key = ""
	if err := solver.CleanUp(ch); err == nil || !strings.Contains(err.Error(), "challenge requires") {
		t.Fatalf("expected invalid challenge error, got %v", err)
	}
	if err := newDNScaleClient("https://api.dnscale.eu", "test").deleteTXTRecord("z1", "name", ""); err == nil {
		t.Fatal("empty content must never reach the delete endpoint")
	}
}

func TestSecretValidation(t *testing.T) {
	for _, data := range []map[string][]byte{nil, {"api-token": []byte(" \n")}} {
		solver := &dnscaleSolver{client: fake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "ns"}, Data: data,
		})}
		if _, err := solver.getAPIToken(dnscaleConfig{APITokenSecretRef: secretKeySelector{Name: "token", Key: "api-token"}}, "ns"); err == nil {
			t.Fatal("expected missing or empty token error")
		}
	}
}

func TestWrappedZonePagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("offset") {
		case "0":
			io.WriteString(w, `{"status":"success","data":{"zones":[{"id":"z1","name":"first.example"}],"pagination":{"has_more":true}}}`)
		case "1":
			io.WriteString(w, `{"status":"success","data":{"zones":[{"id":"z2","name":"example.com"}],"pagination":{"has_more":false}}}`)
		default:
			t.Errorf("wrong offset: %s", r.URL.RawQuery)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	id, _, err := newDNScaleClient(server.URL, "test").findZoneByFQDN("example.com.")
	if err != nil || id != "z2" || calls != 2 {
		t.Fatalf("zone lookup: id=%s calls=%d err=%v", id, calls, err)
	}
}

func TestInvalidZoneResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"error"}`, `{"data":{"zones":[],"pagination":{"has_more":true}}}`, `invalid JSON`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			if _, err := newDNScaleClient(server.URL, "test").listZones(); err == nil {
				t.Fatal("expected malformed zone response to fail")
			}
		})
	}
}

func TestClientTimeoutAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/zones" {
			http.Redirect(w, r, "/unexpected", http.StatusFound)
			return
		}
		t.Error("client followed API redirect")
	}))
	defer server.Close()
	client := newDNScaleClient(server.URL, "test")
	if client.http.Timeout <= 0 {
		t.Fatal("API requests need a timeout")
	}
	if _, err := client.listZones(); err == nil {
		t.Fatal("expected redirect to fail")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	client = newDNScaleClient(slow.URL, "test")
	client.http.Timeout = 20 * time.Millisecond
	if _, err := client.listZones(); err == nil {
		t.Fatal("expected hung API request to time out")
	}
}

func TestLoadConfig(t *testing.T) {
	for _, baseURL := range []string{"https://api.dnscale.eu", "http://localhost:8088", "file:///tmp/token", "https://user:pass@example.com", "https://example.com?token=x", ""} {
		t.Run(baseURL, func(t *testing.T) {
			raw := fmt.Sprintf(`{"apiUrl":%q,"apiTokenSecretRef":{"name":"token","key":"api-token"}}`, baseURL)
			_, err := loadConfig(&extapi.JSON{Raw: []byte(raw)})
			valid := baseURL == "https://api.dnscale.eu" || baseURL == "http://localhost:8088"
			if (err == nil) != valid {
				t.Fatalf("unexpected config validation result: %v", err)
			}
		})
	}
	config, err := loadConfig(&extapi.JSON{Raw: []byte(`{"apiTokenSecretRef":{"name":"token","key":"api-token"}}`)})
	if err != nil || config.APIUrl != "https://api.dnscale.eu" {
		t.Fatalf("default API URL: %+v %v", config, err)
	}
}
