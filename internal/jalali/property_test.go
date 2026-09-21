// Property-based and adversarial tests for the jalali engine (TASK-006).
// These tests probe what jalali_test.go does not: day-step round-trips across
// two centuries, month-length invariants, leap-year cross-checks against an
// independently constructed reference list, parse fuzzing, and digit
// round-trips. Engines are NEVER modified here — a disagreement is frozen in
// a t.Skip with a precise comment and reported to the Lead.
package jalali

import (
	"math/rand"
	"strings"
	"testing"
)

// reproducible fuzzing; change intentionally only with Lead sign-off.
const propertyRandSeed = int64(1405)

// ---------- 1) day-step round-trip sweep 1304→1502 ----------

// jalaliDayStepSweep iterates Gregorian days one at a time from the Jalali
// new year of 1304 (1925-03-22) through the last day of Jalali 1501
// (2123-03-20 inclusive), checking:
//   - FromGregorian→ToGregorian round-trips to the identical UTC date
//   - (jy,jm,jd) advances by exactly one day per step (catches off-by-one at
//     month boundaries: Farvardin 31→Ordibehesh 1, Shahrivar 31→Mehr 1,
//     Esfand 29/30→Farvardin 1)
func TestPropertyDayStepRoundTrip(t *testing.T) {
	// Endpoints are derived from the engine: circular for absolute dates, but
	// safe here — the properties under test (round-trip identity and one-day
	// monotonic steps) are endpoint-independent. Absolute anchor correctness
	// is pinned separately (jalali_test.go + verified probes:
	// 2025-03-21 = 1404/01/01, 2026-03-21 = 1405/01/01, 2016-03-20 = 1395/01/01).
	// NOTE: the brief's start anchor (1925-03-22) is off by one day — probe
	// shows 1304/01/01 = 1925-03-21. Reported to the Lead.
	start, err := ToGregorian(1304, 1, 1)
	if err != nil {
		t.Fatalf("start anchor: %v", err)
	}
	endNewYear, err := ToGregorian(1502, 1, 1)
	if err != nil {
		t.Fatalf("end anchor: %v", err)
	}
	end := endNewYear.AddDate(0, 0, -1) // last day of 1501

	py, pm, pd := FromGregorian(start)
	if py != 1304 || pm != 1 || pd != 1 {
		t.Fatalf("anchor: FromGregorian(%s) = %d/%d/%d, want 1304/01/01", start, py, pm, pd)
	}

	steps := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		jy, jm, jd := FromGregorian(d)
		back, err := ToGregorian(jy, jm, jd)
		if err != nil {
			t.Fatalf("step %d (%s): ToGregorian(%d,%d,%d) error: %v", steps, d, jy, jm, jd, err)
		}
		if !back.Equal(d) {
			t.Fatalf("step %d: round-trip %s → (%d,%d,%d) → %s", steps, d, jy, jm, jd, back)
		}
		// advance by exactly one (jy,jm,jd) day
		njy, njm, njd := jy, jm, jd+1
		if njd > MonthLength(jy, jm) {
			njd = 1
			njm++
			if njm > 12 {
				njm = 1
				njy++
			}
		}
		wy, wm, wd := FromGregorian(d.AddDate(0, 0, 1))
		if wy != njy || wm != njm || wd != njd {
			t.Fatalf("day-step broken at %s: (%d,%d,%d)+1 = want (%d,%d,%d), got (%d,%d,%d)",
				d, jy, jm, jd, njy, njm, njd, wy, wm, wd)
		}
		steps++
	}
	if steps < 70000 {
		t.Fatalf("sweep too short: %d steps", steps)
	}
}

// ---------- 2) month-length invariants ----------

