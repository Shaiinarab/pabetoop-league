package jalali

import (
	"testing"
	"time"
)

// Anchor dates verified against the known-correct jalaali-js implementation
// and calendar references.
func TestAnchorDates(t *testing.T) {
	cases := []struct {
		jy, jm, jd int
		gy, gm, gd int
	}{
		{1403, 1, 1, 2024, 3, 20},   // Nowruz 1403 (after leap 1402)
		{1403, 12, 30, 2025, 3, 20}, // 1403 IS leap → Esfand 30 exists
		{1404, 1, 1, 2025, 3, 21},   // Nowruz 1404
		{1404, 12, 29, 2026, 3, 20}, // 1404 NOT leap → Esfand 29
		{1405, 7, 20, 2026, 10, 12}, // spec example date
		{1405, 1, 1, 2026, 3, 21},   // Nowruz 1405
		{1399, 12, 30, 2021, 3, 20}, // 1399 leap
		{1398, 12, 29, 2020, 3, 19}, // 1398 not leap
		{1500, 5, 15, 2121, 8, 6},   // far future sanity
		{1000, 3, 25, 1621, 6, 15},  // far past sanity
	}
	for _, c := range cases {
		got, err := ToGregorian(c.jy, c.jm, c.jd)
		if err != nil {
			t.Fatalf("ToGregorian(%d,%d,%d): %v", c.jy, c.jm, c.jd, err)
		}
		if got.Year() != c.gy || int(got.Month()) != c.gm || got.Day() != c.gd {
			t.Errorf("ToGregorian(%d,%d,%d) = %s, want %04d-%02d-%02d",
				c.jy, c.jm, c.jd, got.Format("2006-01-02"), c.gy, c.gm, c.gd)
		}
		jy, jm, jd := FromGregorian(time.Date(c.gy, time.Month(c.gm), c.gd, 0, 0, 0, 0, time.UTC))
		if jy != c.jy || jm != c.jm || jd != c.jd {
			t.Errorf("FromGregorian(%04d-%02d-%02d) = %d/%d/%d, want %d/%d/%d",
				c.gy, c.gm, c.gd, jy, jm, jd, c.jy, c.jm, c.jd)
		}
	}
}

func TestRoundTripWholeRange(t *testing.T) {
	// Every day of a leap year and a normal year must round-trip exactly.
	for _, jy := range []int{1399, 1403, 1404, 1405} {
		for jm := 1; jm <= 12; jm++ {
			for jd := 1; jd <= MonthLength(jy, jm); jd++ {
				g, err := ToGregorian(jy, jm, jd)
				if err != nil {
					t.Fatalf("ToGregorian(%d,%d,%d): %v", jy, jm, jd, err)
				}
				jy2, jm2, jd2 := FromGregorian(g)
				if jy2 != jy || jm2 != jm || jd2 != jd {
					t.Fatalf("round-trip failed: %d/%d/%d -> %s -> %d/%d/%d",
						jy, jm, jd, g.Format("2006-01-02"), jy2, jm2, jd2)
				}
			}
		}
	}
}

func TestMonthLength(t *testing.T) {
	if got := MonthLength(1403, 12); got != 30 { // 1403 leap
		t.Errorf("MonthLength(1403,12) = %d, want 30", got)
	}
	if got := MonthLength(1404, 12); got != 29 { // 1404 not leap
		t.Errorf("MonthLength(1404,12) = %d, want 29", got)
	}
	if got := MonthLength(1405, 6); got != 31 {
		t.Errorf("MonthLength(1405,6) = %d, want 31", got)
	}
	if got := MonthLength(1405, 7); got != 30 {
		t.Errorf("MonthLength(1405,7) = %d, want 30", got)
	}
}

func TestParse(t *testing.T) {
	// Persian digits with slashes
	got, err := Parse("۱۴۰۵/۰۷/۲۰")
	if err != nil {
		t.Fatalf("Parse persian: %v", err)
	}
	want := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Parse(۱۴۰۵/۰۷/۲۰) = %s, want %s", got, want)
	}
	// Latin digits, dash separator
	got, err = Parse("1405-07-20")
	if err != nil || !got.Equal(want) {
		t.Errorf("Parse(1405-07-20) = %s (%v), want %s", got, err, want)
	}
	// invalid month
	if _, err := Parse("1405/13/01"); err == nil {
		t.Error("Parse(1405/13/01) should fail")
	}
	// invalid day (1404 not leap)
	if _, err := Parse("1404/12/30"); err == nil {
		t.Error("Parse(1404/12/30) should fail (1404 is not leap)")
	}
	// garbage
	if _, err := Parse("hello"); err == nil {
		t.Error("Parse(hello) should fail")
	}
}

func TestFormat(t *testing.T) {
	got := Format(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if got != "۱۴۰۵/۰۷/۲۰" {
		t.Errorf("Format = %q, want ۱۴۰۵/۰۷/۲۰", got)
	}
	long := FormatLong(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if long != "۲۰ مهر ۱۴۰۵" {
		t.Errorf("FormatLong = %q, want ۲۰ مهر ۱۴۰۵", long)
	}
}

func TestDigitConversion(t *testing.T) {
	if ToPersianDigits("1405") != "۱۴۰۵" {
		t.Error("ToPersianDigits failed")
	}
	if LatinDigits("۱۴۰۵") != "1405" {
		t.Error("LatinDigits failed")
	}
	if LatinDigits("٩٩") != "99" {
		t.Error("Arabic-Indic digits not converted")
	}
}
