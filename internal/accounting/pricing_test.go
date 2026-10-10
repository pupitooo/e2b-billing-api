package accounting_test

import (
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/usage"
)

// Rate selects a historical customer override or default for one usage interval,
// returns its exact charge and UTC month, and rejects unsupported or ambiguous input.
func TestRate(t *testing.T) {
	defaultOctober := accounting.PriceVersion{
		ID: "default-oct", CustomerID: "", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-01T00:00:00Z"), PricePerMillionCents: 5,
	}
	acmeOctober := accounting.PriceVersion{
		ID: "acme-oct", CustomerID: "acme", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-01T00:00:00Z"), PricePerMillionCents: 4,
	}
	defaultMidOctober := accounting.PriceVersion{
		ID: "default-mid-oct", CustomerID: "", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 6,
	}
	// Deliberately unsorted: catalog order must not determine the selected price.
	prices := []accounting.PriceVersion{defaultMidOctober, acmeOctober, defaultOctober}
	withAcmeChange := append([]accounting.PriceVersion{{
		ID: "acme-new", CustomerID: "acme", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 3,
	}}, prices...)
	withCustomerChanges := append([]accounting.PriceVersion{{
		ID: "cyberdyne-new", CustomerID: "cyberdyne", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 2,
	}}, withAcmeChange...)
	withDuplicate := append([]accounting.PriceVersion{defaultMidOctober}, prices...)

	tests := []struct {
		name                     string
		customerID               string
		metric                   string
		schemaVersion            int32
		periodStart              string
		periodEnd                string
		units                    int64
		prices                   []accounting.PriceVersion
		wantPriceID              string
		wantPricePerMillionCents int64
		wantUsageMonth           string
		wantChargeTicks          string
		wantError                bool
		wantErrorMessage         string
	}{
		{
			name:                     "Acme October usage selects its customer override",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-10T12:00:00Z",
			periodEnd:                "2026-10-10T13:00:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "400000000",
			wantError:                false,
		},
		{
			name:                     "later default changes never replace Acme override",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-11-03T12:00:00Z",
			periodEnd:                "2026-11-03T13:00:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-11-01T00:00:00Z",
			wantChargeTicks:          "400000000",
			wantError:                false,
		},
		{
			name:                     "Cyberdyne usage before the change selects the old default",
			customerID:               "cyberdyne",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-10T12:00:00Z",
			periodEnd:                "2026-10-10T13:00:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "default-oct",
			wantPricePerMillionCents: 5,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "500000000",
			wantError:                false,
		},
		{
			name:                     "Cyberdyne usage at the change selects the new default",
			customerID:               "cyberdyne",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-15T00:00:00Z",
			periodEnd:                "2026-10-15T01:00:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "default-mid-oct",
			wantPricePerMillionCents: 6,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "600000000",
			wantError:                false,
		},
		{
			name:             "default price change inside the interval is rejected",
			customerID:       "cyberdyne",
			metric:           "cpu_seconds",
			schemaVersion:    1,
			periodStart:      "2026-10-14T23:30:00Z",
			periodEnd:        "2026-10-15T00:30:00Z",
			units:            1_000,
			prices:           prices,
			wantError:        true,
			wantErrorMessage: "usage interval crosses a price version boundary",
		},
		{
			name:             "one microsecond across the price change is rejected",
			customerID:       "cyberdyne",
			metric:           "cpu_seconds",
			schemaVersion:    1,
			periodStart:      "2026-10-14T23:59:00Z",
			periodEnd:        "2026-10-15T00:00:00.000001Z",
			units:            1_000_000,
			prices:           prices,
			wantError:        true,
			wantErrorMessage: "usage interval crosses a price version boundary",
		},
		{
			name:                     "price change at the excluded interval end preserves the old price",
			customerID:               "cyberdyne",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-14T23:30:00Z",
			periodEnd:                "2026-10-15T00:00:00Z",
			units:                    1_000,
			prices:                   prices,
			wantPriceID:              "default-oct",
			wantPricePerMillionCents: 5,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "5000",
			wantError:                false,
		},
		{
			name:                     "Acme override masks a default change inside the interval",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-14T23:30:00Z",
			periodEnd:                "2026-10-15T00:30:00Z",
			units:                    1_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "4000",
			wantError:                false,
		},
		{
			name:          "customer override change inside the interval is rejected",
			customerID:    "acme",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-14T23:30:00Z",
			periodEnd:     "2026-10-15T00:30:00Z",
			units:         1_000,
			prices:        withAcmeChange,
			wantError:     true,
		},
		{
			name:          "customer override beginning inside a default interval is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-14T23:30:00Z",
			periodEnd:     "2026-10-15T00:30:00Z",
			units:         1_000,
			prices:        withCustomerChanges,
			wantError:     true,
		},
		{
			name:          "missing catalog is rejected rather than priced as free",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices:        nil,
			wantError:     true,
		},
		{
			name:          "duplicate owner and effective time are rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices:        withDuplicate,
			wantError:     true,
		},
		{
			name:          "negative relevant catalog price is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices: []accounting.PriceVersion{{
				ID: "bad", Metric: "cpu_seconds",
				EffectiveFrom: instant(t, "2026-10-10T12:00:00Z"), PricePerMillionCents: -1,
			}},
			wantError: true,
		},
		{
			name:          "missing relevant price identifier is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices: []accounting.PriceVersion{{
				ID: "", Metric: "cpu_seconds",
				EffectiveFrom: instant(t, "2026-10-10T12:00:00Z"), PricePerMillionCents: 5,
			}},
			wantError: true,
		},
		{
			name:          "missing relevant price effective time is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices: []accounting.PriceVersion{{
				ID: "bad", Metric: "cpu_seconds",
				EffectiveFrom: time.Time{}, PricePerMillionCents: 5,
			}},
			wantError: true,
		},
		{
			name:          "unsupported event schema is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 2,
			periodStart:   "2026-10-10T12:00:00Z",
			periodEnd:     "2026-10-10T13:00:00Z",
			units:         1,
			prices:        prices,
			wantError:     true,
		},
		{
			name:             "usage spanning UTC months is rejected",
			customerID:       "cyberdyne",
			metric:           "cpu_seconds",
			schemaVersion:    1,
			periodStart:      "2026-10-31T23:30:00Z",
			periodEnd:        "2026-11-01T00:30:00Z",
			units:            1,
			prices:           prices,
			wantError:        true,
			wantErrorMessage: "usage interval must fit within one UTC month",
		},
		{
			name:             "two-minute interval spanning UTC months is rejected",
			customerID:       "acme",
			metric:           "cpu_seconds",
			schemaVersion:    1,
			periodStart:      "2026-10-31T23:59:00Z",
			periodEnd:        "2026-11-01T00:01:00Z",
			units:            100_000_000,
			prices:           prices,
			wantError:        true,
			wantErrorMessage: "usage interval must fit within one UTC month",
		},
		{
			name:                     "interval ending exactly at the UTC month boundary retains October",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-31T23:59:00Z",
			periodEnd:                "2026-11-01T00:00:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "400000000",
			wantError:                false,
		},
		{
			name:                     "interval starting exactly at the UTC month boundary belongs to November",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-11-01T00:00:00Z",
			periodEnd:                "2026-11-01T00:01:00Z",
			units:                    100_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-11-01T00:00:00Z",
			wantChargeTicks:          "400000000",
			wantError:                false,
		},
		{
			name:          "usage before any applicable price is rejected",
			customerID:    "cyberdyne",
			metric:        "cpu_seconds",
			schemaVersion: 1,
			periodStart:   "2026-09-30T12:00:00Z",
			periodEnd:     "2026-09-30T13:00:00Z",
			units:         1,
			prices:        prices,
			wantError:     true,
		},
		{
			name:                     "Acme second October event costs eight dollars",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-20T12:00:00Z",
			periodEnd:                "2026-10-20T13:00:00Z",
			units:                    200_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "800000000",
			wantError:                false,
		},
		{
			name:                     "late Acme October usage retains its original price and month",
			customerID:               "acme",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-30T12:00:00Z",
			periodEnd:                "2026-10-30T13:00:00Z",
			units:                    50_000_000,
			prices:                   prices,
			wantPriceID:              "acme-oct",
			wantPricePerMillionCents: 4,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "200000000",
			wantError:                false,
		},
		{
			name:                     "Cyberdyne first assignment event retains fractional cents",
			customerID:               "cyberdyne",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-10T12:00:00Z",
			periodEnd:                "2026-10-10T13:00:00Z",
			units:                    123_456_789,
			prices:                   prices,
			wantPriceID:              "default-oct",
			wantPricePerMillionCents: 5,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "617283945",
			wantError:                false,
		},
		{
			name:                     "Cyberdyne second assignment event uses the new default",
			customerID:               "cyberdyne",
			metric:                   "cpu_seconds",
			schemaVersion:            1,
			periodStart:              "2026-10-20T12:00:00Z",
			periodEnd:                "2026-10-20T13:00:00Z",
			units:                    200_000_000,
			prices:                   prices,
			wantPriceID:              "default-mid-oct",
			wantPricePerMillionCents: 6,
			wantUsageMonth:           "2026-10-01T00:00:00Z",
			wantChargeTicks:          "1200000000",
			wantError:                false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := usage.Event{
				Source: "accounting-test", EventID: "event", SandboxID: "sandbox",
				CustomerID: tt.customerID, Metric: tt.metric, SchemaVersion: tt.schemaVersion,
				PeriodStart: instant(t, tt.periodStart), PeriodEnd: instant(t, tt.periodEnd), Units: tt.units,
			}
			got, err := accounting.Rate(event, tt.prices)
			if tt.wantError {
				if err == nil {
					t.Fatalf("Rate(%+v, %+v) error = nil; want an error", event, tt.prices)
				}

				if tt.wantErrorMessage != "" && err.Error() != tt.wantErrorMessage {
					t.Errorf("Rate(%+v, %+v) error = %q; want %q", event, tt.prices, err, tt.wantErrorMessage)
				}

				return
			}

			if err != nil {
				t.Fatalf("Rate(%+v, %+v) error = %v; want nil", event, tt.prices, err)
			}

			if got.Price.ID != tt.wantPriceID {
				t.Errorf("Rate price ID = %q; want %q", got.Price.ID, tt.wantPriceID)
			}

			if got.Price.PricePerMillionCents != tt.wantPricePerMillionCents {
				t.Errorf("Rate price = %d cents per million; want %d", got.Price.PricePerMillionCents, tt.wantPricePerMillionCents)
			}

			if !got.UsageMonth.Equal(instant(t, tt.wantUsageMonth)) {
				t.Errorf("Rate usage month = %s; want %s", got.UsageMonth, tt.wantUsageMonth)
			}

			if got.Charge.Ticks().String() != tt.wantChargeTicks {
				t.Errorf("Rate charge = %s ticks; want %s ticks", got.Charge.Ticks(), tt.wantChargeTicks)
			}
		})
	}
}
