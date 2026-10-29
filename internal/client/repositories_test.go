package client

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestRepositoryDelete404RequiresModelAbsence(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		modelStatus              int
		role                     string
		wantError, wantAmbiguous bool
		wantWhoAmI               int32
	}{
		{name: "repository still exists", modelStatus: 200, role: "write", wantError: true},
		{name: "confirmed missing", modelStatus: 404, role: "write", wantWhoAmI: 1},
		{name: "ambiguous missing", modelStatus: 404, role: "read", wantError: true, wantAmbiguous: true, wantWhoAmI: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes, reads, identities atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "DELETE" && r.URL.Path == "/api/repos/delete":
					deletes.Add(1)
					jsonReply(w, 404, `{"error":"not found"}`)
				case r.Method == "GET" && r.URL.Path == "/api/models/alice/test-model":
					reads.Add(1)
					if tc.modelStatus == 200 {
						jsonReply(w, 200, `{"id":"alice/test-model","private":true}`)
					} else {
						jsonReply(w, 404, `{"error":"not found"}`)
					}
				case r.Method == "GET" && r.URL.Path == "/api/whoami-v2":
					identities.Add(1)
					jsonReply(w, 200, `{"type":"user","name":"alice","auth":{"type":"access_token","accessToken":{"role":"`+tc.role+`"}}}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					jsonReply(w, 400, `{"error":"unexpected request"}`)
				}
			})
			err := c.DeleteRepository(context.Background(), model)
			if (err != nil) != tc.wantError {
				t.Errorf("delete result: %v; want error %v", err, tc.wantError)
			}
			if errors.Is(err, ErrAmbiguousAbsence) != tc.wantAmbiguous {
				t.Errorf("absence classification: %v", err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Errorf("delete must return success only after confirmed absence, not expose missing state: %v", err)
			}
			if deletes.Load() != 1 || reads.Load() != 1 || identities.Load() != tc.wantWhoAmI {
				t.Errorf("requests: DELETE=%d model GET=%d whoami=%d; want 1, 1, %d", deletes.Load(), reads.Load(), identities.Load(), tc.wantWhoAmI)
			}
		})
	}
}
