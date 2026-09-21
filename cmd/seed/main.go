// Command seed builds a complete realistic development season inside a fresh
// SQLite database (TASK-007). Dev/seed tooling only — the product never
// auto-generates fixtures (DECISIONS.md D8).
//
// Usage:
//
//	go run ./cmd/seed --db data/seed.db [--force]
//
// Structure (PROJECT_SPEC §4–§5): season «۱۴۰۵–۱۴۰۶» (active), age groups
// 10..14, per age one premier competition (12 teams) plus league1 groups —
// U10: A,B · U11: A,B,C · U12: A,B,C,D · U13: A,B,C · U14: A,B (10 each).
// Clubs come ONLY from the JSON list given by --clubs-file (default
// seed/clubs.json), which ships with obvious placeholders (D7). Double
// round-robin fixtures per competition (circle method, weeks 1..2N−2,
// balanced home/away), deterministic results (seed 1405): the first ~55% of
// weeks finish with home-weighted scores; one 5-0 and two 0-0 draws are
// forced at deterministic finished-match positions. U12 premier gets
// engineered tie-break edge cases: one pair equal on points+GD with
// different GF (GF decides), another equal on points+GD+GF (Persian name
// decides). All writes go through the store API — no direct SQL here.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/jalali"
	"github.com/shaiinarab/pabetoop-league/internal/standing"
	"github.com/shaiinarab/pabetoop-league/internal/store"
)

// generation seed — same flag value must reproduce byte-identical content.
const rngSeed = int64(1405)

// league1 groups per age (10 teams each).
var league1Groups = map[int][]string{
	10: {"A", "B"},
	11: {"A", "B", "C"},
	12: {"A", "B", "C", "D"},
	13: {"A", "B", "C"},
	14: {"A", "B"},
}

const (
	premierSize      = 12
	league1GroupSize = 10
	finishFraction   = 0.55
)

type clubRef struct {
	ID   int64
	Name string
}

type teamRef struct {
	ID     int64
	ClubID int64
	Name   string
}

