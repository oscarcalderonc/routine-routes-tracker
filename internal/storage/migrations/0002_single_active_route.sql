-- Enforce that only one route template is active at a time.
--
-- The first waypoint creates the route on demand, so two requests arriving
-- together could each create one. Only the oldest is ever shown, which makes
-- waypoints added to the other invisible rather than reporting an error. The
-- constraint is placed in the database so the invariant holds regardless of how
-- many requests race.

-- Any template beyond the oldest active one is deactivated first, so that the
-- index below can be created on a database that already holds duplicates.
UPDATE route_templates SET active = 0
WHERE active = 1
  AND id <> (SELECT id FROM route_templates WHERE active = 1 ORDER BY created_at, id LIMIT 1);

CREATE UNIQUE INDEX route_templates_single_active
    ON route_templates (active) WHERE active = 1;
