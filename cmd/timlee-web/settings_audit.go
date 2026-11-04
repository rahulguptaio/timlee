package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
	"github.com/rahulguptaio/timlee/internal/i18n"
)

// Audit log exports: the events the page's filters match, as a file to keep
// or to load into another tool. CSV and JSON carry the same fields, named as
// GitHub's and Vercel's audit exports name theirs, with the actor and the
// target as they were called when each event was recorded.

// auditRecord is one event as an export carries it.
type auditRecord struct {
	TimeUTC     string          `json:"time_utc"`
	ActorID     string          `json:"actor_id"`
	ActorName   string          `json:"actor_name"`
	ActorEmail  string          `json:"actor_email"`
	Action      string          `json:"action"`
	Description string          `json:"description"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	TargetLabel string          `json:"target_label"`
	Details     json.RawMessage `json:"details"`
}

var auditExportColumns = []string{
	"time_utc", "actor_id", "actor_name", "actor_email", "action", "description",
	"target_type", "target_id", "target_label", "details",
}

// settingsAuditExport answers /settings/audit/export.csv and .json with
// every event the query's filters match, newest first, up to
// auditExportCap.
func (s *server) settingsAuditExport(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sr, ok := s.beginSettings(w, r, "audit")
		if !ok {
			return
		}
		filters := auditFiltersOf(r.URL.Query())
		back := auditHref("/settings/audit", filters.values())
		if !sr.shell.CanManage() {
			// The page says who can read the log.
			sr.up.relay(w)
			http.Redirect(w, r, "/settings/audit", http.StatusSeeOther)
			return
		}

		now := time.Now()
		l := sr.page.L
		since, until, valid := filters.window(now, l, sr.shell)
		if !valid {
			// The page explains what is wrong with the range.
			sr.up.relay(w)
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		if until == 0 {
			// Pinned, so events recorded while the export pages through the
			// log do not shift the pages under it.
			until = now.UnixMilli() + 1
		}

		var (
			events []auditEvent
			total  int
		)
		for len(events) < auditExportCap {
			page, err := sr.up.Audit(r.Context(), auditQuery{
				Category: filters.Category, Actor: filters.Actor, Target: filters.Target,
				Since: since, Until: until,
				Offset: len(events), Limit: min(auditFetchSize, auditExportCap-len(events)),
			})
			if err != nil {
				s.log.Warn("could not export audit log", "error", err, "request_id", httpx.IDOf(r))
				s.done(w, r, sr.up, back, flash{Tone: "danger", Message: l.T("settings.audit.exportFailed")})
				return
			}
			total = page.Total
			events = append(events, page.Events...)
			if len(page.Events) == 0 || len(events) >= total {
				break
			}
		}

		members := map[string]member{}
		if sr.org != nil {
			for _, m := range sr.org.Members {
				members[m.ID] = m
			}
		}
		records := make([]auditRecord, 0, len(events))
		for _, e := range events {
			records = append(records, recordOf(e, members, l, now))
		}

		name := "timlee-audit-" + now.UTC().Format(dateLayout) + "." + format
		sr.up.relay(w)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			out := csv.NewWriter(w)
			_ = out.Write(auditExportColumns)
			for _, rec := range records {
				_ = out.Write([]string{
					rec.TimeUTC, cell(rec.ActorID), cell(rec.ActorName), cell(rec.ActorEmail), cell(rec.Action),
					cell(rec.Description), cell(rec.TargetType), cell(rec.TargetID), cell(rec.TargetLabel),
					cell(string(rec.Details)),
				})
			}
			out.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		window := map[string]any{"since": nil, "until": nil}
		if since > 0 {
			window["since"] = time.UnixMilli(since).UTC().Format(time.RFC3339)
		}
		if filters.To != "" {
			window["until"] = time.UnixMilli(until).UTC().Format(time.RFC3339)
		}
		org := map[string]string{}
		if sr.shell.Org != nil {
			org["id"], org["name"] = sr.shell.Org.ID, sr.shell.Org.Name
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"organisation": org,
			"exportedAt":   now.UTC().Format(time.RFC3339),
			"filters": map[string]any{
				"category": filters.Category, "actor": filters.Actor, "target": filters.Target,
				"since": window["since"], "until": window["until"],
			},
			"total":     total,
			"truncated": total > len(records),
			"events":    records,
		})
	}
}

// recordOf is one event as an export carries it. The names are those
// recorded with the event; an event older than them gets the names the
// organisation knows now, or none.
func recordOf(e auditEvent, members map[string]member, l *i18n.Localizer, now time.Time) auditRecord {
	row := describe(e, members, l, now)
	rec := auditRecord{
		TimeUTC:     time.UnixMilli(e.CreatedAt).UTC().Format("2006-01-02T15:04:05.000Z"),
		ActorID:     e.ActorID,
		ActorName:   e.ActorName,
		ActorEmail:  e.ActorEmail,
		Action:      e.Action,
		Description: row.Action,
		TargetType:  targetType(e.Action),
		TargetID:    e.Target,
		TargetLabel: targetName(e, members, metadataOf(e)),
		Details:     json.RawMessage("null"),
	}
	if row.Details != "" {
		rec.Description += " (" + row.Details + ")"
	}
	if m, ok := members[e.ActorID]; ok && rec.ActorName == "" && rec.ActorEmail == "" {
		rec.ActorName, rec.ActorEmail = m.Name, m.Email
	}
	if e.Metadata != "" && json.Valid([]byte(e.Metadata)) {
		rec.Details = json.RawMessage(e.Metadata)
	}
	return rec
}

// cell keeps a value from being run as a formula by a spreadsheet that
// opens the export, as OWASP advises for CSV: a value that starts with a
// character a spreadsheet reads as the start of a formula gets an
// apostrophe in front. Names, labels and addresses are other people's
// text.
func cell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
