package importer

import (
	"strings"
	"testing"
)

// jalaliAnchor is the well-known equivalence used across the project's tests:
// Jalali ۱۴۰۵/۰۷/۲۰ == Gregorian 2026-10-12.
const (
	jalaliAnchor = "۱۴۰۵/۰۷/۲۰"
	isoAnchor    = "2026-10-12"
)

func TestParseHeaderWithPersianDigitsAndJalaliDate(t *testing.T) {
	csv := strings.Join([]string{
		"هفته,تاریخ,ساعت,میزبان,مهمان,زمین",
		"۵,۱۴۰۵/۰۷/۲۰,۱۶:۳۰,نمونه ب ۱,نمونه پ ۱,زمین نمونه",
	}, "\n")

	res := Parse([]byte(csv))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	row := res.Rows[0]
	if row.Week == nil || *row.Week != 5 {
		t.Errorf("week = %v, want 5", row.Week)
	}
	if row.Date != isoAnchor {
		t.Errorf("date = %q, want %q (Jalali %s)", row.Date, isoAnchor, jalaliAnchor)
	}
	if row.Time != "16:30" {
		t.Errorf("time = %q, want 16:30", row.Time)
	}
	if row.Home != "نمونه ب ۱" || row.Away != "نمونه پ ۱" {
		t.Errorf("teams = %q vs %q", row.Home, row.Away)
	}
	if row.Venue != "زمین نمونه" {
		t.Errorf("venue = %q", row.Venue)
	}
	if row.Line != 2 {
		t.Errorf("line = %d, want 2", row.Line)
	}
}

func TestParseHeaderlessPositionalWithISODate(t *testing.T) {
	res := Parse([]byte("1,2026-10-12,16:00,نمونه ب ۱,نمونه پ ۱,زمین نمونه ۲"))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	row := res.Rows[0]
	if row.Week == nil || *row.Week != 1 {
		t.Errorf("week = %v, want 1", row.Week)
	}
	if row.Date != isoAnchor {
		t.Errorf("date = %q, want %q", row.Date, isoAnchor)
	}
	if row.Time != "16:00" {
		t.Errorf("time = %q, want 16:00", row.Time)
	}
}

func TestParsePipeDelimitedStructuredText(t *testing.T) {
	text := strings.Join([]string{
		"هفته | تاریخ | میزبان | مهمان",
		"۵ | ۱۴۰۵/۰۷/۲۰ | نمونه ب ۱ | نمونه پ ۱",
	}, "\n")
	res := Parse([]byte(text))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	if res.Rows[0].Home != "نمونه ب ۱" || res.Rows[0].Away != "نمونه پ ۱" {
		t.Errorf("pipe parsing wrong: %+v", res.Rows[0])
	}
	if res.Rows[0].Date != isoAnchor {
		t.Errorf("date = %q, want %q", res.Rows[0].Date, isoAnchor)
	}
}

func TestParsePersianCommaDelimiter(t *testing.T) {
	res := Parse([]byte("۱،۱۴۰۵/۰۷/۲۰،نمونه ب ۱،نمونه پ ۱"))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	if res.Rows[0].Home != "نمونه ب ۱" {
		t.Errorf("Persian-comma parsing wrong: %+v", res.Rows[0])
	}
}

func TestParseSemicolonDelimiter(t *testing.T) {
	res := Parse([]byte("هفته;تاریخ;میزبان;مهمان\n۲;۱۴۰۵/۰۷/۲۰;نمونه ب ۱;نمونه پ ۱"))
	if len(res.Rows) != 1 || len(res.Errors) != 0 {
		t.Fatalf("semicolon parse failed: rows=%d errors=%+v", len(res.Rows), res.Errors)
	}
}

func TestParseQuotedFieldKeepsSeparator(t *testing.T) {
	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,زمین",
		`۴,۱۴۰۵/۰۷/۲۰,نمونه ب ۱,نمونه پ ۱,"زمین نمونه، سالن ۲"`,
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	if res.Rows[0].Venue != "زمین نمونه، سالن ۲" {
		t.Errorf("venue = %q, want the quoted value with its separator", res.Rows[0].Venue)
	}
}

