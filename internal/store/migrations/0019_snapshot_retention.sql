-- Snapshots expire, and no longer need a customer to exist at all.
--
-- Two changes, for two reasons.
--
-- A capture arrives before anybody has decided whose it is. A technician is
-- handed a zip and wants to know what is wrong with it; making them create a
-- customer record first, or pick the wrong one from a list to get past the
-- form, is a worse answer than reading the bundle and asking afterwards. The
-- bundle usually knows anyway — it carries the phone system's own FQDN, which
-- is the address Azir already stores per customer — so most of them attach
-- themselves and the rest can be attached later.
--
-- And they expire. Not for space: a report is a few hundred kilobytes and a
-- thousand of them would not be noticed. For what is in them. A report carries
-- the extension numbers, MAC addresses, internal IP ranges, caller IDs and the
-- names of the people who administer a customer's phone system. That is a
-- liability to hold forever to answer a ticket that closed a fortnight ago, and
-- the honest default for diagnostic data is that it goes away on its own.
--
-- Anything worth keeping can be kept: clearing expires_at pins a snapshot, for
-- the one that turned out to explain something.

ALTER TABLE snapshots ALTER COLUMN customer_id DROP NOT NULL;

ALTER TABLE snapshots ADD COLUMN expires_at TIMESTAMPTZ;

-- The FQDN the capture came off, which is what attached it to a customer and
-- is worth showing on one that could not be attached to anybody.
ALTER TABLE snapshots ADD COLUMN fqdn TEXT NOT NULL DEFAULT '';

-- Existing rows get the same fortnight everything else now gets.
UPDATE snapshots SET expires_at = uploaded_at + INTERVAL '14 days' WHERE expires_at IS NULL;

CREATE INDEX snapshots_expiry ON snapshots (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX snapshots_unattached ON snapshots (uploaded_at DESC) WHERE customer_id IS NULL;