// extras carries the global "forced edge cases" state across competitions.
type extras struct {
	finished int  // count of finished matches so far (season-wide)
	fiveNil  bool // whether the forced 5-0 has been placed
	draws    int  // count of 0-0 draws placed by forcing
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "خطا: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	dbPath := flag.String("db", "data/seed.db", "مسیر فایل پایگاه داده")
	force := flag.Bool("force", false, "بازنویسی پایگاه داده موجود")
	clubsFile := flag.String("clubs-file", "seed/clubs.json",
		"فایل JSON نام باشگاه‌ها؛ برای دادهٔ نمایشی جایگزین کنید")
	flag.Parse()

	clubs := loadClubNames(*clubsFile)
	if len(clubs) < premierSize {
		fatalf("تعداد باشگاه‌ها برای لیگ برتر کافی نیست: %d", len(clubs))
	}

	if !*force {
		if fi, err := os.Stat(*dbPath); err == nil && fi.Size() > 0 {
			fmt.Println("پایگاه داده از قبل وجود دارد و خالی نیست.")
			fmt.Printf("برای بازنویسی از فلگ ‎--force استفاده کنید:  go run ./cmd/seed --db %s --force\n", *dbPath)
			os.Exit(2)
		}
	} else {
		if err := os.Remove(*dbPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fatalf("حذف پایگاه داده قبلی ممکن نشد: %v", err)
		}
		_ = os.Remove(*dbPath + "-wal")
		_ = os.Remove(*dbPath + "-shm")
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fatalf("باز کردن پایگاه داده: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(); err != nil {
		fatalf("ایجاد ساختار پایگاه داده: %v", err)
	}

	start := time.Now()
	rnd := rand.New(rand.NewSource(rngSeed))
	ex := &extras{}

	// ---------- season + age groups ----------
	seasonID, err := s.CreateSeason("۱۴۰۵–۱۴۰۶", strPtr("2026-10-02"), strPtr("2027-05-23"))
	if err != nil {
		fatalf("ایجاد فصل: %v", err)
	}
	if err := s.ActivateSeason(seasonID); err != nil {
		fatalf("فعال‌سازی فصل: %v", err)
	}
	if err := s.EnsureAgeGroups(10, 11, 12, 13, 14); err != nil {
		fatalf("ایجاد رده‌های سنی: %v", err)
	}
	ageGroups, err := s.AgeGroups()
	if err != nil {
		fatalf("خواندن رده‌های سنی: %v", err)
	}
	ageID := map[int]int64{}
	for _, ag := range ageGroups {
		ageID[ag.Age] = ag.ID
	}

	// ---------- clubs (official names only — D7) ----------
	clubRefs := make([]clubRef, 0, len(clubs))
	for _, name := range clubs {
		id, err := s.CreateClub(name)
		if err != nil {
			fatalf("ایجاد باشگاه «%s»: %v", name, err)
		}
		clubRefs = append(clubRefs, clubRef{ID: id, Name: name})
	}

	// ---------- teams (global pool; the schema scopes teams per CLUB, not
	// per age — age association happens via registration). 3 teams per club
	// (labels «», «۲», «۳») = 219 teams ≥ 200 slots. The club-cycled order
	// guarantees: any 12 consecutive teams within one label-block have
	// distinct clubs, which every premier allocation lands inside. ----------
	allTeams := make([]teamRef, 0, 3*len(clubRefs))
	counts := map[int64]int{}
	for i := 0; i < 3*len(clubRefs); i++ {
		c := clubRefs[i%len(clubRefs)]
		counts[c.ID]++
		label := ""
		display := c.Name
		switch counts[c.ID] {
		case 2:
			label = "۲"
			display = c.Name + " ۲"
		case 3:
			label = "۳"
			display = c.Name + " ۳"
		}
		tid, err := s.CreateTeam(c.ID, label, display)
		if err != nil {
			fatalf("ایجاد تیم «%s»: %v", display, err)
		}
		allTeams = append(allTeams, teamRef{ID: tid, ClubID: c.ID, Name: display})
	}

	// ---------- competitions + fixtures + results ----------
	totalMatches, finishedMatches, totalDraws := 0, 0, 0
	var u12PremierID int64
	var u12PremierTeams []teamRef
	cursor := 0
	take := func(n int) []teamRef {
		if cursor+n > len(allTeams) {
			fatalf("استخر تیم برای تخصیص کافی نیست (%d > %d)", cursor+n, len(allTeams))
		}
		out := allTeams[cursor : cursor+n]
		cursor += n
		return out
	}

	for _, age := range []int{10, 11, 12, 13, 14} {
		premTeams := take(premierSize)
		compID, err := s.CreateCompetition(seasonID, ageID[age], "premier", nil,
			"لیگ برتر "+toPersian(age)+" سال")
		if err != nil {
			fatalf("ایجاد لیگ برتر %d: %v", age, err)
		}
		if err := registerAll(s, compID, premTeams); err != nil {
			fatalf("ثبت‌نام لیگ برتر %d: %v", age, err)
		}
		t, f, d, err := runFixturesAndResults(s, compID, premTeams, rnd, ex)
		if err != nil {
			fatalf("برنامه لیگ برتر %d: %v", age, err)
		}
		totalMatches += t
		finishedMatches += f
		totalDraws += d
		if age == 12 {
			u12PremierID = compID
			u12PremierTeams = premTeams
		}

		for _, g := range league1Groups[age] {
			groupTeams := take(league1GroupSize)
			groupName := g
			cid, err := s.CreateCompetition(seasonID, ageID[age], "league1", &groupName,
				"لیگ یک "+toPersian(age)+" سال — گروه "+g)
			if err != nil {
				fatalf("ایجاد لیگ یک %d گروه %s: %v", age, g, err)
			}
			if err := registerAll(s, cid, groupTeams); err != nil {
				fatalf("ثبت‌نام لیگ یک %d%s: %v", age, g, err)
			}
			t, f, d, err := runFixturesAndResults(s, cid, groupTeams, rnd, ex)
			if err != nil {
				fatalf("برنامه لیگ یک %d%s: %v", age, g, err)
			}
			totalMatches += t
			finishedMatches += f
			totalDraws += d
		}
	}

	// ---------- U12 premier tie-break edge cases ----------
	top3, err := engineerTieBreaks(s, u12PremierID, u12PremierTeams)
	if err != nil {
		fatalf("تعدیل نتایج لیگ برتر ۱۲ سال: %v", err)
	}

	totalTeams := len(allTeams)
	seasonComps, _ := s.Competitions(&seasonID, nil)

	// ---------- Persian summary (digits via internal/jalali — D9) ----------
	fmt.Println("════════════════════════════════════════════")
	fmt.Println("داده‌های نمونه پا موفقیت ساخته شد")
	fmt.Println("════════════════════════════════════════════")
	fmt.Printf("فایل پایگاه داده: %s\n", *dbPath)
	fmt.Printf("باشگاه‌ها: %s | تیم‌ها: %s | مسابقات: %s\n",
		toPersian(len(clubRefs)), toPersian(totalTeams), toPersian(len(seasonComps)))
	fmt.Printf("بازی‌ها: %s (تمام‌شده: %s، مساوی: %s)\n",
		toPersian(totalMatches), toPersian(finishedMatches), toPersian(totalDraws))
	fmt.Printf("نتیجه ۵-۰ در فصل: %s | مساوی ۰-۰ تحمیلی: %s\n",
		toPersian(btoi(ex.fiveNil)), toPersian(ex.draws))
	fmt.Printf("زمان ساخت: %s ثانیه\n", toPersian(int(time.Since(start).Seconds())))
	fmt.Println()
	fmt.Println("نمونه جدول — لیگ برتر ۱۲ سال (سه نفر برتر):")
	for _, r := range top3 {
		fmt.Printf("  %s. %s — %s امتیاز (تفاضل %s، گل زده %s)\n",
			toPersian(r.Rank), r.TeamName, toPersian(r.Points),
			toPersian(r.GoalDiff), toPersian(r.GoalsFor))
	}
}

func registerAll(s *store.Store, compID int64, teams []teamRef) error {
	for _, t := range teams {
		if _, err := s.Register(compID, t.ID); err != nil {
			return fmt.Errorf("«%s»: %w", t.Name, err)
		}
	}
	return nil
}

func loadClubNames(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("خواندن فایل باشگاه‌ها (%s): %v", path, err)
	}
	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		fatalf("تحلیل JSON باشگاه‌ها: %v", err)
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.Name)
	}
	return out
}

