package envcfg_test

import (
	"math"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// setenv is a test helper that sets key to val for the duration of the test.
func setenv(t *testing.T, key, val string) {
	t.Helper()
	t.Setenv(key, val)
}

// ---- Float -----------------------------------------------------------------

func TestFloat_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_FLOAT_UNSET_XYZ"
	if got := envcfg.Float(key, 3.14); got != 3.14 {
		t.Fatalf("want 3.14, got %v", got)
	}
}

func TestFloat_Override_WhenSet(t *testing.T) {
	const key = "TEST_ENVCFG_FLOAT_SET"
	setenv(t, key, "2.71")
	if got := envcfg.Float(key, 0); got != 2.71 {
		t.Fatalf("want 2.71, got %v", got)
	}
}

func TestFloat_Default_OnBadValue(t *testing.T) {
	const key = "TEST_ENVCFG_FLOAT_BAD"
	setenv(t, key, "not-a-float")
	if got := envcfg.Float(key, 1.0); got != 1.0 {
		t.Fatalf("want default 1.0, got %v", got)
	}
}

func TestFloat_RejectsNaN(t *testing.T) {
	const key = "TEST_ENVCFG_FLOAT_NAN"
	setenv(t, key, "NaN")
	if got := envcfg.Float(key, 5.0); got != 5.0 {
		t.Fatalf("want default 5.0 for NaN input, got %v", got)
	}
}

func TestFloat_RejectsInf(t *testing.T) {
	for _, val := range []string{"+Inf", "-Inf", "Inf"} {
		val := val
		t.Run(val, func(t *testing.T) {
			const key = "TEST_ENVCFG_FLOAT_INF"
			setenv(t, key, val)
			if got := envcfg.Float(key, 9.0); got != 9.0 {
				t.Fatalf("want default 9.0 for %q input, got %v", val, got)
			}
		})
	}
}

// ---- FloatRange ------------------------------------------------------------

func TestFloatRange_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_UNSET"
	if got := envcfg.FloatRange(key, 0.5, 0, 1); got != 0.5 {
		t.Fatalf("want 0.5, got %v", got)
	}
}

func TestFloatRange_Override_InRange(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_IN"
	setenv(t, key, "0.7")
	if got := envcfg.FloatRange(key, 0.5, 0, 1); got != 0.7 {
		t.Fatalf("want 0.7, got %v", got)
	}
}

func TestFloatRange_Default_BelowMin(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_BELOW"
	setenv(t, key, "-1")
	if got := envcfg.FloatRange(key, 0.5, 0, 1); got != 0.5 {
		t.Fatalf("want default 0.5 for below-min, got %v", got)
	}
}

func TestFloatRange_Default_AboveMax(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_ABOVE"
	setenv(t, key, "2.0")
	if got := envcfg.FloatRange(key, 0.5, 0, 1); got != 0.5 {
		t.Fatalf("want default 0.5 for above-max, got %v", got)
	}
}

func TestFloatRange_RejectsNaN(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_NAN"
	setenv(t, key, "NaN")
	if got := envcfg.FloatRange(key, 0.5, 0, 1); got != 0.5 {
		t.Fatalf("want default 0.5 for NaN, got %v", got)
	}
}

func TestFloatRange_RejectsInf(t *testing.T) {
	const key = "TEST_ENVCFG_FLOATRANGE_INF"
	setenv(t, key, "+Inf")
	if got := envcfg.FloatRange(key, 0.5, 0, 1); !math.IsNaN(got) && got != 0.5 {
		t.Fatalf("want default 0.5 for Inf, got %v", got)
	}
}

// ---- Int -------------------------------------------------------------------

func TestInt_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_INT_UNSET"
	if got := envcfg.Int(key, 42); got != 42 {
		t.Fatalf("want 42, got %v", got)
	}
}

func TestInt_Override_WhenSet(t *testing.T) {
	const key = "TEST_ENVCFG_INT_SET"
	setenv(t, key, "7")
	if got := envcfg.Int(key, 42); got != 7 {
		t.Fatalf("want 7, got %v", got)
	}
}

func TestInt_Default_OnBadValue(t *testing.T) {
	const key = "TEST_ENVCFG_INT_BAD"
	setenv(t, key, "oops")
	if got := envcfg.Int(key, 99); got != 99 {
		t.Fatalf("want default 99, got %v", got)
	}
}

// ---- IntRange --------------------------------------------------------------

func TestIntRange_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_INTRANGE_UNSET"
	if got := envcfg.IntRange(key, 5, 1, 10); got != 5 {
		t.Fatalf("want 5, got %v", got)
	}
}

func TestIntRange_Override_InRange(t *testing.T) {
	const key = "TEST_ENVCFG_INTRANGE_IN"
	setenv(t, key, "8")
	if got := envcfg.IntRange(key, 5, 1, 10); got != 8 {
		t.Fatalf("want 8, got %v", got)
	}
}

