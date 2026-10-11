package billing

import "time"

// ParseMonth requires a canonical UTC calendar month, including supported years.
func ParseMonth(value string) (time.Time, error) {
	month, err := time.Parse("2006-01", value)
	if err != nil || month.Format("2006-01") != value {
		return time.Time{}, &ValidationError{Field: "month", Message: "Use a UTC calendar month as YYYY-MM."}
	}

	if err := ValidateTime("month", month); err != nil {
		return time.Time{}, err
	}

	return month, nil
}