// takeDistinctClubTeams is no longer needed: the cursor allocation over the
// club-cycled team pool guarantees distinct-club premiers by construction.

// runFixturesAndResults generates the double round-robin, persists every
// match via the store, and finishes the first ~55% of weeks.
func runFixturesAndResults(s *store.Store, compID int64, teams []teamRef, rnd *rand.Rand,
	ex *extras) (total, finished, draws int, err error) {
	fixtures := doubleRoundRobin(len(teams))
	dates := weekStartDates(len(fixtures))

	for weekIdx, round := range fixtures {
		week := weekIdx + 1
		date := dates[weekIdx]
		for _, pr := range round {
			home, away := teams[pr.home], teams[pr.away]
			w, d := week, date
			matchID, err := s.CreateMatch(compID, home.ID, away.ID, &w, &d, nil, nil)
			if err != nil {
				return total, finished, draws, err
			}
			total++
			if float64(week) <= finishFraction*float64(len(fixtures)) {
				hs, as := sampleScore(rnd, ex)
				if err := s.SetResult(matchID, hs, as); err != nil {
					return total, finished, draws, err
				}
				finished++
				if hs == as {
					draws++
				}
			}
		}
	}
	return total, finished, draws, nil
}

type pairing struct{ home, away int }

// doubleRoundRobin: circle method — N−1 single rounds (balanced venue
// alternation), then the mirrored return leg. Weeks 1..2N−2.
func doubleRoundRobin(n int) [][]pairing {
	realN := n
	if n%2 == 1 {
		n++ // ghost slot
	}
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i
	}
	single := make([][]pairing, 0, n-1)
	for round := 0; round < n-1; round++ {
		var pairs []pairing
		for i := 0; i < n/2; i++ {
			h, a := ids[i], ids[n-1-i]
			if round%2 == 0 {
				h, a = a, h
			}
			if h < realN && a < realN && h != a { // skip the ghost pairing
				pairs = append(pairs, pairing{home: h, away: a})
			}
		}
		single = append(single, pairs)
		last := ids[n-1]
		copy(ids[2:], ids[1:n-1])
		ids[1] = last
	}
	out := make([][]pairing, 0, 2*(n-1))
	out = append(out, single...)
	for _, r := range single {
		mirror := make([]pairing, len(r))
		for i, p := range r {
			mirror[i] = pairing{home: p.away, away: p.home}
		}
		out = append(out, mirror)
	}
	return out
}

