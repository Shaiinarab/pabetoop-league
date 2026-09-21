// Package jalali implements Persian (Shamsi) calendar conversion and formatting.
//
// It is the single place in the codebase where Jalali⇄Gregorian conversion happens
// (DECISIONS.md D9). Canonical machine representation is the Gregorian date
// ("2006-01-02" / time.Time in UTC); all user-facing rendering is Jalali with Persian digits.
//
// Conversion delegates to the battle-tested github.com/yaa110/go-persian-calendar
// library (per client direction: use proven implementations, not hand-rolled math).
package jalali

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	pt "github.com/yaa110/go-persian-calendar"
)

// Persian digits used for all user-facing numeric rendering.
const persianDigits = "۰۱۲۳۴۵۶۷۸۹"

// MonthNames is the canonical Persian month name list (index 1..12).
var MonthNames = [...]string{
	"", "فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور",
	"مهر", "آبان", "آذر", "دی", "بهمن", "اسفند",
}

// ToPersianDigits converts ASCII digits in s to Persian digits.
func ToPersianDigits(s string) string {
	var b strings.Builder
	digits := []rune(persianDigits)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(digits[r-'0'])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LatinDigits converts Persian (۰-۹) and Arabic-Indic (٠-٩) digits to ASCII.
func LatinDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteByte(byte('0' + r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteByte(byte('0' + r - '٠'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ToGregorian converts a Jalali date to the equivalent Gregorian time.Time (UTC midnight).
// It validates month/day ranges (leap-year aware) with Persian error messages.
func ToGregorian(jy, jm, jd int) (time.Time, error) {
	if jm < 1 || jm > 12 {
		return time.Time{}, fmt.Errorf("ماه شمسی نامعتبر: %d", jm)
	}
	maxDay := MonthLength(jy, jm)
	if jd < 1 || jd > maxDay {
		return time.Time{}, fmt.Errorf("روز شمسی نامعتبر: %d/%02d/%02d", jy, jm, jd)
	}
	t := pt.Date(jy, pt.Month(jm), jd, 0, 0, 0, 0, time.UTC)
	return t.Time(), nil
}

// FromGregorian converts a Gregorian date to Jalali (year, month, day).
func FromGregorian(t time.Time) (jy, jm, jd int) {
	p := pt.New(t)
	y, m, d := p.Date()
	return y, int(m), d
}

// IsLeap reports whether a Jalali year is leap (366 days, Esfand 30).
func IsLeap(jy int) bool {
	return pt.Date(jy, pt.Farvardin, 1, 0, 0, 0, 0, time.UTC).IsLeap()
}

// MonthLength returns the number of days in Jalali month jm of year jy (months 1..12).
func MonthLength(jy, jm int) int {
	if jm < 1 || jm > 12 {
		return 0
	}
	last := pt.Date(jy, pt.Month(jm), 1, 0, 0, 0, 0, time.UTC).LastMonthDay()
	return last.Day()
}

// Format renders a time as the standard Persian numeric date: ۱۴۰۵/۰۷/۲۰.
// Zero-padded two-digit month and day.
func Format(t time.Time) string {
	jy, jm, jd := FromGregorian(t)
	return ToPersianDigits(fmt.Sprintf("%04d/%02d/%02d", jy, jm, jd))
}

// FormatLong renders a human-friendly long form: ۲۰ مهر ۱۴۰۵.
func FormatLong(t time.Time) string {
	jy, jm, jd := FromGregorian(t)
	return fmt.Sprintf("%s %s %s",
		ToPersianDigits(fmt.Sprintf("%d", jd)), MonthNames[jm], ToPersianDigits(fmt.Sprintf("%d", jy)))
}

// FormatISO renders the canonical Gregorian ISO date (machine form).
func FormatISO(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// Parse accepts Persian or Latin digits and both separators ("/" or "-") in
// Jalali form "YYYY/MM/DD" and returns the Gregorian UTC date. It validates
// month/day ranges (leap-year aware) and returns Persian error messages.
func Parse(input string) (time.Time, error) {
	s := LatinDigits(strings.TrimSpace(input))
	s = strings.NewReplacer("-", "/", "٫", "/").Replace(s)
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("تاریخ باید به شکل ۱۴۰۵/۰۷/۲۰ باشد: «%s»", input)
	}
	parsePart := func(s, label string) (int, error) {
		if s == "" {
			return 0, fmt.Errorf("%s نامعتبر در تاریخ: «%s»", label, input)
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("%s نامعتبر در تاریخ: «%s»", label, input)
			}
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%s نامعتبر در تاریخ: «%s»", label, input)
		}
		return n, nil
	}
	jy, err := parsePart(parts[0], "سال")
	if err != nil {
		return time.Time{}, err
	}
	jm, err := parsePart(parts[1], "ماه")
	if err != nil {
		return time.Time{}, err
	}
	jd, err := parsePart(parts[2], "روز")
	if err != nil {
		return time.Time{}, err
	}
	return ToGregorian(jy, jm, jd)
}
