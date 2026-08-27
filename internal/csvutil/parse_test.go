package csvutil

import (
	"math/big"
	"testing"
)

// The rule the whole package exists to enforce: an empty field is missing, and
// missing is not zero. A model reading a defaulted zero treats it as a real
// observation.
func TestEmptyFieldsBecomeNullAndNotZero(t *testing.T) {
	if NullText("") != nil {
		t.Error("empty text did not become NULL")
	}
	if v, err := NullFloat32(""); err != nil || v != nil {
		t.Errorf("empty float = %v, %v", v, err)
	}
	if v, err := NullInt16(""); err != nil || v != nil {
		t.Errorf("empty int16 = %v, %v", v, err)
	}
	if v, err := NullInt32(""); err != nil || v != nil {
		t.Errorf("empty int32 = %v, %v", v, err)
	}

	// A genuine zero must survive as a zero, not be confused with absence.
	v, err := NullFloat32("0")
	if err != nil || v == nil || *v != 0 {
		t.Errorf("literal zero = %v, %v", v, err)
	}
}

// IEEE-CIS writes integer codes in float form. Parsing "404.0" with Atoi fails
// and the field would be dropped as unparseable.
func TestIntegerCodesWrittenAsFloats(t *testing.T) {
	v, err := NullInt16("404.0")
	if err != nil || v == nil || *v != 404 {
		t.Errorf("NullInt16(\"404.0\") = %v, %v", v, err)
	}
	w, err := NullInt32("87654.0")
	if err != nil || w == nil || *w != 87654 {
		t.Errorf("NullInt32(\"87654.0\") = %v, %v", w, err)
	}
}

func TestBadNumbersAreReportedRatherThanDefaulted(t *testing.T) {
	if _, err := NullFloat32("not a number"); err == nil {
		t.Error("a malformed float parsed without error")
	}
	if _, err := NullInt32("¤"); err == nil {
		t.Error("a malformed int parsed without error")
	}
}

// Amounts go to NUMERIC exactly. Via float64, 75.887 lands as 75.88699999999999,
// and in both datasets the fractional part of an amount is signal.
func TestAmountsParseExactly(t *testing.T) {
	cases := []struct {
		in   string
		mant int64
		exp  int32
	}{
		{"75.887", 75887, -3},
		{"0.01", 1, -2},
		{"1000", 1000, 0},
		{"-42.50", -4250, -2},
		{"5078345.99", 507834599, -2},
	}
	for _, c := range cases {
		n, err := ParseNumeric(c.in)
		if err != nil {
			t.Fatalf("ParseNumeric(%q): %v", c.in, err)
		}
		if !n.Valid {
			t.Fatalf("ParseNumeric(%q) is not valid", c.in)
		}
		if n.Int.Cmp(big.NewInt(c.mant)) != 0 || n.Exp != c.exp {
			t.Errorf("ParseNumeric(%q) = %v e%d, want %d e%d", c.in, n.Int, n.Exp, c.mant, c.exp)
		}
	}
}

func TestEmptyAmountIsAnErrorRatherThanZero(t *testing.T) {
	if _, err := ParseNumeric(""); err == nil {
		t.Error("an empty amount parsed as something")
	}
	if _, err := ParseNumeric("12,34"); err == nil {
		t.Error("a malformed amount parsed as something")
	}
}
