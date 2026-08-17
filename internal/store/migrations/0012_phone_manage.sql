-- Changing a customer's phone system is its own permission.
--
-- Not folded into tool.write, because the two are different sizes of mistake:
-- commenting on the wrong ticket is embarrassing, disabling the wrong extension
-- takes a business's phones off the air.
--
-- Granted to administrators only. A deployment that wants its technicians to
-- have it can add it to the technician role on the People tab — that is the
-- point of permissions being data rather than a name in the code.
UPDATE roles
SET permissions = array_append(permissions, 'phone.manage')
WHERE name = 'admin' AND NOT ('phone.manage' = ANY (permissions));
