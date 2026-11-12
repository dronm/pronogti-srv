import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { createServer, type ViteDevServer } from "vite";
import * as v from "valibot";

import { createFiscalProfileFormModel } from "../src/forms/fiscalProfile.gen";
import {
	fiscalPaymentMethodOptions,
	fiscalTaxSystemOptions,
	fiscalVatOptions,
} from "../src/forms/fiscalProfileOptions";
let server: ViteDevServer;

before(async () => {
	server = await createServer({
		ssr: { noExternal: ["@katren/vue-business-app", "@katren/vue-collection-lib"] },
		server: { middlewareMode: true, watch: null },
		appType: "custom",
	});
});
after(async () => {
	await server?.close();
});

test("fiscal profiles use a short collection and standalone create and edit routes", async () => {
	const { fiscalProfileCollection } = (await server.ssrLoadModule(
		"/src/collections/fiscalProfile.gen.ts",
	)) as typeof import("../src/collections/fiscalProfile.gen");
	assert.equal(fiscalProfileCollection.editMode, "page");
	assert.deepEqual(
		fiscalProfileCollection.columns.map(({ field }) => field),
		["name", "company_id", "mode", "enabled", "device_id"],
	);
	const { generatedRouteManifest } = (await server.ssrLoadModule(
		"/src/router/routeManifest.gen.ts",
	)) as typeof import("../src/router/routeManifest.gen");
	const routes = generatedRouteManifest.filter(({ route }) =>
		["fiscalProfileCreate", "fiscalProfileEdit"].includes(String(route.name)),
	);
	assert.deepEqual(
		routes.map(({ route }) => route.path),
		["/admin/fiscal-profiles/new", "/admin/fiscal-profiles/:id"],
	);
});

test("fiscal profile forms accept TerminalFA integer codes and keep PATCH fields optional", async () => {
	const { FiscalProfileNewSchema, FiscalProfileUpdSchema } = (await server.ssrLoadModule(
		"/src/schemas/fiscalProfile.gen.ts",
	)) as typeof import("../src/schemas/fiscalProfile.gen");
	const model = { ...createFiscalProfileFormModel(), company_id: 1, name: "Test profile" };
	const created = v.parse(FiscalProfileNewSchema, model);
	assert.equal(created.timeout_seconds, 100);
	assert.equal(created.payment_method, 4);
	assert.equal(created.driver_base_url, null);
	assert.equal(created.bearer_token_env, null);
	const connection = { driver_base_url: "http://127.0.0.1:8090", bearer_token_env: null };
	assert.deepEqual(v.parse(FiscalProfileUpdSchema, connection), connection);
	assert.equal(
		v.parse(FiscalProfileNewSchema, { ...model, ...connection }).driver_base_url,
		connection.driver_base_url,
	);

	for (const { value } of fiscalVatOptions) {
		assert.deepEqual(v.parse(FiscalProfileUpdSchema, { service_vat_rate: value, good_vat_rate: value }), {
			service_vat_rate: value,
			good_vat_rate: value,
		});
	}
	for (const { value } of fiscalTaxSystemOptions) {
		assert.equal(v.parse(FiscalProfileUpdSchema, { tax_system: value }).tax_system, value);
	}
	for (const { value } of fiscalPaymentMethodOptions) {
		assert.equal(v.parse(FiscalProfileUpdSchema, { payment_method: value }).payment_method, value);
	}
	assert.deepEqual(v.parse(FiscalProfileUpdSchema, { name: "Renamed" }), { name: "Renamed" });
	for (const value of [
		{ tax_system: 8 },
		{ tax_system: "1" },
		{ service_vat_rate: 13 },
		{ good_vat_rate: 0 },
		{ payment_method: 8 },
		{ timeout_seconds: 301 },
		{ timeout_seconds: 0.5 },
	]) {
		assert.equal(v.safeParse(FiscalProfileUpdSchema, value).success, false);
	}
});
