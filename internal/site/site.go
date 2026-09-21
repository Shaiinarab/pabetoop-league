// Package site is the single source of truth for this deployment's identity.
//
// Pabetoop League ships as a template: no league, competition, city or club
// name is baked into the code, the templates or the seed data. A deployment
// re-brands itself with environment variables, which is what makes a fork
// deployable without a source edit.
//
//	LEAGUE_NAME            Latin name, used in docs, backups and logs
//	                       (default "Pabetoop League")
//	LEAGUE_NAME_FA         Persian display name shown in the UI
//	                       (default "لیگ فوتبال نوجوانان")
//	LEAGUE_DISCIPLINE_FA   Sport noun phrase (default "فوتبال")
//	LEAGUE_SUBTITLE_FA     Page footer strapline; empty derives one from the
//	                       discipline and the name
package site

import (
	"os"
	"strings"
)

// Profile is one deployment's identity.
type Profile struct {
	// Name is the Latin-script name.
	Name string
	// NameFA is the Persian display name rendered in page titles and the header.
	NameFA string
	// DisciplineFA is the sport, e.g. "فوتبال" or "فوتسال".
	DisciplineFA string
	// SubtitleFA is the footer strapline; when empty it is derived.
	SubtitleFA string
}

// Defaults returns the identity shipped with the template: a generic
// youth-football competition platform with no city and no league affiliation.
func Defaults() Profile {
	return Profile{
		Name:         "Pabetoop League",
		NameFA:       "لیگ فوتبال نوجوانان",
		DisciplineFA: "فوتبال",
		SubtitleFA:   "",
	}
}

// FromEnv builds a Profile from the process environment, falling back to
// Defaults() per field. An explicitly-set empty value disables that field.
func FromEnv() Profile {
	p := Defaults()
	if v, ok := os.LookupEnv("LEAGUE_NAME"); ok {
		p.Name = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("LEAGUE_NAME_FA"); ok {
		p.NameFA = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("LEAGUE_DISCIPLINE_FA"); ok {
		p.DisciplineFA = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("LEAGUE_SUBTITLE_FA"); ok {
		p.SubtitleFA = strings.TrimSpace(v)
	}
	return p
}

var active = FromEnv()

// Active returns the profile this process is running with.
func Active() Profile { return active }

// Set replaces the active profile. Intended for tests and for embedders that
// resolve configuration themselves before boot.
func Set(p Profile) { active = p }

// Subtitle returns the footer strapline: the explicit value when configured,
// otherwise a sentence derived from the discipline.
func (p Profile) Subtitle() string {
	if p.SubtitleFA != "" {
		return p.SubtitleFA
	}
	if p.DisciplineFA == "" {
		return "سامانهٔ نتایج و جدول مسابقات"
	}
	return "سامانهٔ نتایج مسابقات " + p.DisciplineFA
}
