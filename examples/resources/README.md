# Resource examples

Each `onpager_*` directory supplies a resource page's usage and import snippets.
For creation, copy `resource.tf` and `terraform.tf`, configure provider credentials,
and supply the declared input variables. Run `terraform init`,
`terraform validate` and `terraform plan` in the copied directory.

Omit `import-by-string-id.tf` during creation: it is a separate import example
using synthetic UUIDs. To adopt an existing object, replace those IDs with the
object's actual import identity and include the file with the matching resource
configuration. `import.sh` shows the alternative CLI command. Use the documented
composite import ID where a resource requires parent and child IDs.

`task docs` and `task docs:check` validate all four source files without applying
them or contacting OnPager. The complete root configuration is in
[`team-configuration/`](../team-configuration/README.md).
