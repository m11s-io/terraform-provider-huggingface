package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLostPublisherResponseWithFailedRecoveryStillAmbiguous(t *testing.T) {
	var reads, posts int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reads++
			if reads == 1 {
				jsonReply(w, 200, "[]")
			} else {
				jsonReply(w, 403, `{"error":"denied"}`)
			}
			return
		}
		posts++
		jsonReply(w, 200, "{")
	})
	p, err := c.CreatePublisher(context.Background(), model, githubInput)
	var detail *Error
	if p.ID != "" || !errors.As(err, &detail) || !detail.MayHaveSucceeded || detail.Status != 200 || posts != 1 || reads != 2 {
		t.Fatalf("lost ambiguity: %#v %v posts=%d reads=%d", p, err, posts, reads)
	}
}
func TestPublisherDuplicatePreflightCompletesAllPages(t *testing.T) {
	var posts int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts++
			t.Error("duplicate mutation sent")
			return
		}
		if r.URL.RawQuery == "" {
			w.Header().Set("Link", `<?page=2>; rel="next"`)
		}
		jsonReply(w, 200, "["+strings.ReplaceAll(publisherJSON, "0123456789abcdef01234567", fmt.Sprintf("%024d", len(r.URL.RawQuery)))+"]")
	})
	_, err := c.CreatePublisher(context.Background(), model, githubInput)
	var detail *Error
	if !errors.Is(err, ErrImportRequired) || !errors.As(err, &detail) || len(detail.ImportIDs) != 2 || posts != 0 {
		t.Fatalf("incomplete duplicate detection: %v %d", err, posts)
	}
}
func TestPublisherParent404RequiresConfirmedAbsence(t *testing.T) {
	for _, role := range []string{"write", "read"} {
		t.Run(role, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/whoami-v2" {
					jsonReply(w, 200, `{"type":"user","name":"alice","auth":{"type":"access_token","accessToken":{"role":"`+role+`"}}}`)
					return
				}
				jsonReply(w, 404, `{"error":"missing"}`)
			})
			_, err := c.ListPublishers(context.Background(), model)
			if errors.Is(err, ErrNotFound) != (role == "write") {
				t.Fatalf("parent absence: %v", err)
			}
			if role == "read" && !errors.Is(err, ErrAmbiguousAbsence) {
				t.Fatal(err)
			}
		})
	}
}
func TestPublisherTruncationAndBounds(t *testing.T) {
	for _, header := range []string{"X-Total-Count", "X-Has-More", "Content-Range", "X-Truncated"} {
		t.Run(header, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				value := "true"
				if header == "X-Total-Count" {
					value = "1"
				}
				w.Header().Set(header, value)
				jsonReply(w, 200, "[]")
			})
			if _, err := c.ListPublishers(context.Background(), model); err == nil {
				t.Fatal("truncation accepted")
			}
		})
	}
	var pages int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Link", fmt.Sprintf("<?page=%d>; rel=next", pages+1))
		jsonReply(w, 200, "[]")
	})
	if _, err := c.ListPublishers(context.Background(), model); err == nil || pages != 100 {
		t.Fatalf("page bound: %d %v", pages, err)
	}
}
func TestForeignArbitraryClaimsAndManagedDriftRemainVisible(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 200, `[{"_id":"aaaaaaaaaaaaaaaaaaaaaaaa","issuerUrl":"https://foreign.example","claims":{"numeric":{"kind":"other","value":42},"array":[1,2]}},{"_id":"bbbbbbbbbbbbbbbbbbbbbbbb","issuerUrl":"https://token.actions.githubusercontent.com","claims":{"ref":{"kind":"unknown","value":false}}}]`)
	})
	list, err := c.ListPublishers(context.Background(), model)
	if err != nil || len(list) != 2 || list[0].Claims["numeric"].Value != float64(42) || string(list[0].RawClaims["array"]) != "[1,2]" || list[1].Claims["ref"].Value != false {
		t.Fatalf("foreign data/drift discarded: %#v %v", list, err)
	}
}
func TestRetryAfterWaitAndCancellation(t *testing.T) {
	var calls int
	var first time.Time
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			jsonReply(w, 429, "{}")
			return
		}
		if time.Since(first) < time.Second {
			t.Error("retry earlier than requested")
		}
		jsonReply(w, 200, `{"id":"alice/test-model","private":true}`)
	})
	c.timeout = 3 * time.Second
	c.http.Timeout = 3 * time.Second
	if _, err := c.GetRepository(context.Background(), model); err != nil || calls != 2 {
		t.Fatalf("wait: %d %v", calls, err)
	}
	calls = 0
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.GetRepository(ctx, model)
	if err == nil || calls != 1 {
		t.Fatalf("retry cancellation: %d %v", calls, err)
	}
}
func TestTransportFailuresAreSanitizedAndReadsRetry(t *testing.T) {
	c, _ := New(Config{Endpoint: "https://example.com", Token: secret})
	var calls int
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("unsafe URL " + secret) })
	_, err := c.GetRepository(context.Background(), model)
	if err == nil || strings.Contains(err.Error(), secret) || calls != 3 {
		t.Fatalf("unsafe/unbounded transport: %d %v", calls, err)
	}
}
func TestSameOriginReadRedirectAndMutationRedirectRejection(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirected" {
			jsonReply(w, 200, `{"id":"alice/test-model","private":true}`)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	if _, err := c.GetRepository(context.Background(), model); err != nil || calls != 2 {
		t.Fatalf("same origin read: %d %v", calls, err)
	}
	calls = 0
	_, err := c.CreateRepository(context.Background(), model)
	var detail *Error
	if err == nil || !errors.As(err, &detail) || !detail.MayHaveSucceeded || calls != 3 {
		t.Fatalf("mutation redirect replay: calls=%d %v", calls, err)
	}
}
func TestDeleteDoesNotRetryOrTreatAmbiguous404AsSuccess(t *testing.T) {
	for _, publisher := range []bool{false, true} {
		var mutations int
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				mutations++
			}
			jsonReply(w, 404, `{"error":"missing"}`)
		})
		var err error
		if publisher {
			err = c.DeletePublisher(context.Background(), model, "0123456789abcdef01234567")
		} else {
			err = c.DeleteRepository(context.Background(), model)
		}
		if !errors.Is(err, ErrAmbiguousAbsence) || mutations != 1 {
			t.Fatalf("unsafe delete: %v %d", err, mutations)
		}
	}
}
func TestRequestIDAndMalformedCreateCannotLeakToken(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", secret)
		jsonReply(w, 200, "{")
	})
	_, err := c.CreateRepository(context.Background(), model)
	var detail *Error
	if !errors.As(err, &detail) || detail.Status != 200 || !detail.MayHaveSucceeded || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe malformed create: %v", err)
	}
}
func TestResponseBodyReadErrorKeepsStatus(t *testing.T) {
	c, _ := New(Config{Endpoint: "https://example.com", Token: secret})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"read-error"}}, Body: io.NopCloser(brokenReader{}), Request: r}, nil
	})
	_, err := c.GetRepository(context.Background(), model)
	var detail *Error
	if !errors.As(err, &detail) || detail.Status != 200 || detail.RequestID != "read-error" || strings.Contains(err.Error(), secret) {
		t.Fatalf("read failure lost metadata: %v", err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New(secret) }

func TestUnexpectedSuccessfulMutationStatusIsAmbiguous(t *testing.T) {
	var mutations int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			mutations++
			jsonReply(w, 201, publisherJSON)
			return
		}
		jsonReply(w, 200, "[]")
	})
	p, err := c.CreatePublisher(context.Background(), model, githubInput)
	var detail *Error
	if p.ID != "" || !errors.As(err, &detail) || !detail.MayHaveSucceeded || detail.Status != 201 || mutations != 1 {
		t.Fatalf("unexpected success was reported as definite failure or adopted: %#v %v mutations=%d", p, err, mutations)
	}
}
