package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
)

// testHTTPBusinessClock checks current-month queries against the supplied UTC
// clock on each request, including the two sides of the month boundary.
func testHTTPBusinessClock(t *testing.T) {
	cases := []struct {
		name       string
		inputTimes []string
		wantMonths []string
		wantStatus int
	}{
		{name: "month is sampled for each request", inputTimes: []string{"2026-10-31T23:59:59.999999Z", "2026-11-01T00:00:00Z"}, wantMonths: []string{"2026-10", "2026-11"}, wantStatus: 200},
		{name: "Shanghai date does not advance UTC month early", inputTimes: []string{"2026-11-01T07:59:59.999999+08:00", "2026-11-01T08:00:00+08:00"}, wantMonths: []string{"2026-10", "2026-11"}, wantStatus: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var instant time.Time
			store := clockLimitStore{Store: acceptingStore()}
			handler, err := httpapi.NewHandler(store, time.Second, 8, func() time.Time { return instant })
			if err != nil {
				t.Fatalf("NewHandler with business clock: %v", err)
			}

			for step, inputTime := range tc.inputTimes {
				instant, err = time.Parse(time.RFC3339Nano, inputTime)
				if err != nil {
					t.Fatalf("Parse clock %q: %v", inputTime, err)
				}

				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", "/customers/acme/limit-status", nil))

				if response.Code != tc.wantStatus {
					t.Fatalf("Current month at %s status=%d; want %d", inputTime, response.Code, tc.wantStatus)
				}

				var snapshot billing.LimitStatus
				if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
					t.Fatalf("Decode current month at %s: %v", inputTime, err)
				}

				if snapshot.Month != tc.wantMonths[step] {
					t.Errorf("Current month at %s=%s; want %s", inputTime, snapshot.Month, tc.wantMonths[step])
				}
			}
		})
	}
}

type clockLimitStore struct{ httpapi.Store }

// MonthlyLimitStatus exposes the transport-selected month without introducing
// storage time calculations into this HTTP boundary test.
func (store clockLimitStore) MonthlyLimitStatus(_ context.Context, customer, month string) (billing.LimitStatus, error) {
	return billing.LimitStatus{CustomerID: customer, Month: month}, nil
}
