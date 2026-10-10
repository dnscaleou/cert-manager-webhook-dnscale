//go:build conformance

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acmetest "github.com/cert-manager/cert-manager/test/acme"
	"github.com/miekg/dns"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The upstream suite uses real envtest API servers and the actual solver. This
// fixture exposes the documented DNScale HTTP contract and observable DNS TXT
// answers, so PR CI needs neither DNScale credentials nor external DNS writes.
func TestDNS01Conformance(t *testing.T) {
	configureConformance(t)
	var mu sync.Mutex
	records := map[string]map[string]bool{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/zones":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"zones":[{"id":"fixture-zone","name":"example.test"}],"pagination":{"has_more":false}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/zones/fixture-zone/records":
			var record struct {
				Name, Type, Content string
				TTL                 int
			}
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil || record.Type != "TXT" || record.TTL != 300 || record.Content == "" || !strings.HasSuffix(record.Name, ".example.test") {
				http.Error(w, "invalid TXT record", http.StatusBadRequest)
				return
			}
			if records[record.Name] == nil {
				records[record.Name] = map[string]bool{}
			}
			records[record.Name][record.Content] = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/zones/fixture-zone/records/by-name/") && strings.HasSuffix(r.URL.Path, "/TXT"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/zones/fixture-zone/records/by-name/"), "/TXT")
			content := r.URL.Query().Get("content")
			if content == "" {
				http.Error(w, "selected content required", http.StatusBadRequest)
				return
			}
			delete(records[name], content)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	server := &dns.Server{PacketConn: packet, NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		mu.Lock()
		defer mu.Unlock()
		response := new(dns.Msg)
		response.SetReply(r)
		response.Authoritative = true
		for _, question := range r.Question {
			if question.Qtype != dns.TypeTXT {
				continue
			}
			for value := range records[strings.TrimSuffix(question.Name, ".")] {
				response.Answer = append(response.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: []string{value}})
			}
		}
		_ = w.WriteMsg(response)
	})}
	go func() {
		if err := server.ActivateAndServe(); err != nil {
			t.Errorf("DNS fixture: %v", err)
		}
	}()
	<-ready
	t.Cleanup(func() { _ = server.Shutdown() })
	acmetest.NewFixture(&dnscaleSolver{},
		acmetest.SetResolvedZone("example.test."),
		acmetest.SetDNSName("example.test"),
		acmetest.SetManifestPath(conformanceSecret(t, "fixture-token")),
		acmetest.SetConfig(dnscaleConfig{APIUrl: api.URL, APITokenSecretRef: secretKeySelector{Name: "dnscale-api-token", Key: "api-token"}}),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetDNSServer(packet.LocalAddr().String()),
		acmetest.SetUseAuthoritative(false),
		acmetest.SetStrict(true),
		acmetest.SetPollInterval(100*time.Millisecond),
		acmetest.SetPropagationLimit(10*time.Second),
	).RunConformance(t)
	mu.Lock()
	defer mu.Unlock()
	for name, values := range records {
		if len(values) != 0 {
			t.Errorf("conformance left TXT values at %s", name)
		}
	}
}

// Explicitly opt in: this test writes public TXT records in the supplied zone.
func TestDNS01LiveConformance(t *testing.T) {
	if os.Getenv("DNSCALE_LIVE_TESTS") != "true" {
		t.Skip("set DNSCALE_LIVE_TESTS=true with a disposable zone and scoped token file")
	}
	configureConformance(t)
	zone := strings.TrimSuffix(os.Getenv("DNSCALE_TEST_ZONE"), ".")
	if _, ok := dns.IsDomainName(zone + "."); !ok || !strings.Contains(zone, ".") {
		t.Fatal("DNSCALE_TEST_ZONE must name a disposable publicly delegated zone")
	}
	token, err := os.ReadFile(os.Getenv("DNSCALE_TEST_TOKEN_FILE"))
	if err != nil || strings.TrimSpace(string(token)) == "" {
		t.Fatal("DNSCALE_TEST_TOKEN_FILE must contain a scoped API token")
	}
	runID := make([]byte, 12)
	if _, err := rand.Read(runID); err != nil {
		t.Fatal(err)
	}
	name := "conformance-" + hex.EncodeToString(runID) + "." + zone
	apiURL := os.Getenv("DNSCALE_TEST_API_URL")
	if apiURL == "" {
		apiURL = "https://api.dnscale.eu"
	}
	if !strings.HasPrefix(apiURL, "https://") {
		t.Fatal("live tests require an HTTPS API URL")
	}
	acmetest.NewFixture(&dnscaleSolver{},
		acmetest.SetResolvedZone(zone+"."),
		acmetest.SetResolvedFQDN("_acme-challenge."+name+"."),
		acmetest.SetDNSName(name),
		acmetest.SetDNSChallengeKey(hex.EncodeToString(runID)),
		acmetest.SetManifestPath(conformanceSecret(t, strings.TrimSpace(string(token)))),
		acmetest.SetConfig(dnscaleConfig{APIUrl: apiURL, APITokenSecretRef: secretKeySelector{Name: "dnscale-api-token", Key: "api-token"}}),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetStrict(true),
		acmetest.SetPropagationLimit(10*time.Minute),
	).RunConformance(t)
}

func configureConformance(t *testing.T) {
	t.Helper()
	// Upstream's test package enables verbose Kubernetes logging at init. Keep
	// Secret contents out of logs, especially in opt-in live runs.
	if flag.Lookup("v") != nil {
		if err := flag.Set("v", "0"); err != nil {
			t.Fatal(err)
		}
	}
}

func conformanceSecret(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	secret := corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "dnscale-api-token"},
		Data:       map[string][]byte{"api-token": []byte(token)},
	}
	body, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
