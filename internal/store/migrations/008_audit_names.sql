-- Who and what an audit event was about, by the names they had at the time.
--
-- An event names its actor and target by id. Read back later, an id whose
-- person has left the organisation, or whose key or invitation is long gone,
-- tells an auditor nothing, so each event now also records the actor's name
-- and email address and a label for the target when it is written:
-- recordAudit fills them in from the rows themselves.
ALTER TABLE audit_event ADD COLUMN actor_name TEXT;
ALTER TABLE audit_event ADD COLUMN actor_email TEXT;
ALTER TABLE audit_event ADD COLUMN target_label TEXT;

-- Events recorded before this migration get the names current when it runs:
-- later than the event, but better than none. A row that has gone since
-- leaves the column empty, as before.
UPDATE audit_event
   SET actor_name = (SELECT name FROM app_user WHERE app_user.id = audit_event.actor_id),
       actor_email = (SELECT email FROM app_user WHERE app_user.id = audit_event.actor_id)
 WHERE actor_id IS NOT NULL;

UPDATE audit_event
   SET target_label = CASE
     WHEN action LIKE 'api\_key.%' ESCAPE '\'
       THEN (SELECT label FROM api_key WHERE api_key.id = audit_event.target)
     WHEN action = 'member.invited' OR action LIKE 'invitation.%'
       THEN (SELECT email FROM invitation WHERE invitation.id = audit_event.target)
     WHEN action LIKE 'member.%'
       THEN (SELECT coalesce(nullif(trim(name), ''), email) FROM app_user WHERE app_user.id = audit_event.target)
     WHEN action LIKE 'org.%'
       THEN (SELECT name FROM org WHERE org.id = audit_event.org_id)
   END
 WHERE target_label IS NULL;
