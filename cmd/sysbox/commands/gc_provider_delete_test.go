package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCDeleteDockerRevalidatesIdentityAndOwnership(t *testing.T) {
	for _, kind := range []string{"container", "network"} {
		for _, change := range []string{"none", "id", "managed", "owner_uid", "topology_uid", "resource_uid", "generation", "missing-config", "inspect-error", "absent"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				id := strings.Repeat("a", 64)
				candidate := orphan{Kind: kind, ID: id, OwnerUID: "owner", ResourceUID: "resource", Generation: 2}
				labels := map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": "owner", "sysbox.topology_uid": "owner", "sysbox.resource_uid": "resource", "sysbox.generation": "2"}
				if _, ok := labels["sysbox."+change]; ok {
					labels["sysbox."+change] = "wrong"
				}
				returnedID := id
				if change == "id" {
					returnedID = strings.Repeat("b", 64)
				}
				var requests []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests = append(requests, r.Method+" "+r.URL.Path)
					if r.Method == http.MethodDelete {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if change == "inspect-error" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"unavailable"}`)
						return
					}
					if change == "absent" {
						w.WriteHeader(404)
						fmt.Fprint(w, `{"message":"absent"}`)
						return
					}
					body := map[string]any{"Id": returnedID, "Labels": labels}
					if change != "missing-config" {
						body["Config"] = map[string]any{"Labels": labels}
					}
					if kind == "network" && change == "missing-config" {
						delete(body, "Labels")
					}
					_ = json.NewEncoder(w).Encode(body)
				}))
				defer server.Close()
				t.Setenv("DOCKER_HOST", server.URL)
				t.Setenv("DOCKER_API_VERSION", "1.44")
				t.Setenv("DOCKER_TLS_VERIFY", "")
				err := deleteProviderCandidate(context.Background(), candidate)
				wantError := change != "none" && change != "absent"
				if (err != nil) != wantError {
					t.Errorf("error = %v, want error %v", err, wantError)
				}
				deletes := 0
				for _, request := range requests {
					if strings.HasPrefix(request, "DELETE ") {
						deletes++
					}
				}
				wantDeletes := 0
				if change == "none" {
					wantDeletes = 1
				}
				if deletes != wantDeletes {
					t.Errorf("unsafe delete requests: %v", requests)
				}
				if change == "none" && (len(requests) != 2 || !strings.HasPrefix(requests[0], "GET ") || !strings.HasSuffix(requests[1], "/"+id)) {
					t.Errorf("must inspect before deleting immutable ID: %v", requests)
				}
			})
		}
	}
}

func TestGCDeleteDomainRevalidatesAndPreservesStorage(t *testing.T) {
	for _, change := range []string{"none", "id", "owner_uid", "topology_uid", "resource_uid", "generation", "metadata", "inspect-error", "changed-after-destroy", "name"} {
		t.Run(change, func(t *testing.T) {
			id := "11111111-1111-4111-8111-111111111111"
			candidate := orphan{Kind: "domain", ID: id, OwnerUID: "owner", ResourceUID: "resource", Generation: 2}
			metadata := `<managed xmlns="urn:sysbox:managed" owner_uid="owner" topology_uid="owner" resource_uid="resource" generation="2"/>`
			if change == "id" {
				id = "22222222-2222-4222-8222-222222222222"
			}
			if change == "metadata" {
				metadata = ""
			}
			for _, attr := range []string{"owner_uid", "topology_uid", "resource_uid", "generation"} {
				if change == attr {
					start := strings.Index(metadata, attr+`="`) + len(attr) + 2
					end := start + strings.Index(metadata[start:], `"`)
					metadata = metadata[:start] + "wrong" + metadata[end:]
				}
			}
			if change == "name" {
				candidate.ID = "user-domain"
			}
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			xml := "<domain><uuid>" + id + "</uuid><metadata>" + metadata + "</metadata></domain>"
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GC_TEST_LOG\"\ncase \"$1\" in\ndumpxml)\n"
			if change == "inspect-error" {
				script += "exit 1\n"
			} else {
				script += "if [ -f \"$GC_TEST_LOG.changed\" ]; then printf '%s' '<domain/>'; else printf '%s' \"$GC_TEST_XML\"; fi\n"
			}
			script += ";;\ndestroy)\n"
			if change == "changed-after-destroy" {
				script += ": > \"$GC_TEST_LOG.changed\"\n"
			} else {
				script += ":\n"
			}
			script += ";;\nundefine) :;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("GC_TEST_LOG", log)
			t.Setenv("GC_TEST_XML", xml)
			err := deleteProviderCandidate(context.Background(), candidate)
			if (err != nil) != (change != "none") {
				t.Errorf("error = %v", err)
			}
			calls, _ := os.ReadFile(log)
			if change == "none" {
				want := "dumpxml " + id + "\ndestroy " + id + "\ndumpxml " + id + "\nundefine " + id + "\n"
				if string(calls) != want {
					t.Errorf("calls = %s; want %s", calls, want)
				}
			} else if strings.Contains(string(calls), "undefine") || (change != "changed-after-destroy" && strings.Contains(string(calls), "destroy")) {
				t.Errorf("unsafe domain mutation: %s", calls)
			}
		})
	}
}