func TestIntRange_Default_BelowMin(t *testing.T) {
	const key = "TEST_ENVCFG_INTRANGE_BELOW"
	setenv(t, key, "0")
	if got := envcfg.IntRange(key, 5, 1, 10); got != 5 {
		t.Fatalf("want default 5 for below-min, got %v", got)
	}
}

func TestIntRange_Default_AboveMax(t *testing.T) {
	const key = "TEST_ENVCFG_INTRANGE_ABOVE"
	setenv(t, key, "11")
	if got := envcfg.IntRange(key, 5, 1, 10); got != 5 {
		t.Fatalf("want default 5 for above-max, got %v", got)
	}
}

// ---- Bool ------------------------------------------------------------------

func TestBool_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_BOOL_UNSET"
	if got := envcfg.Bool(key, true); !got {
		t.Fatalf("want true, got %v", got)
	}
}

func TestBool_Override_True(t *testing.T) {
	for _, val := range []string{"1", "true", "TRUE", "t"} {
		val := val
		t.Run(val, func(t *testing.T) {
			const key = "TEST_ENVCFG_BOOL_TRUE"
			setenv(t, key, val)
			if got := envcfg.Bool(key, false); !got {
				t.Fatalf("want true for %q, got false", val)
			}
		})
	}
}

func TestBool_Override_False(t *testing.T) {
	const key = "TEST_ENVCFG_BOOL_FALSE"
	setenv(t, key, "0")
	if got := envcfg.Bool(key, true); got {
		t.Fatalf("want false, got true")
	}
}

func TestBool_Default_OnBadValue(t *testing.T) {
	const key = "TEST_ENVCFG_BOOL_BAD"
	setenv(t, key, "yes") // not accepted by strconv.ParseBool
	if got := envcfg.Bool(key, false); got {
		t.Fatalf("want default false for bad value, got true")
	}
}

// ---- String ----------------------------------------------------------------

func TestString_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_STRING_UNSET"
	if got := envcfg.String(key, "hello"); got != "hello" {
		t.Fatalf("want hello, got %q", got)
	}
}

func TestString_Override_WhenSet(t *testing.T) {
	const key = "TEST_ENVCFG_STRING_SET"
	setenv(t, key, "world")
	if got := envcfg.String(key, "hello"); got != "world" {
		t.Fatalf("want world, got %q", got)
	}
}

// ---- Duration --------------------------------------------------------------

func TestDuration_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_DURATION_UNSET"
	if got := envcfg.Duration(key, 5*time.Second); got != 5*time.Second {
		t.Fatalf("want 5s, got %v", got)
	}
}

func TestDuration_Override_WhenSet(t *testing.T) {
	const key = "TEST_ENVCFG_DURATION_SET"
	setenv(t, key, "2m30s")
	if got := envcfg.Duration(key, time.Second); got != 150*time.Second {
		t.Fatalf("want 150s, got %v", got)
	}
}

func TestDuration_Default_OnBadValue(t *testing.T) {
	const key = "TEST_ENVCFG_DURATION_BAD"
	setenv(t, key, "notaduration")
	if got := envcfg.Duration(key, 10*time.Second); got != 10*time.Second {
		t.Fatalf("want default 10s, got %v", got)
	}
}

// ---- PositiveDuration -------------------------------------------------------

func TestPositiveDuration_Default_WhenUnset(t *testing.T) {
	const key = "TEST_ENVCFG_PD_UNSET_XYZ"
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 5*time.Minute {
		t.Fatalf("want 5m, got %v", got)
	}
}

func TestPositiveDuration_Default_WhenEmpty(t *testing.T) {
	const key = "TEST_ENVCFG_PD_EMPTY"
	setenv(t, key, "")
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 5*time.Minute {
		t.Fatalf("want 5m for empty string, got %v", got)
	}
}

func TestPositiveDuration_Default_WhenZero(t *testing.T) {
	const key = "TEST_ENVCFG_PD_ZERO"
	setenv(t, key, "0")
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 5*time.Minute {
		t.Fatalf("want 5m for zero, got %v", got)
	}
}

func TestPositiveDuration_Default_WhenNegative(t *testing.T) {
	const key = "TEST_ENVCFG_PD_NEG"
	setenv(t, key, "-5")
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 5*time.Minute {
		t.Fatalf("want 5m for negative, got %v", got)
	}
}

func TestPositiveDuration_Default_WhenNonNumeric(t *testing.T) {
	const key = "TEST_ENVCFG_PD_NAN"
	setenv(t, key, "abc")
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 5*time.Minute {
		t.Fatalf("want 5m for non-numeric, got %v", got)
	}
}

func TestPositiveDuration_Override_WhenPositive(t *testing.T) {
	const key = "TEST_ENVCFG_PD_OK"
	setenv(t, key, "12")
	if got := envcfg.PositiveDuration(key, 5, time.Minute); got != 12*time.Minute {
		t.Fatalf("want 12m, got %v", got)
	}
}