// weekStartDates: week 1 = 1405/07/10 (2026-10-02 ISO), +7 days per week.
// ISO Gregorian in the store; Jalali rendering belongs to the UI (D9).
func weekStartDates(rounds int) []string {
	base := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	out := make([]string, rounds)
	for i := range out {
		out[i] = base.AddDate(0, 0, 7*i).Format("2006-01-02")
	}
	return out
}

// sampleScore: home-weighted random score (home 0..5, away 0..4). The 5-0
// and the two 0-0 draws are forced at deterministic finished-match indexes
// (7th, 3rd and 11th) so the "at least" guarantees hold by construction.
func sampleScore(rnd *rand.Rand, ex *extras) (int, int) {
	ex.finished++
	switch ex.finished {
	case 3, 11:
		ex.draws++
		return 0, 0
	case 7:
		ex.fiveNil = true
		return 5, 0
	}
	return rnd.Intn(6), rnd.Intn(5)
}

// engineerTieBreaks adjusts U12-premier results so that:
//   - pair (A,B): equal points + equal GD, different GF → GF decides
//   - pair (C,D): equal points + GD + GF → Persian name decides
//
// Mechanism (exact, no probabilistic repair): results finish by WEEK, so
// every team has the same number of finished matches. For a pair, clear
// every finished match involving either team, then re-enter:
//   - every finished head-to-head meeting: 1-1 (symmetric draw — there may
//     be 0..2 of these inside the finished window; both sides stay equal)
//   - A/C's other finished matches: 0-0
//   - B's others: 1-1 (GF-decides pair) or 0-0 (name-decides pair)
//   - D's others: 0-0
//
// Equal finished-match counts keep points equal; all draws keep GD equal
// (0); the 1-1 others give the GF pair a GF gap of 2·count.
func engineerTieBreaks(s *store.Store, compID int64, teams []teamRef) ([]standing.Row, error) {
	names := make([]string, 0, len(teams))
	byName := map[string]teamRef{}
	for _, t := range teams {
		names = append(names, t.Name)
		byName[t.Name] = t
	}
	sort.Strings(names)
	pairGF := [2]teamRef{byName[names[0]], byName[names[1]]}
	pairName := [2]teamRef{byName[names[2]], byName[names[3]]}

	if err := shapePair(s, compID, pairGF[0], pairGF[1], true); err != nil {
		return nil, err
	}
	if err := shapePair(s, compID, pairName[0], pairName[1], false); err != nil {
		return nil, err
	}

	// Verify with the real engine.
	regs, err := s.Registrations(compID)
	if err != nil {
		return nil, err
	}
	teamInputs := make([]standing.TeamInput, 0, len(regs))
	for _, r := range regs {
		teamInputs = append(teamInputs, standing.TeamInput{ID: r.TeamID, Name: r.TeamName})
	}
	fin, err := s.FinishedMatches(compID)
	if err != nil {
		return nil, err
	}
	matchInputs := make([]standing.MatchInput, 0, len(fin))
	for _, f := range fin {
		matchInputs = append(matchInputs, standing.MatchInput{
			HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID,
			HomeGoals: f.HomeGoals, AwayGoals: f.AwayGoals,
		})
	}
	rows := standing.Compute(teamInputs, matchInputs)

	rowOf := map[int64]*standing.Row{}
	for i := range rows {
		rowOf[rows[i].TeamID] = &rows[i]
	}
	ga, gb := rowOf[pairGF[0].ID], rowOf[pairGF[1].ID]
	ca, cb := rowOf[pairName[0].ID], rowOf[pairName[1].ID]
	if ga == nil || gb == nil || ca == nil || cb == nil {
		return nil, fmt.Errorf("تیم‌های تعدیل در جدول یافت نشدند")
	}
	// GF decides: equal points+GD, different GF, higher GF ranked first.
	if ga.Points != gb.Points || ga.GoalDiff != gb.GoalDiff {
		return nil, fmt.Errorf("تعدیل GF: امتیاز/تفاضل برابر نشد (A=%+v, B=%+v)", ga, gb)
	}
	if ga.GoalsFor == gb.GoalsFor {
		return nil, fmt.Errorf("تعدیل GF: گل‌های زده برابر شد؛ باید متفاوت باشد")
	}
	if ga.Rank > gb.Rank && ga.GoalsFor > gb.GoalsFor {
		return nil, fmt.Errorf("تعدیل GF: تیم با گل بیشتر باید بالاتر باشد")
	}
	if gb.Rank > ga.Rank && gb.GoalsFor > ga.GoalsFor {
		return nil, fmt.Errorf("تعدیل GF: تیم با گل بیشتر باید بالاتر باشد")
	}
	// Name decides: fully equal stats → PersianLess ranks them.
	if ca.Points != cb.Points || ca.GoalDiff != cb.GoalDiff || ca.GoalsFor != cb.GoalsFor {
		return nil, fmt.Errorf("تعدیل نام: آمار برابر نشد (C=%+v, D=%+v)", ca, cb)
	}
	first, second := ca, cb
	if cb.Rank < ca.Rank {
		first, second = cb, ca
	}
	wantFirst, wantSecond := pairName[0], pairName[1]
	if standing.PersianLess(wantSecond.Name, wantFirst.Name) {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	if first.TeamID != wantFirst.ID || second.TeamID != wantSecond.ID {
		return nil, fmt.Errorf("تعدیل نام: «%s» باید بالاتر از «%s» باشد", wantFirst.Name, wantSecond.Name)
	}
	return rows[:3], nil
}

// shapePair implements the mechanism described on engineerTieBreaks.
func shapePair(s *store.Store, compID int64, x, y teamRef, gfDecides bool) error {
	fin, err := s.Matches(compID, nil, strPtr("finished"))
	if err != nil {
		return err
	}
	var pairM, otherX, otherY []store.Match
	for _, m := range fin {
		inX := m.HomeTeamID == x.ID || m.AwayTeamID == x.ID
		inY := m.HomeTeamID == y.ID || m.AwayTeamID == y.ID
		switch {
		case inX && inY:
			pairM = append(pairM, m)
		case inX:
			otherX = append(otherX, m)
		case inY:
			otherY = append(otherY, m)
		}
	}
	if len(pairM) > 2 || len(otherX) != len(otherY) || len(otherX)+len(pairM) == 0 {
		return fmt.Errorf("ساختار بازی‌های «%s»/«%s» برای تعدیل مناسب نیست (h2h=%d، دیگر=%d/%d)",
			x.Name, y.Name, len(pairM), len(otherX), len(otherY))
	}

	// Clear every finished match of the pair.
	for _, m := range append(append([]store.Match{}, pairM...), otherX...) {
		if err := s.ClearResult(m.ID); err != nil {
			return err
		}
	}
	for _, m := range otherY {
		if err := s.ClearResult(m.ID); err != nil {
			return err
		}
	}

	// Re-enter: every finished head-to-head meeting as a 1-1 draw
	// (symmetric for both teams regardless of how many fell in the window).
	for _, m := range pairM {
		if err := s.SetResult(m.ID, 1, 1); err != nil {
			return err
		}
	}
	// X's others: 0-0.
	for _, m := range otherX {
		if err := s.SetResult(m.ID, 0, 0); err != nil {
			return err
		}
	}
	// Y's others: 1-1 (GF-decides) or 0-0 (name-decides).
	yScore := 0
	if gfDecides {
		yScore = 1
	}
	for _, m := range otherY {
		if err := s.SetResult(m.ID, yScore, yScore); err != nil {
			return err
		}
	}
	return nil
}

func strPtr(s string) *string { return &s }

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// toPersian renders digits via internal/jalali — D9: jalali owns rendering.
func toPersian(n int) string {
	return jalali.ToPersianDigits(fmt.Sprintf("%d", n))
}