func TestParseSkipsCommentsAndBlankLines(t *testing.T) {
	csv := strings.Join([]string{
		"# این فایل نمونهٔ برنامهٔ هفتهٔ پنجم است",
		"هفته,تاریخ,میزبان,مهمان",
		"",
		"۵,۱۴۰۵/۰۷/۲۰,نمونه ب ۱,نمونه پ ۱",
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	// Line numbering counts the physical file, comments included.
	if res.Rows[0].Line != 4 {
		t.Errorf("line = %d, want 4", res.Rows[0].Line)
	}
}

func TestParseGarbageRowsReportExactLineNumbers(t *testing.T) {
	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان", // line 1 (header)
		"#,توضیح", // line 2 (comment, skipped)
		"",        // line 3 (blank, skipped)
		"۱,تاریخ‌غلط,نمونه ب ۱,نمونه پ ۱", // line 4 (bad date)
		"۲,۱۴۰۵/۰۷/۲۰,,نمونه پ ۱",         // line 5 (no home team)
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Rows) != 0 {
		t.Fatalf("rows = %d, want 0 (both data rows are fatal)", len(res.Rows))
	}
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want exactly 2", res.Errors)
	}
	if res.Errors[0].Line != 4 || !strings.Contains(res.Errors[0].Message, "تاریخ نامعتبر") {
		t.Errorf("first error = %+v, want line 4 with a Persian invalid-date message", res.Errors[0])
	}
	if res.Errors[1].Line != 5 || !strings.Contains(res.Errors[1].Message, "میزبان خالی") {
		t.Errorf("second error = %+v, want line 5 with a Persian empty-home message", res.Errors[1])
	}
}

func TestParseScoresOnlyWhenBothPresent(t *testing.T) {
	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,گل میزبان,گل مهمان",
		"۱,۱۴۰۵/۰۷/۲۰,نمونه ب ۱,نمونه پ ۱,۲,۱",
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	row := res.Rows[0]
	if row.HomeScore == nil || row.AwayScore == nil || *row.HomeScore != 2 || *row.AwayScore != 1 {
		t.Fatalf("scores = %v-%v, want 2-1", row.HomeScore, row.AwayScore)
	}

	// Half a score is a row error: a finished match needs both.
	half := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان,گل میزبان,گل مهمان",
		"۱,۱۴۰۵/۰۷/۲۰,نمونه ب ۱,نمونه پ ۱,۲,",
	}, "\n")
	res2 := Parse([]byte(half))
	if len(res2.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(res2.Rows))
	}
	if len(res2.Errors) != 1 || !strings.Contains(res2.Errors[0].Message, "هر دو نتیجه") {
		t.Errorf("errors = %+v, want one Persian both-scores-required message", res2.Errors)
	}
}

// D7: the official name must survive byte-for-byte, including ZWNJ and
// Arabic-form letters. The parser never rewrites what the admin supplied.
func TestParsePreservesOfficialNameBytes(t *testing.T) {
	// Built with an explicit escape so the ZWNJ is definitely present rather
	// than depending on how this file was typed.
	official := "نمونه\u200cت"
	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان",
		"۱,۱۴۰۵/۰۷/۲۰," + official + ",نمونه پ ۱",
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	if res.Rows[0].Home != official {
		t.Errorf("home = %q, want the exact official bytes %q", res.Rows[0].Home, official)
	}
	if !strings.Contains(res.Rows[0].Home, "\u200c") {
		t.Error("ZWNJ was stripped from the official name")
	}
}

func TestParseEmptyFileReportsPersianError(t *testing.T) {
	res := Parse([]byte("   \n"))
	if len(res.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(res.Rows))
	}
	if len(res.Errors) == 0 {
		t.Fatal("empty input should produce a Persian error")
	}
	if !strings.Contains(res.Errors[0].Message, "خالی") {
		t.Errorf("message = %q, want a Persian empty-file message", res.Errors[0].Message)
	}
}

func TestParseIntegerWeekRejectsGarbage(t *testing.T) {
	csv := strings.Join([]string{
		"هفته,تاریخ,میزبان,مهمان",
		"هفتهٔ پنجم,۱۴۰۵/۰۷/۲۰,نمونه ب ۱,نمونه پ ۱",
	}, "\n")
	res := Parse([]byte(csv))
	if len(res.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(res.Rows))
	}
	if len(res.Errors) != 1 || res.Errors[0].Field != "week" {
		t.Fatalf("errors = %+v, want one week error", res.Errors)
	}
}
