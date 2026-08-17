-- Technicians may apply what the assistant proposes.
--
-- 0012 gave phone.manage to administrators only, reasoning that disabling the
-- wrong extension takes a business's phones off the air. True, and the wrong
-- conclusion: this product exists to help technicians, and a change a
-- technician cannot approve is a change they have to leave the app to make —
-- which is both slower and less recorded than doing it here.
--
-- The boundary belongs one level up, and already exists there. An administrator
-- decides whether a plugin may write at all, which of its tools are approved,
-- and who holds which role. Inside that, a technician does the work. Splitting
-- it the other way put the safety check on the person doing the job rather than
-- on the policy, which is the wrong half.
UPDATE roles
SET permissions = array_append(permissions, 'phone.manage')
WHERE name = 'technician' AND NOT ('phone.manage' = ANY (permissions));
