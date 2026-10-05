package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// --- dnscaleClient tests ---

func newTestServer(t *testing.T, mux *http.ServeMux) (*dnscaleClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(mux)
	client := newDNScaleClient(server.URL, "test-token")
	return client, server
}

// --- findZoneByFQDN tests ---

func TestFindZoneByFQDN_ExactMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "z1", Name: "example.com"},
			},
		})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	id, name, err := client.findZoneByFQDN("example.com.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "z1" {
		t.Errorf("expected z1, got %s", id)
	}
	if name != "example.com" {
		t.Errorf("expected example.com, got %s", name)
	}
}

func TestFindZoneByFQDN_SubdomainWalkUp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "z1", Name: "example.com"},
			},
		})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	id, _, err := client.findZoneByFQDN("_acme-challenge.sub.example.com.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "z1" {
		t.Errorf("expected z1, got %s", id)
	}
}

func TestFindZoneByFQDN_CaseInsensitive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "z1", Name: "Example.COM"},
			},
		})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	id, _, err := client.findZoneByFQDN("example.com.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "z1" {
		t.Errorf("expected z1, got %s", id)
	}
}

func TestFindZoneByFQDN_NoMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "z1", Name: "other.com"},
			},
		})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	_, _, err := client.findZoneByFQDN("example.com.")
	if err == nil {
		t.Fatal("expected error for no matching zone")
	}
}

func TestFindZoneByFQDN_EmptyZones(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{Zones: []zoneEntry{}})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	_, _, err := client.findZoneByFQDN("example.com.")
	if err == nil {
		t.Fatal("expected error for empty zones")
	}
}

// --- listZones tests ---

func TestListZones_SinglePage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "z1", Name: "a.com"},
				{ID: "z2", Name: "b.com"},
			},
		})
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	zones, err := client.listZones()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zones) != 2 {
		t.Errorf("expected 2 zones, got %d", len(zones))
	}
}

func TestListZones_Pagination(t *testing.T) {
	mux := http.NewServeMux()
	callCount := 0

	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		offset := r.URL.Query().Get("offset")
		if offset == "" || offset == "0" {
			zones := make([]zoneEntry, 100)
			for i := range zones {
				zones[i] = zoneEntry{ID: "z-filler", Name: "filler.com"}
			}
			json.NewEncoder(w).Encode(zonesResponse{Zones: zones})
		} else {
			json.NewEncoder(w).Encode(zonesResponse{
				Zones: []zoneEntry{{ID: "z-last", Name: "last.com"}},
			})
		}
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	zones, err := client.listZones()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zones) != 101 {
		t.Errorf("expected 101 zones, got %d", len(zones))
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 API calls, got %d", callCount)
	}
}

func TestListZones_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	_, err := client.listZones()
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

// --- createTXTRecord tests ---

func TestCreateTXTRecord_Success(t *testing.T) {
	mux := http.NewServeMux()

	var received map[string]any
	mux.HandleFunc("/v1/zones/z1/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}

		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", auth)
		}

		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.createTXTRecord("z1", "_acme-challenge.example.com", "token-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received["name"] != "_acme-challenge.example.com" {
		t.Errorf("expected name _acme-challenge.example.com, got %v", received["name"])
	}
	if received["type"] != "TXT" {
		t.Errorf("expected type TXT, got %v", received["type"])
	}
	if received["content"] != "token-value" {
		t.Errorf("expected content token-value, got %v", received["content"])
	}
	if received["ttl"] != float64(300) {
		t.Errorf("expected documented minimum TTL 300, got %v", received["ttl"])
	}
}

