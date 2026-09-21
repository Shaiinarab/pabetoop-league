// Audit log UI (TASK-014, PROJECT_SPEC §17): a read-only, filterable view over
// the store's audit_log table. Every admin mutation is recorded by the store
// layer in the same transaction (DECISIONS.md D10); this file only presents it.
//
// Read-only is a hard rule: there is no POST/PUT/DELETE route here, and the store
// contract exposes no way to mutate or delete an audit row.
//
// Filtering and pagination are store-side (TASK-017): AuditLogFiltered returns
// the page and the true match count, so the page reaches the whole history
// instead of an in-memory-capped slice. The in-memory filter that used to serve
// the rows is gone rather than kept as dead code — with a store query available
// there is no state in which it would run again. The bounded unfiltered fetch
// below survives only to build the filter <option> list (per-value counts have
// no store query yet; a second method would need a NAG).
package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shaiinarab/pabetoop-league/internal/store"
)

const (
	// auditPageSize is the brief's page size (50 rows per page).
	auditPageSize = 50
	// auditFetchLimit bounds the unfiltered fetch that builds the filter
	// <option> lists. It does NOT bound the rendered rows — those page through
	// the whole history via store.AuditLogFiltered (TASK-017). Per-value counts
	// therefore cover only the most recent auditFetchLimit rows.
	auditFetchLimit = 500

	auditDataErrorMessage = "خطایی در خواندن گزارش تغییرات رخ داد؛ لطفاً بعداً تلاش کنید."
)

// auditActionLabels / auditEntityLabels translate the store's action/entity
// tokens into the Persian the administrator reads. Unknown tokens fall back to
// the raw token so a new store action can never render as an empty cell.
var auditActionLabels = map[string]string{
	"insert": "ایجاد",
	"update": "ویرایش",
	"delete": "حذف",
	"backup": "پشتیبان‌گیری",
}

var auditEntityLabels = map[string]string{
	"season":       "فصل",
	"age_group":    "ردهٔ سنی",
	"club":         "باشگاه",
	"team":         "تیم",
	"competition":  "مسابقات",
	"registration": "ثبت‌نام",
	"match":        "مسابقه",
	"database":     "پایگاه داده",
}

// changeFieldOrder is the preference order when summarising a JSON change payload
// (the store writes objects such as {"name":"نمونه ب"} or {"is_active":false}).
var changeFieldOrder = []string{"name", "display_name", "label", "status", "dest", "is_active"}

// ---- view shapes (must match admin/audit.html) ----

type auditFilterOption struct {
	Value string
	Label string
	Count int
}

type auditRowView struct {
	ID          int64
	When        string
	Action      string
	ActionLabel string
	Entity      string
	EntityLabel string
	EntityID    int64
	Admin       string
	Change      string
}

type auditView struct {
	adminPage
	Rows          []auditRowView
	Action        string
	Entity        string
	ActionOptions []auditFilterOption
	EntityOptions []auditFilterOption
	Page          int
	HasPrev       bool
	PrevURL       string
	HasNext       bool
	NextURL       string
	ClearURL      string
	Shown         int
	FilteredTotal int
}

