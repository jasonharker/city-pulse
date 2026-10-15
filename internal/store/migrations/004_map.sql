-- One map pin per building: a place and its units share a pin. The pin links to the
-- building's own place when there is one, else to its first unit, and sits on the
-- building's point, else on the average of its units'. ReplaceResolved refreshes it.
CREATE MATERIALIZED VIEW pins AS
SELECT coalesce(max(id) FILTER (WHERE id = building), min(id))           AS id,
       coalesce(max(address) FILTER (WHERE id = building), min(address)) AS address,
       coalesce(max(lat) FILTER (WHERE id = building), avg(lat))         AS lat,
       coalesce(max(lon) FILTER (WHERE id = building), avg(lon))         AS lon,
       count(*)::int                                                     AS places
FROM (SELECT *, regexp_replace(id, '[^|]*$', '') AS building FROM places) p
GROUP BY building
HAVING count(lat) > 0;

CREATE INDEX pins_lat_lon_idx ON pins (lat, lon);