func TestCreateTXTRecord_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones/z1/records", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden"}`))
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.createTXTRecord("z1", "test", "val")
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestCreateTXTRecord_Accepts200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones/z1/records", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.createTXTRecord("z1", "test", "val")
	if err != nil {
		t.Fatalf("expected 200 to be accepted: %v", err)
	}
}

// --- deleteTXTRecord tests ---

func TestDeleteTXTRecord_Success(t *testing.T) {
	mux := http.NewServeMux()

	var receivedContent string
	var receivedPath string
	mux.HandleFunc("/v1/zones/z1/records/by-name/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		receivedPath = r.URL.Path
		receivedContent = r.URL.Query().Get("content")
		w.WriteHeader(http.StatusOK)
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.deleteTXTRecord("z1", "_acme-challenge.example.com", "token-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedContent != "token-value" {
		t.Errorf("expected content token-value, got %s", receivedContent)
	}
	if receivedPath == "" {
		t.Error("expected path to be recorded")
	}
}

func TestDeleteTXTRecord_Accepts204(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones/z1/records/by-name/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.deleteTXTRecord("z1", "test", "val")
	if err != nil {
		t.Fatalf("expected 204 to be accepted: %v", err)
	}
}

func TestDeleteTXTRecord_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/zones/z1/records/by-name/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not_found"}`))
	})

	client, server := newTestServer(t, mux)
	defer server.Close()

	err := client.deleteTXTRecord("z1", "test", "val")
	if err != nil {
		t.Fatalf("cleanup must tolerate an already absent record: %v", err)
	}
}

// --- setHeaders tests ---

func TestSetHeaders(t *testing.T) {
	client := newDNScaleClient("https://api.test.com", "my-token")

	req, _ := http.NewRequest(http.MethodGet, "https://api.test.com/v1/zones", nil)
	client.setHeaders(req)

	if req.Header.Get("Authorization") != "Bearer my-token" {
		t.Errorf("expected Bearer my-token, got %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("expected application/json, got %s", req.Header.Get("Accept"))
	}
}

// --- loadConfig tests ---

func TestLoadConfig_Nil(t *testing.T) {
	cfg, err := loadConfig(nil)
	if err == nil {
		t.Fatal("expected missing credentials configuration to fail")
	}
	if cfg.APIUrl != "https://api.dnscale.eu" {
		t.Errorf("expected default API URL, got %s", cfg.APIUrl)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	if _, err := loadConfig(&extapi.JSON{Raw: []byte(`{"invalid"`)}); err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}

// --- Integration-style tests ---

func TestClient_CreateFlow(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "zone-abc", Name: "example.com"},
			},
		})
	})

	var recordBody map[string]any
	mux.HandleFunc("/v1/zones/zone-abc/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		json.NewDecoder(r.Body).Decode(&recordBody)
		w.WriteHeader(http.StatusCreated)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := newDNScaleClient(server.URL, "test-token")
	zoneID, _, err := client.findZoneByFQDN("example.com.")
	if err != nil {
		t.Fatalf("findZoneByFQDN failed: %v", err)
	}

	err = client.createTXTRecord(zoneID, "_acme-challenge.example.com", "challenge-value")
	if err != nil {
		t.Fatalf("createTXTRecord failed: %v", err)
	}

	if recordBody["name"] != "_acme-challenge.example.com" {
		t.Errorf("expected _acme-challenge.example.com, got %v", recordBody["name"])
	}
	if recordBody["type"] != "TXT" {
		t.Errorf("expected TXT, got %v", recordBody["type"])
	}
}

func TestClient_DeleteFlow(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(zonesResponse{
			Zones: []zoneEntry{
				{ID: "zone-abc", Name: "example.com"},
			},
		})
	})

	var deletedContent string
	mux.HandleFunc("/v1/zones/zone-abc/records/by-name/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		deletedContent = r.URL.Query().Get("content")
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := newDNScaleClient(server.URL, "test-token")
	zoneID, _, err := client.findZoneByFQDN("example.com.")
	if err != nil {
		t.Fatalf("findZoneByFQDN failed: %v", err)
	}

	err = client.deleteTXTRecord(zoneID, "_acme-challenge.example.com", "challenge-value")
	if err != nil {
		t.Fatalf("deleteTXTRecord failed: %v", err)
	}

	if deletedContent != "challenge-value" {
		t.Errorf("expected challenge-value, got %s", deletedContent)
	}
}