func TestPropertyMonthLengthInvariants(t *testing.T) {
	for jy := 1304; jy <= 1502; jy++ {
		sum := 0
		for jm := 1; jm <= 12; jm++ {
			n := MonthLength(jy, jm)
			switch {
			case jm <= 6:
				if n != 31 {
					t.Fatalf("MonthLength(%d,%d) = %d, want 31", jy, jm, n)
				}
			case jm <= 11:
				if n != 30 {
					t.Fatalf("MonthLength(%d,%d) = %d, want 30", jy, jm, n)
				}
			default: // Esfand
				if n != 29 && n != 30 {
					t.Fatalf("Esfand %d length = %d, want 29 or 30", jy, n)
				}
				leap := IsLeap(jy)
				if leap && n != 30 {
					t.Fatalf("leap year %d: Esfand = %d, want 30", jy, n)
				}
				if !leap && n != 29 {
					t.Fatalf("common year %d: Esfand = %d, want 29", jy, n)
				}
			}
			sum += n
		}
		want := 365
		if IsLeap(jy) {
			want = 366
		}
		if sum != want {
			t.Fatalf("year %d: ΣMonthLength = %d, want %d", jy, sum, want)
		}
	}
}

// ---------- 3) leap-year checks (hard-verified anchors + consistency) ----------

// leapCertainties holds only years whose leap status is hard-verified from
// real-world anchors / the standard 33-year algorithm:
//
//	1398 common (Nowruz 2020-03-20 opened 1399),
//	1399 leap   (Nowruz 2021-03-20 opened 1400),
//	1403 leap   (Nowruz 2025-03-21 opened 1404 — probed here),
//	1408 leap   (standard 33-year cycle continuation of 1403).
//
// The brief also lists 1448 as leap, but the standard 33-year algorithm puts
// the cycle-boundary 5-year gap at 1444→1449, making 1448 COMMON. The engine
// agrees with the standard algorithm (probed: IsLeap(1448) = false). This
// property test therefore asserts the verified set plus full-span internal
// consistency instead of the brief's naive list — the discrepancy is
// documented in OUTBOX/TASK-006-REPORT.md for the Lead.
var leapCertainties = map[int]bool{
	1398: false, 1399: true, 1403: true, 1408: true,
}

func TestPropertyKnownLeapYears(t *testing.T) {
	for jy, want := range leapCertainties {
		if got := IsLeap(jy); got != want {
			t.Skipf("leap disagreement (frozen, engine untouched): IsLeap(%d) = %v, reference %v — Lead decides",
				jy, got, want)
		}
	}

	// Full-span internal consistency (1304..1502): IsLeap(jy) ⇔ Esfand has 30
	// days ⇔ the Jalali year spans 366 Gregorian days. A library that
	// disagrees with itself anywhere in the supported range fails here.
	for jy := 1304; jy <= 1502; jy++ {
		leap := IsLeap(jy)
		esfand := MonthLength(jy, 12)
		if (esfand == 30) != leap {
			t.Fatalf("year %d: IsLeap=%v but Esfand length=%d", jy, leap, esfand)
		}
		newYear, err := ToGregorian(jy, 1, 1)
		if err != nil {
			t.Fatalf("new year %d: %v", jy, err)
		}
		nextNewYear, err := ToGregorian(jy+1, 1, 1)
		if err != nil {
			t.Fatalf("next new year %d: %v", jy+1, err)
		}
		days := int(nextNewYear.Sub(newYear).Hours() / 24)
		wantDays := 365
		if leap {
			wantDays = 366
		}
		if days != wantDays {
			t.Fatalf("year %d: spans %d days but IsLeap=%v", jy, days, leap)
		}
	}
}

// ---------- 4) parse fuzz ----------

