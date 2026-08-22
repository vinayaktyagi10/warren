// Package csvutil holds the field parsing shared by the dataset loaders.
//
// The rule both loaders follow: an empty field means "missing" and must reach
// Postgres as NULL, never as a zero. A model reading a defaulted zero treats it
// as a real observation, which quietly corrupts anything learned from it.
package csvutil

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

func NullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func NullFloat32(s string) (*float32, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return nil, err
	}
	f := float32(v)
	return &f, nil
}

// NullInt16 parses codes that a CSV may write in float form ("404.0" for 404).
func NullInt16(s string) (*int16, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	i := int16(v)
	return &i, nil
}

func NullInt32(s string) (*int32, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	i := int32(v)
	return &i, nil
}

// ParseNumeric converts a decimal string to an exact NUMERIC, carried as
// mantissa and exponent. Going via float64 would let 75.887 land as
// 75.88699999999999, and in both datasets the fractional part of an amount is
// signal rather than noise.
func ParseNumeric(s string) (pgtype.Numeric, error) {
	if s == "" {
		return pgtype.Numeric{}, errors.New("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, fracPart, _ := strings.Cut(s, ".")
	mant, ok := new(big.Int).SetString(intPart+fracPart, 10)
	if !ok {
		return pgtype.Numeric{}, fmt.Errorf("bad numeric %q", s)
	}
	if neg {
		mant.Neg(mant)
	}
	return pgtype.Numeric{Int: mant, Exp: int32(-len(fracPart)), Valid: true}, nil
}
