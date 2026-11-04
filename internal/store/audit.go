package store

import (
	"context"
	"database/sql"
	"strings"
)

// AuditEvent is one recorded action. Empty strings are stored as NULL, so an
// account-level event with no organisation is not attributed to one.
//
// ActorName, ActorEmail and TargetLabel are what the actor and the target
// were called when the event was recorded: the log still reads correctly
// after a person leaves, is renamed, or a key is deleted with its
// organisation. recordAudit fills them in; a caller does not.
type AuditEvent struct {
	ID          string `json:"id"`
	OrgID       string `json:"orgId"`
	ActorID     string `json:"actorId"`
	ActorName   string `json:"actorName,omitempty"`
	ActorEmail  string `json:"actorEmail,omitempty"`
	Action      string `json:"action"`
	Target      string `json:"target"`
	TargetLabel string `json:"targetLabel,omitempty"`
	Metadata    string `json:"metadata"`
	IP          string `json:"ip"`
	CreatedAt   int64  `json:"createdAt"`
}

// RecordAudit appends an event.
func (s *Store) RecordAudit(ctx context.Context, e AuditEvent) error {
	return recordAudit(ctx, s.db, e)
}

// recordAudit writes an event with its names as they are now. The target's
// label depends on what the action is about: a key's label, an invited
// address, a member's name, or the organisation's name.
func recordAudit(ctx context.Context, q queryer, e AuditEvent) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO audit_event (id, org_id, actor_id, action, target, metadata, ip, created_at,
		                          actor_name, actor_email, target_label)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8,
		   (SELECT name FROM app_user WHERE id = ?3),
		   (SELECT email FROM app_user WHERE id = ?3),
		   CASE
		     WHEN ?4 LIKE 'api\_key.%' ESCAPE '\'
		       THEN (SELECT label FROM api_key WHERE id = ?5)
		     WHEN ?4 = 'member.invited' OR ?4 LIKE 'invitation.%'
		       THEN (SELECT email FROM invitation WHERE id = ?5)
		     WHEN ?4 LIKE 'member.%'
		       THEN (SELECT coalesce(nullif(trim(name), ''), email) FROM app_user WHERE id = ?5)
		     WHEN ?4 LIKE 'org.%'
		       THEN (SELECT name FROM org WHERE id = ?2)
		   END)`,
		newEventID(), nullable(e.OrgID), nullable(e.ActorID), e.Action,
		nullable(e.Target), nullable(e.Metadata), nullable(e.IP), nowMillis(),
	)
	return err
}

const auditColumns = `id, org_id, actor_id, actor_name, actor_email, action, target, target_label, metadata, ip, created_at`

func scanAudit(rows *sql.Rows) (AuditEvent, error) {
	var (
		e                                     AuditEvent
		org, actor, actorName, actorEmail     sql.NullString
		target, targetLabel, metadata, remote sql.NullString
	)
	err := rows.Scan(&e.ID, &org, &actor, &actorName, &actorEmail, &e.Action, &target, &targetLabel, &metadata, &remote, &e.CreatedAt)
	e.OrgID, e.ActorID, e.ActorName, e.ActorEmail = nullString(org), nullString(actor), nullString(actorName), nullString(actorEmail)
	e.Target, e.TargetLabel, e.Metadata, e.IP = nullString(target), nullString(targetLabel), nullString(metadata), nullString(remote)
	return e, err
}

// RecentAudit returns an org's newest events, capped so a hostile limit
// cannot turn one request into a full table scan.
func (s *Store) RecentAudit(ctx context.Context, orgID string, limit int) ([]AuditEvent, error) {
	page, err := s.QueryAudit(ctx, orgID, AuditFilter{}, 0, limit)
	return page.Events, err
}

// AuditCategories are the groups of actions the log can be narrowed to.
var AuditCategories = []string{"keys", "members", "invitations", "organisation"}

// AuditFilter narrows an organisation's audit log. A zero field does not
// narrow it.
type AuditFilter struct {
	// Category is one of AuditCategories.
	Category string
	ActorID  string
	// Target matches the target's id exactly, or any part of its label at
	// the time, ignoring the case of ASCII letters.
	Target string
	// Since and Until bound the time the event was recorded, in epoch
	// milliseconds: Since inclusive, Until exclusive.
	Since, Until int64
}

// AuditPage is one page of the events a filter matches, newest first.
type AuditPage struct {
	Events []AuditEvent
	// Total is how many events the filter matches across every page.
	Total int
	// Earliest is when the organisation's oldest kept event was recorded,
	// whatever the filter; 0 when it has none.
	Earliest int64
}

// MaxAuditPage is the most events one page returns.
const MaxAuditPage = 500

// auditMatch is the filter as SQL: ?1 the organisation, then the filter's
// fields in AuditFilter's order. One constant statement serves every
// combination, so it is prepared once.
const auditMatch = `org_id = ?1
   AND (?2 = '' OR CASE ?2
         WHEN 'keys' THEN action LIKE 'api\_key.%' ESCAPE '\'
         WHEN 'invitations' THEN action = 'member.invited' OR action LIKE 'invitation.%'
         WHEN 'members' THEN action LIKE 'member.%' AND action <> 'member.invited'
         WHEN 'organisation' THEN action LIKE 'org.%'
         ELSE 0 END)
   AND (?3 = '' OR actor_id = ?3)
   AND (?4 = '' OR target = ?4 OR instr(lower(coalesce(target_label, '')), lower(?4)) > 0)
   AND (?5 = 0 OR created_at >= ?5)
   AND (?6 = 0 OR created_at < ?6)`

// QueryAudit returns one page of an organisation's events matching f,
// newest first, with how many match in all; all from one snapshot, so the
// count and the page agree. limit is capped at MaxAuditPage.
func (s *Store) QueryAudit(ctx context.Context, orgID string, f AuditFilter, offset, limit int) (AuditPage, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, MaxAuditPage)
	offset = max(offset, 0)
	args := []any{orgID, f.Category, f.ActorID, strings.TrimSpace(f.Target), f.Since, f.Until}

	var page AuditPage
	err := s.read.snapshot(ctx, func(q queryer) error {
		var earliest sql.NullInt64
		err := q.QueryRowContext(ctx,
			`SELECT count(*), (SELECT min(created_at) FROM audit_event WHERE org_id = ?1)
			   FROM audit_event WHERE `+auditMatch, args...).Scan(&page.Total, &earliest)
		if err != nil {
			return err
		}
		page.Earliest = earliest.Int64

		rows, err := q.QueryContext(ctx,
			`SELECT `+auditColumns+` FROM audit_event WHERE `+auditMatch+`
			  ORDER BY created_at DESC, id DESC LIMIT ?7 OFFSET ?8`, append(args, limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		page.Events = []AuditEvent{}
		for rows.Next() {
			e, err := scanAudit(rows)
			if err != nil {
				return err
			}
			page.Events = append(page.Events, e)
		}
		return rows.Err()
	})
	return page, err
}

// AuditActor is someone who has done something in an organisation, named as
// in their most recent event.
type AuditActor struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// AuditActors lists everyone with an event in the organisation's log,
// including people who have since left it.
func (s *Store) AuditActors(ctx context.Context, orgID string) ([]AuditActor, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT actor_id, actor_name, actor_email, max(created_at)
		   FROM audit_event WHERE org_id = ? AND actor_id IS NOT NULL
		  GROUP BY actor_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditActor{}
	for rows.Next() {
		var (
			a           AuditActor
			name, email sql.NullString
			latest      int64
		)
		if err := rows.Scan(&a.ID, &name, &email, &latest); err != nil {
			return nil, err
		}
		a.Name, a.Email = nullString(name), nullString(email)
		out = append(out, a)
	}
	return out, rows.Err()
}
