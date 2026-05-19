package azure

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type fakeCred struct{}

func (fakeCred) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "fake", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mkResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func newTestClient(rt http.RoundTripper) *Client {
	return NewClientWithCred(fakeCred{}, rt, 1000) // high rps so tests don't sleep
}

func TestQueryPagination(t *testing.T) {
	page1 := `{"properties":{"nextLink":"https://management.azure.com/page2","columns":[{"name":"Cost","type":"Number"},{"name":"ServiceName","type":"String"}],"rows":[[10.5,"A"],[7.0,"B"]]}}`
	page2 := `{"properties":{"nextLink":"","columns":[{"name":"Cost","type":"Number"},{"name":"ServiceName","type":"String"}],"rows":[[3.0,"C"]]}}`

	var calls int32
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 && !strings.Contains(r.URL.String(), "/Microsoft.CostManagement/query") {
			t.Errorf("first url=%s", r.URL)
		}
		if n == 2 && !strings.Contains(r.URL.String(), "page2") {
			t.Errorf("nextLink not followed: %s", r.URL)
		}
		if n == 1 {
			return mkResp(200, page1), nil
		}
		return mkResp(200, page2), nil
	})

	rows, err := newTestClient(rt).Query(context.Background(), QueryOptions{
		Scope: "subscriptions/abc", GroupBy: []string{"ServiceName"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Float("Cost") != 10.5 || rows[2].String("ServiceName") != "C" {
		t.Errorf("rows=%+v", rows)
	}
}

func TestQuery429RefreshesTokenPerAttempt(t *testing.T) {
	body := `{"properties":{"nextLink":"","columns":[{"name":"Cost","type":"Number"}],"rows":[[1.0]]}}`
	var calls int32
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer fake" {
			t.Errorf("missing bearer on attempt: %v", r.Header)
		}
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			r := mkResp(429, "throttled")
			r.Header.Set("Retry-After", "1")
			return r, nil
		}
		return mkResp(200, body), nil
	})

	rows, err := newTestClient(rt).Query(context.Background(), QueryOptions{Scope: "subscriptions/abc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("rows=%+v calls=%d", rows, atomic.LoadInt32(&calls))
	}
}

func TestQueryCustomTimeframeAndFilter(t *testing.T) {
	body := `{"properties":{"nextLink":"","columns":[],"rows":[]}}`
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		buf, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(buf))
		s := string(buf)
		if !strings.Contains(s, `"timeframe":"Custom"`) {
			t.Errorf("want Custom in: %s", s)
		}
		if !strings.Contains(s, `"ServiceName"`) {
			t.Errorf("want ServiceName filter in: %s", s)
		}
		return mkResp(200, body), nil
	})
	_, err := newTestClient(rt).Query(context.Background(), QueryOptions{
		Scope: "subscriptions/abc", Timeframe: "Custom",
		From:          time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		FilterService: "Bandwidth",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueryGranularityDaily(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		buf, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(buf))
		if !strings.Contains(string(buf), `"granularity":"Daily"`) {
			t.Errorf("want daily, got: %s", string(buf))
		}
		return mkResp(200, `{"properties":{"nextLink":"","columns":[],"rows":[]}}`), nil
	})
	_, err := newTestClient(rt).Query(context.Background(), QueryOptions{
		Scope: "subscriptions/abc", Granularity: "Daily",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHardErrorNoRetry(t *testing.T) {
	var calls int32
	rt := rtFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return mkResp(400, "bad"), nil
	})
	_, err := newTestClient(rt).Query(context.Background(), QueryOptions{Scope: "subscriptions/abc"})
	if err == nil || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, atomic.LoadInt32(&calls))
	}
}

func TestScopeRequired(t *testing.T) {
	_, err := newTestClient(nil).Query(context.Background(), QueryOptions{})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("want scope err, got %v", err)
	}
}

func TestNetworkErrorBubbles(t *testing.T) {
	rt := rtFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	err := newTestClient(rt).Get(context.Background(), "https://management.azure.com/x", nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %v", err)
	}
}

func TestBackoffFromRetryAfter(t *testing.T) {
	def := 5 * time.Second
	if backoffFromRetryAfter("", def) != def {
		t.Error("empty default broken")
	}
	if backoffFromRetryAfter("12", def) != 12*time.Second {
		t.Error("seconds parse broken")
	}
	if backoffFromRetryAfter("garbage", def) != def {
		t.Error("garbage fallback broken")
	}
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	got := backoffFromRetryAfter(future, def)
	if got < 30*time.Second || got > 2*time.Minute {
		t.Errorf("http-date: got %v", got)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("untrimmed broken")
	}
	if !strings.HasSuffix(truncate("helloworld", 5), "…") {
		t.Error("trim suffix broken")
	}
}