// ---- handler ----

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	setDynamicCache(w)
	page := s.backupChrome(w, r)
	page.NavActive = "audit" // the audit page gets its own nav tab (TASK-021)
	page.Breadcrumb = []breadcrumbItem{
		{Label: "خانه", Href: "/admin"},
		{Label: "پشتیبان‌گیری", Href: "/admin/backup"},
		{Label: "گزارش تغییرات"},
	}
	v := auditView{adminPage: page}
	v.Action = strings.TrimSpace(r.URL.Query().Get("action"))
	v.Entity = strings.TrimSpace(r.URL.Query().Get("entity"))
	v.Page = queryPage(r)
	v.ClearURL = auditURL("", "", 1)

	if s.store == nil {
		s.renderAdmin(w, r, "audit", v, http.StatusOK)
		return
	}

	// Filter options are built from a bounded unfiltered fetch so the operator can
	// always switch to a different filter (including clearing one). Their counts
	// cover the most recent auditFetchLimit rows; the rows themselves do not.
	options, err := s.store.AuditLog(auditFetchLimit)
	if err != nil {
		s.log.Error("audit log query failed", "error", err)
		writePersianError(w, http.StatusInternalServerError, auditDataErrorMessage)
		return
	}
	v.ActionOptions = auditOptions(options, func(e store.AuditEntry) string { return e.Action }, actionLabel)
	v.EntityOptions = auditOptions(options, func(e store.AuditEntry) string { return e.Entity }, entityLabel)

	// Newest first (id DESC in the store) and strictly read-only.
	offset := (v.Page - 1) * auditPageSize
	rows, total, err := s.store.AuditLogFiltered(v.Action, v.Entity, auditPageSize, offset)
	if err != nil {
		s.log.Error("audit log query failed", "error", err, "action", v.Action, "entity", v.Entity, "page", v.Page)
		writePersianError(w, http.StatusInternalServerError, auditDataErrorMessage)
		return
	}
	if offset >= total && v.Page > 1 {
		// A page past the end (or a stale link) shows the first page rather than
		// an empty table with pagination that points nowhere (TASK-014 contract).
		v.Page = 1
		rows, total, err = s.store.AuditLogFiltered(v.Action, v.Entity, auditPageSize, 0)
		if err != nil {
			s.log.Error("audit log query failed", "error", err, "action", v.Action, "entity", v.Entity, "page", 1)
			writePersianError(w, http.StatusInternalServerError, auditDataErrorMessage)
			return
		}
	}

	v.FilteredTotal = total
	for _, e := range rows {
		v.Rows = append(v.Rows, auditRow(e))
	}
	v.Shown = len(v.Rows)
	v.HasPrev = v.Page > 1
	v.PrevURL = auditURL(v.Action, v.Entity, v.Page-1)
	v.HasNext = v.Page*auditPageSize < total
	v.NextURL = auditURL(v.Action, v.Entity, v.Page+1)

	s.renderAdmin(w, r, "audit", v, http.StatusOK)
}

// ---- helpers ----

// auditRow turns a store entry into its presentation shape.
func auditRow(e store.AuditEntry) auditRowView {
	return auditRowView{
		ID:          e.ID,
		When:        jalaliDateTime(e.CreatedAt),
		Action:      e.Action,
		ActionLabel: actionLabel(e.Action),
		Entity:      e.Entity,
		EntityLabel: entityLabel(e.Entity),
		EntityID:    e.EntityID,
		Admin:       e.Admin,
		Change:      changeSummary(e),
	}
}

func actionLabel(action string) string {
	if l, ok := auditActionLabels[action]; ok {
		return l
	}
	return action
}

func entityLabel(entity string) string {
	if l, ok := auditEntityLabels[entity]; ok {
		return l
	}
	return entity
}

// auditOptions builds a sorted filter list with per-value counts.
func auditOptions(entries []store.AuditEntry, key func(store.AuditEntry) string, label func(string) string) []auditFilterOption {
	counts := map[string]int{}
	for _, e := range entries {
		if k := key(e); k != "" {
			counts[k]++
		}
	}
	opts := make([]auditFilterOption, 0, len(counts))
	for k, n := range counts {
		opts = append(opts, auditFilterOption{Value: k, Label: label(k), Count: n})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Value < opts[j].Value })
	return opts
}

// changeSummary renders the before/after pair as one short string.
func changeSummary(e store.AuditEntry) string {
	before := pickChangeValue(e.Before)
	after := pickChangeValue(e.After)
	switch {
	case before != "" && after != "" && before != after:
		return before + " ← " + after
	case after != "":
		return after
	case before != "":
		return before
	}
	return "—"
}

// pickChangeValue extracts the meaningful field from a store JSON payload.
// Path-like values are reduced to their base name so the audit page never exposes
// a host path (TASK-014 hard rule) even when BACKUP_DIR is absolute.
func pickChangeValue(raw *string) string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return ""
	}
	text := strings.TrimSpace(*raw)
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return truncateRunes(text, 80)
	}
	for _, key := range changeFieldOrder {
		v, ok := obj[key]
		if !ok {
			continue
		}
		s := scalarString(v)
		if s == "" {
			continue
		}
		if key == "dest" || key == "path" {
			s = filepath.Base(s)
		}
		return s
	}
	return truncateRunes(text, 80)
}

// scalarString renders the JSON scalars the store actually writes.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "فعال"
		}
		return "غیرفعال"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// truncateRunes shortens a string without tearing a UTF-8 sequence.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// queryPage parses ?page= (defaults to 1 for missing/invalid/negative input).
func queryPage(r *http.Request) int {
	p, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// auditURL builds a self-link that preserves the active filters. Values are
// URL-escaped so a Persian filter value round-trips safely.
func auditURL(action, entity string, page int) string {
	q := url.Values{}
	if action != "" {
		q.Set("action", action)
	}
	if entity != "" {
		q.Set("entity", entity)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/admin/audit"
	}
	return "/admin/audit?" + q.Encode()
}
