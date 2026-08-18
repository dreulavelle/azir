-- Seeing what Azir is holding, and clearing it, is its own permission.
--
-- Not folded into any existing one, because none of them is the right size.
-- audit.read is for reading the log, not for emptying it. user.manage is about
-- people. plugin.configure is about connections. Clearing every conversation,
-- every diagnostic capture and the audit trail itself is a different kind of
-- authority from all three, and the only honest way to say so is a permission
-- that means exactly that.
--
-- Administrators only, and deliberately not added to the technician role. A
-- deployment that wants to delegate it can, on the People tab — but it should
-- be a decision somebody makes, not a default they inherit.
UPDATE roles
SET permissions = array_append(permissions, 'data.manage')
WHERE name = 'admin' AND NOT ('data.manage' = ANY (permissions));
