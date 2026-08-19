# The 3CX XAPI schema

`openapi.yaml` is 3CX's own OpenAPI description of the v20 configuration API,
served by every PBX at `/xapi/v1/swagger.json`. It is vendored rather than
fetched because it is the contract this plugin is written against, and a
contract nobody can read is one everybody guesses at.

That is not hypothetical. Three bugs in one day came from writing property
names from memory:

- Two of the eight role names were invented. `departmentadmins` and `owners` do
  not exist; the phone system knows `group_admins` and `group_owners`, and
  refused both guesses while Azir's own form insisted they were valid.
- A role was written to `GroupRights`, which a `Pbx.UserGroup` carries alongside
  `Rights`. The phone system accepted it, validated the name, and changed
  nothing.
- Forwarding rules were written to `AvailableRoute` on every profile. Half the
  profiles carry `AwayRoute` instead and refused them.

`schema_test.go` beside the plugin checks every property name it reads or
writes against this file. All three would have failed at build time.

## Keeping it current

It is a snapshot, and a PBX on a newer build may know more than it does. Fetch
a fresh one from a customer's system and diff it:

    curl -s https://<pbx>/xapi/v1/swagger.json | yq -P > cmd/plugin-3cx/xapi/openapi.yaml

The test tells you what the plugin depends on, so a diff that touches none of
those names is a version bump and nothing more.