// parseFuzzInput builds one fuzz candidate around a valid core date with a
// mutation class chosen by the seeded RNG.
func parseFuzzInput(rnd *rand.Rand) (input string, valid bool) {
	jy := 1300 + rnd.Intn(220) // 1300..1519 (near-boundary years included)
	jm := 1 + rnd.Intn(12)
	maxJd := MonthLength(jy, jm)
	jd := 1 + rnd.Intn(maxJd)

	latin := func(n int) string {
		s := ""
		if n < 10 {
			s = "0"
		}
		return s + itoa(n)
	}
	core := latin(jy) + "/" + latin(jm) + "/" + latin(jd)

	mut := rnd.Intn(8)
	switch mut {
	case 0: // Persian digits (valid)
		return ToPersianDigits(core), true
	case 1: // dash separator (valid)
		return strings.ReplaceAll(core, "/", "-"), true
	case 2: // extra spaces around (valid — Parse trims)
		return "  " + core + "  ", true
	case 3: // Arabic-Indic digits (valid — LatinDigits handles them)
		return arabicIndic(core), true
	case 4: // extra separator part (invalid)
		return core + "/0", false
	case 5: // ZWNJ injected (invalid: not a digit/separator after trim rules)
		return insertZWNJ(core, rnd), false
	case 6: // letter garbage inside a part (invalid)
		return core[:4] + "ع" + core[4:], false
	default: // empty middle part (invalid)
		parts := strings.SplitN(core, "/", 3)
		return parts[0] + "//" + parts[2], false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func arabicIndic(s string) string {
	repl := strings.NewReplacer("0", "٠", "1", "١", "2", "٢", "3", "٣", "4", "٤",
		"5", "٥", "6", "٦", "7", "٧", "8", "٨", "9", "٩")
	return repl.Replace(s)
}

func insertZWNJ(s string, rnd *rand.Rand) string {
	pos := rnd.Intn(len(s))
	return s[:pos] + "\u200c" + s[pos:]
}

func TestPropertyParseFuzz(t *testing.T) {
	rnd := rand.New(rand.NewSource(propertyRandSeed))
	for i := 0; i < 200; i++ {
		input, wantValid := parseFuzzInput(rnd)
		got, err := Parse(input)
		if wantValid {
			if err != nil {
				t.Fatalf("fuzz #%d: Parse(%q) = error %v, want valid", i, input, err)
			}
		} else {
			if err == nil {
				t.Fatalf("fuzz #%d: Parse(%q) = %v, want error (silently reinterpreted?)", i, input, got)
			}
		}
	}
}

// FROZEN FINDING (engine untouched, per TASK-006 hard rules):
// Parse silently reinterprets garbage because fmt.Sscanf("%d") prefix-scans:
//
//	Parse("1464ع/08/19") → 2085-11-09 (no error)
//
// RESOLVED FINDING (was frozen skip; Lead fixed Parse 2026-09-11):
// Parse must reject digit-prefixed garbage suffixes with a Persian error.
// History: fmt.Sscanf("%d") prefix-scanned parts, silently dropping garbage
// suffixes (e.g. "1464ع" → 1464). Fixed in jalali.go; oracle whitelist removed.
func TestPropertyParseRejectsGarbageSuffix(t *testing.T) {
	got, err := Parse("1464ع/08/19")
	if err == nil {
		t.Fatalf("Parse regression: %q = %v with no error (Sscanf prefix-scan bug is back?)", "1464ع/08/19", got)
	}
}

// Parse must never panic on arbitrary garbage.
func TestPropertyParseNeverPanics(t *testing.T) {
	rnd := rand.New(rand.NewSource(propertyRandSeed + 1))
	garbage := []string{"", "/", "//", "///", "۱۴۰۵", "۱۴۰۵/", "۱۴۰۵//", "-/-/-",
		"۱۴۰۵/۱۳/۰۱", "۱۴۰۵/۰۱/۳۲", "9999/00/00", "1405/13/40", "abc/def/ghi",
		"\n\t / / ", "۱۴۰۵/۰۷/۲۰/۵", "۱/۱/۱/۱"}
	for i := 0; i < 100; i++ {
		b := make([]byte, rnd.Intn(20))
		for j := range b {
			b[j] = byte(rnd.Intn(96) + 32) // printable ASCII noise
		}
		garbage = append(garbage, string(b))
	}
	for _, g := range garbage {
		_, _ = Parse(g) // must not panic; result ignored
	}
}

// ---------- 5) digit round-trip ----------

func TestPropertyDigitRoundTrip(t *testing.T) {
	rnd := rand.New(rand.NewSource(propertyRandSeed + 2))
	for i := 0; i < 500; i++ {
		n := rnd.Intn(20)
		b := make([]byte, n)
		for j := range b {
			b[j] = byte('0' + rnd.Intn(10))
		}
		orig := string(b)
		if back := LatinDigits(ToPersianDigits(orig)); back != orig {
			t.Fatalf("digit round-trip broken: %q → %q → %q", orig, ToPersianDigits(orig), back)
		}
		// mixed Persian + Arabic-Indic must also normalize to the same Latin
		mixed := arabicIndic(ToPersianDigits(orig))
		if back := LatinDigits(mixed); back != orig {
			t.Fatalf("arabic-indic round-trip broken: %q → %q", mixed, back)
		}
	}
}
