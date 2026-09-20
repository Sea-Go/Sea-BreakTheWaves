package datacenter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
)

func TestRepresentAuthorityFixtures(t *testing.T) {
	for _, name := range []string{"dense", "sparse", "token_matrix", "token_matrix_mean"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var f struct {
				Contract representation.Contract `json:"contract"`
				Request  representation.Request  `json:"request"`
				Response representation.Response `json:"response"`
			}
			if err = json.Unmarshal(raw, &f); err != nil {
				t.Fatal(err)
			}
			wrong := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/representations" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("wrong request boundary")
				}
				var q representation.Request
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				if q.ConfigurationID != f.Request.ConfigurationID || q.Role != f.Request.Role || q.Space != f.Request.Space {
					t.Error("request rewritten")
				}
				out := f.Response
				if wrong {
					out.Space = "different-space"
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			client, _ := New(httpclient.Config{BaseURL: server.URL, Token: "fixture"})
			got, err := client.Represent(context.Background(), f.Request, f.Contract, f.Response.Model)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Data) != len(f.Request.Input) {
				t.Fatal("cardinality")
			}
			wrong = true
			if _, err = client.Represent(context.Background(), f.Request, f.Contract, f.Response.Model); err == nil {
				t.Fatal("accepted wrong space")
			}
		})
	}
}
func TestTechnicalClients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/events":
			if r.Header.Get("Idempotency-Key") != "e1" {
				t.Error("event idempotency")
			}
			json.NewEncoder(w).Encode(eventing.Receipt{EventID: "e1", Producer: "p", TechnicalStatus: "accepted", ReceiptID: "receipt"})
		case "/v1/jobs":
			if r.Header.Get("Idempotency-Key") != "o1" {
				t.Error("job idempotency")
			}
			json.NewEncoder(w).Encode(jobs.SubmissionReceipt{ID: "job", OperationID: "o1", Producer: "p", TechnicalStatus: "accepted"})
		case "/v1/jobs/claim":
			w.WriteHeader(204)
		case "/v1/jobs/job/cancel/ack":
			json.NewEncoder(w).Encode(jobs.Job{ID: "job", State: "cancelled"})
		case "/v1/event-consumers/c/events":
			if r.URL.Query().Get("producer") != "p" || r.URL.Query().Get("limit") != "3" {
				t.Error("batch query")
			}
			json.NewEncoder(w).Encode(eventing.Batch{Consumer: "c", Producer: "p"})
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, _ := New(httpclient.Config{BaseURL: server.URL})
	if _, e := c.PublishEvent(context.Background(), eventing.Event{EventID: "e1", Producer: "p"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.SubmitJob(context.Background(), jobs.Submit{OperationID: "o1", Producer: "p"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.ClaimJob(context.Background(), jobs.Claim{}); !errors.Is(e, ErrNoWork) {
		t.Fatalf("claim: %v", e)
	}
	if v, e := c.AcknowledgeCancellation(context.Background(), "job", jobs.Lease{}); e != nil || v.State != "cancelled" {
		t.Fatalf("ack %+v %v", v, e)
	}
	if _, e := c.ReadEvents(context.Background(), "c", "p", 3); e != nil {
		t.Fatal(e)
	}
}

func TestRepresentIdempotencyBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/dense.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Contract representation.Contract `json:"contract"`
		Request  representation.Request  `json:"request"`
		Response representation.Response `json:"response"`
	}
	if err = representation.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var keys []string
	failOnce := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if !representationKeyPattern.MatchString(key) {
			t.Error("invalid call key")
		}
		keys = append(keys, key)
		if key == "stable-operation-1" && failOnce {
			failOnce = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(fixture.Response)
	}))
	defer server.Close()
	c, err := New(httpclient.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = c.Represent(context.Background(), fixture.Request, fixture.Contract, fixture.Response.Model); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatal("independent equal payloads reused a call key", keys)
	}
	if _, err = c.RepresentWithKey(context.Background(), fixture.Request, fixture.Contract, fixture.Response.Model, "stable-operation-1"); err == nil {
		t.Fatal("expected uncertain server failure")
	}
	if len(keys) != 3 {
		t.Fatal("unexpected automatic retry")
	}
	if _, err = c.RepresentWithKey(context.Background(), fixture.Request, fixture.Contract, fixture.Response.Model, "stable-operation-1"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 4 || keys[2] != keys[3] {
		t.Fatal("logical retry changed key", keys)
	}
	for _, bad := range []string{"", "short", "invalid key", "line\nbreak"} {
		if _, err = c.RepresentWithKey(context.Background(), fixture.Request, fixture.Contract, fixture.Response.Model, bad); err == nil {
			t.Fatal("accepted invalid key")
		}
	}
	if len(keys) != 4 {
		t.Fatal("invalid key reached server")
	}
}
