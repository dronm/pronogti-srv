# Code generation schemas

This directory contains [codegen](https://github.com/dronm/codegen) object definitions. The root `codegen.yaml` selects this directory and the separate `pronogti-admin` frontend.

`salon.yaml` describes every column in the existing `public.salons` table, including the default bank, fiscal and YClients profile bindings. Its natural primary key is `company_id`; the database manages `session_version`, `created_at` and `updated_at`. The primary key is entered on creation and excluded from updates.

The object uses `/admin/salons`, distinct from the existing public directory route. Mount generated administrative routes with administrator authentication and permission enforcement when integrating the admin backend. The existing public/customer routes remain application-owned.

The frontend collection uses `editMode: page`. Once frontend scaffolding is generated, `SalonEditPage.vue` is a create-once application-owned page that can later host kiosk detail collections filtered by `company_id`. The current schema does not create kiosk schemas or detail components. Profile IDs are nullable numeric fields until their reference components are added.

Object migration generation is disabled because the table, company-scoped foreign keys and session-version trigger already exist in migrations 000004–000008. Keep those migrations authoritative. Deactivate salons through `is_active`; generic deletion is disabled.

The generator CLI can validate the definition with `codegen validate -config ./codegen.yaml`. The project does not declare Codegen as a Go tool yet; add that dependency before using `go tool codegen` or the corresponding Make target. No generated backend or frontend files are committed by this schema addition.
