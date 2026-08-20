-- The picture on the sign-in screen.
--
-- The logo made a deployment's name theirs; this makes the screen theirs. It
-- is the largest thing anybody sees before signing in, and until now it was
-- the one element a reseller could not change — so a customer's login page
-- still showed our picture under their name, which is the seam this whole
-- table exists to close.
--
-- Empty means "use what this version ships", the same rule every other column
-- here follows: a release that improves the default still reaches a deployment
-- that never overrode it.
ALTER TABLE branding
    ADD COLUMN splash      BYTEA,
    ADD COLUMN splash_type TEXT NOT NULL DEFAULT '';
