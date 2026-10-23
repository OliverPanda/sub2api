package money

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Cents is a fixed-point amount in a currency's minor unit. The bridge only
// accepts two decimal places so database, provider and Sub2API amounts can be
// compared without floating point arithmetic.
type Cents int64

func Parse(value string) (Cents, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("amount is empty")
	}
	negative := false
	if strings.HasPrefix(value, "-") {
		negative = true
		value = value[1:]
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return 0, fmt.Errorf("invalid amount %q", value)
	}
	whole := parts[0]
	if whole == "" {
		whole = "0"
	}
	if len(whole) > 15 {
		return 0, fmt.Errorf("amount is too large")
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", value, err)
	}
	var fraction int64
	if len(parts) == 2 {
		raw := parts[1]
		if len(raw) == 0 || len(raw) > 2 {
			return 0, fmt.Errorf("amount supports at most two decimal places")
		}
		if len(raw) == 1 {
			raw += "0"
		}
		fraction, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid amount %q: %w", value, err)
		}
	}
	result := Cents(units*100 + fraction)
	if negative {
		result = -result
	}
	return result, nil
}

// ParseJSON accepts both quoted and numeric JSON amounts. Dujiao-Next has
// returned decimal values as strings in some versions and numbers in others.
func ParseJSON(raw []byte) (Cents, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, fmt.Errorf("amount is empty")
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("invalid JSON amount: %w", err)
		}
		return Parse(value)
	}
	var value json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, fmt.Errorf("invalid JSON amount: %w", err)
	}
	return Parse(value.String())
}

func (c *Cents) UnmarshalJSON(raw []byte) error {
	value, err := ParseJSON(raw)
	if err != nil {
		return err
	}
	*c = value
	return nil
}

func FromInt64(cents int64) Cents { return Cents(cents) }

func (c Cents) Int64() int64 { return int64(c) }

func (c Cents) String() string {
	negative := c < 0
	value := int64(c)
	if negative {
		value = -value
	}
	s := fmt.Sprintf("%d.%02d", value/100, value%100)
	if negative {
		return "-" + s
	}
	return s
}

func (c Cents) JSONNumber() json.Number { return json.Number(c.String()) }

func (c Cents) Positive() bool { return c > 0 }
