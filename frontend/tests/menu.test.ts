import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer, type ViteDevServer } from "vite";
import vue from "@vitejs/plugin-vue";
import * as v from "valibot";
import { createMemoryHistory, createRouter } from "vue-router";
import {
	createRenderer,
	defineComponent,
	ssrContextKey,
	type Component,
	type ComponentPublicInstance,
} from "vue";
import { businessAppApi } from "../src/api/businessApp";
import api from "../src/api/http";

let server: ViteDevServer;

before(async () => {
	server = await createServer({
		configFile: false,
		plugins: [vue()],
		resolve: {
			alias: {
				"@": fileURLToPath(
					new URL("../src", import.meta.url),
				),
			},
		},
		ssr: {
			noExternal: [
				"@katren/vue-business-app",
				"@katren/vue-collection-lib",
			],
		},
		optimizeDeps: { noDiscovery: true },
		server: { middlewareMode: true, watch: null },
		appType: "custom",
	});
});

after(async () => {
	await server?.close();
});

const sharedModule = (path: string): string =>
	fileURLToPath(
		new URL(
			`../../../packages/vue-business-app/src/${path}`,
			import.meta.url,
		),
	);

test("the default administrator menu exposes all five actions", async () => {
	const { default: component } = await server.ssrLoadModule(
		"/src/components/AdministratorMenu.vue",
	);
	let renderedProps: Record<string, unknown> | undefined;
	const menuStub = defineComponent({
		props: [
			"canViewProfile",
			"canManageMenu",
			"canViewApplicationRoutes",
			"onProfile",
			"onConstructor",
			"onAllRoutes",
			"onAbout",
			"onLogout",
		],
		setup: (props) => {
			renderedProps = { ...props };
			return () => null;
		},
	});
	const emitted: string[] = [];
	component.ssrRender(
		null,
		() => {},
		null,
		{},
		{ userName: "admin" },
		{
			HorizontalMainMenu: menuStub,
			menu: [],
			menuError: null,
			emit: (action: string) => emitted.push(action),
		},
	);
	assert.ok(renderedProps);
	assert.equal(renderedProps.canViewProfile, true);
	assert.equal(renderedProps.canManageMenu, true);
	assert.equal(renderedProps.canViewApplicationRoutes, true);
	for (const action of [
		"Profile",
		"Constructor",
		"AllRoutes",
		"About",
		"Logout",
	]) {
		(renderedProps[`on${action}`] as () => void)();
	}
	assert.deepEqual(emitted, [
		"profile",
		"constructor",
		"allRoutes",
		"about",
		"logout",
	]);
});

test("shared administration views are lazy, registered and restricted to administrators", async () => {
	const { applicationRouteManifest, applicationRouteRecords } =
		await server.ssrLoadModule("/src/router/routeManifest.ts");
	for (const [name, path, exportName] of [
		[
			"mainMenuConstructor",
			"/main-menu-constructor",
			"MainMenuConstructorView",
		],
		[
			"applicationRoutes",
			"/application-routes",
			"ApplicationRoutesView",
		],
	]) {
		const route = applicationRouteRecords.find(
			(item: { name: string }) => item.name === name,
		);
		assert.equal(route.path, path);
		assert.equal(route.meta.roleId, "admin");
		assert.equal(typeof route.component, "function");
		const view = await route.component();
		assert.equal(view.__name, exportName);
		const registry = applicationRouteManifest.find(
			(item: { name: string }) => item.name === name,
		);
		assert.equal(registry.path, route.path);
		assert.equal(
			registry.menu_available,
			name === "applicationRoutes",
		);
	}
	assert.ok(
		applicationRouteManifest.some(
			(item: { name: string }) => item.name === "userProfile",
		),
	);
});

test("shared menu, form registry and user lookup APIs retain their real administrator contracts", async () => {
	const { createMainMenuApi } = await server.ssrLoadModule(
		sharedModule("menu/api.ts"),
	);
	const { createApplicationRouteApi } = await server.ssrLoadModule(
		sharedModule("application-routes/api.ts"),
	);
	const { createUserApi } = await server.ssrLoadModule(
		sharedModule("user/api.ts"),
	);
	const menus = createMainMenuApi(businessAppApi, v.literal("admin"));
	const routes = createApplicationRouteApi(businessAppApi);
	const users = createUserApi(businessAppApi, v.literal("admin"));
	const originalFetch = globalThis.fetch;
	const requests: Array<{
		url: URL;
		method: string;
		headers: Headers;
		body: unknown;
	}> = [];
	const createdAt = "2026-11-04T00:00:00Z";
	const menuInput = {
		role_id: "admin",
		user_id: null,
		parent_id: null,
		route_id: 1,
		caption: "Home",
		icon: null,
		sort_order: 0,
		is_active: true,
	};
	const menuDetail = {
		id: 1,
		...menuInput,
		created_at: createdAt,
		updated_at: createdAt,
	};
	const routeDetail = {
		id: 1,
		name: "main",
		path: "/",
		descr: "Home",
		section: "Forms",
		icon: null,
		menu_available: true,
		is_active: true,
		created_at: createdAt,
		updated_at: createdAt,
	};
	const user = { id: 1, name: "admin", role_id: "admin" };
	globalThis.fetch = async (input, options) => {
		const url = new URL(String(input));
		const method = options?.method ?? "GET";
		requests.push({
			url,
			method,
			headers: new Headers(options?.headers),
			body: options?.body
				? JSON.parse(String(options.body))
				: undefined,
		});
		let response: unknown = { rows_affected: 1 };
		if (url.pathname === "/admin/main-menu") {
			response = [
				{
					id: 1,
					caption: "Home",
					route: "main",
					icon: null,
					children: null,
				},
			];
		} else if (
			url.pathname === "/admin/main-menus/routes/autocomplete"
		) {
			response = [routeDetail];
		} else if (
			url.pathname === "/admin/main-menus/1" &&
			method === "GET"
		) {
			response = menuDetail;
		} else if (url.pathname === "/admin/main-menus") {
			response =
				method === "POST"
					? { id: 1 }
					: {
							rows: [
								{
									...menuDetail,
									users_ref: null,
									main_menus_ref:
										null,
									application_routes_ref:
										{
											keys: {
												id: 1,
											},
											descr: "Home",
										},
									route_name: "main",
								},
							],
							agg: { totalCount: 1 },
						};
		} else if (url.pathname === "/admin/application-routes/sync") {
			response = { registered: 4, deactivated: 0 };
		} else if (url.pathname === "/admin/application-routes") {
			response = {
				rows: [routeDetail],
				agg: { totalCount: 1 },
			};
		} else if (
			url.pathname === "/admin/application-routes/1" &&
			method === "GET"
		) {
			response = routeDetail;
		} else if (url.pathname === "/admin/users") {
			response = { rows: [user], agg: { totalCount: 1 } };
		} else if (url.pathname === "/admin/users/1") {
			response = user;
		}
		return new Response(JSON.stringify(response), {
			headers: { "Content-Type": "application/json" },
		});
	};
	try {
		api.setSessionToken("navigation-session-csrf-nonce-1234567890");
		const params = {
			from: 0,
			count: 10000,
			sorter: [{ f: "sort_order", d: "a" }],
		};
		assert.equal(
			(await menus.list(params)).rows[0]
				.application_routes_ref.keys.id,
			1,
		);
		assert.deepEqual(
			JSON.parse(
				requests[0]!.url.searchParams.get("params")!,
			),
			params,
		);
		assert.equal((await menus.detail({ id: 1 })).caption, "Home");
		assert.deepEqual(await menus.create(menuInput), { id: 1 });
		assert.deepEqual(requests.at(-1)?.body, menuInput);
		await menus.update({ id: 1 }, { caption: "Changed" });
		assert.equal(requests.at(-1)?.method, "PATCH");
		assert.deepEqual(requests.at(-1)?.body, { caption: "Changed" });
		await menus.delete({ id: 1 });
		assert.equal(requests.at(-1)?.method, "DELETE");
		assert.equal((await menus.forUser())[0].route, "main");
		assert.equal(
			(await menus.routeAutocomplete(" Home "))[0].name,
			"main",
		);
		assert.equal(
			requests.at(-1)?.url.searchParams.get("q"),
			"Home",
		);
		assert.equal((await routes.list()).rows[0].name, "main");
		assert.equal((await routes.detail({ id: 1 })).path, "/");
		await routes.update({ id: 1 }, { descr: "Main page" });
		const { applicationRouteManifest } = await server.ssrLoadModule(
			"/src/router/routeManifest.ts",
		);
		assert.deepEqual(await routes.sync(applicationRouteManifest), {
			registered: 4,
			deactivated: 0,
		});
		assert.deepEqual(requests.at(-1)?.body, {
			items: applicationRouteManifest,
		});
		assert.deepEqual((await users.list()).rows, [user]);
		assert.deepEqual(await users.detail({ id: 1 }), user);
		for (const request of requests) {
			assert.equal(
				request.headers.get("X-Admin-Session"),
				request.method === "GET"
					? null
					: "navigation-session-csrf-nonce-1234567890",
			);
		}
	} finally {
		api.setSessionToken(null);
		globalThis.fetch = originalFetch;
	}
});

const deferred = <T>() => {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((done) => {
		resolve = done;
	});
	return { promise, resolve };
};

const flush = async (): Promise<void> => {
	await new Promise<void>((resolve) => setImmediate(resolve));
};

test("menu changes reload the shared menu and discarded sessions cannot replace a new menu", async () => {
	const originalWindow = globalThis.window;
	const events = new EventTarget();
	Object.defineProperty(globalThis, "window", {
		configurable: true,
		value: events,
	});
	const requests: ReturnType<typeof deferred<unknown[]>>[] = [];
	const renderer = createRenderer<
		Record<string, unknown>,
		Record<string, unknown>
	>({
		insert: () => {},
		remove: () => {},
		createElement: () => ({}),
		createText: () => ({}),
		createComment: () => ({}),
		setText: () => {},
		setElementText: () => {},
		parentNode: () => null,
		nextSibling: () => null,
		patchProp: () => {},
	});
	const { default: component } = await server.ssrLoadModule(
		"/src/components/AdministratorMenu.vue",
	);
	const { createBusinessApp } = await server.ssrLoadModule(
		sharedModule("core/runtime.ts"),
	);
	const plugin = createBusinessApp({
		api: {
			get: async (endpoint: string) => {
				assert.equal(endpoint, "/main-menus/for-user");
				const request = deferred<unknown[]>();
				requests.push(request);
				return request.promise;
			},
		},
		branding: { title: "Pronogti" },
	});
	// Render-free host runs the actual component setup and lifecycle. The visual
	// menu itself is covered separately by its compiled template contract above.
	component.render = () => null;
	const mount = () => {
		const app = renderer.createApp(component as Component, {
			userName: "admin",
		});
		app.provide(ssrContextKey, { modules: new Set() });
		app.use(plugin);
		const instance = app.mount({}) as ComponentPublicInstance;
		const state = instance.$.setupState;
		return { app, state };
	};
	let oldSession: ReturnType<typeof mount> | undefined;
	let newSession: ReturnType<typeof mount> | undefined;
	const menu = (caption: string) => [
		{ id: 1, caption, route: "main", icon: null, children: null },
	];
	try {
		oldSession = mount();
		assert.equal(requests.length, 1);
		requests[0]!.resolve(menu("Before edit"));
		await flush();
		assert.equal(oldSession.state.menu[0].caption, "Before edit");
		events.dispatchEvent(new Event("main-menu-changed"));
		assert.equal(requests.length, 2);
		requests[1]!.resolve(menu("After edit"));
		await flush();
		assert.equal(oldSession.state.menu[0].caption, "After edit");

		events.dispatchEvent(new Event("main-menu-changed"));
		assert.equal(requests.length, 3);
		oldSession.app.unmount();
		oldSession = undefined;
		events.dispatchEvent(new Event("main-menu-changed"));
		assert.equal(
			requests.length,
			3,
			"logout removes the menu-change listener",
		);
		newSession = mount();
		assert.equal(requests.length, 4);
		assert.deepEqual(newSession.state.menu, []);
		requests[3]!.resolve(menu("New session"));
		await flush();
		requests[2]!.resolve(menu("Stale session response"));
		await flush();
		assert.equal(newSession.state.menu[0].caption, "New session");
	} finally {
		oldSession?.app.unmount();
		newSession?.app.unmount();
		Object.defineProperty(globalThis, "window", {
			configurable: true,
			value: originalWindow,
		});
	}
});

test("salon edit loads named references and copying clears company-owned defaults", async () => {
	const { applicationRouteRecords } = await server.ssrLoadModule(
		"/src/router/routeManifest.ts",
	);
	const { salonCollection } = await server.ssrLoadModule(
		"/src/collections/salon.gen.ts",
	);
	const { default: component } = await server.ssrLoadModule(
		"/src/views/salon/SalonEditPage.vue",
	);
	const { i18n } = await server.ssrLoadModule("/src/i18n/index.ts");
	const router = createRouter({
		history: createMemoryHistory(),
		routes: applicationRouteRecords.map((route: Record<string, unknown>) => ({
			...route,
			component: defineComponent({ setup: () => () => null }),
		})),
	});
	const originalFetch = globalThis.fetch;
	const requested: string[] = [];
	const company = { keys: { id: 781898 }, descr: "Company One" };
	const payment = { keys: { id: 42 }, descr: "Payment One" };
	const fiscal = { keys: { id: 43 }, descr: "Fiscal One" };
	const yclients = { keys: { id: 44 }, descr: "YClients One" };
	globalThis.fetch = async (input) => {
		const path = new URL(String(input)).pathname;
		requested.push(path);
		return new Response(JSON.stringify({
			company_id: 781898,
			slug: "salon-one",
			name: "Salon One",
			is_active: true,
			session_version: 1,
			sort_order: 0,
			created_at: "2026-11-04T00:00:00Z",
			updated_at: "2026-11-04T00:00:00Z",
			default_payment_profile_id: 42,
			default_fiscal_profile_id: 43,
			default_yclients_profile_id: 44,
			company,
			default_payment_profile: payment,
			default_fiscal_profile: fiscal,
			default_yclients_profile: yclients,
		}), { headers: { "Content-Type": "application/json" } });
	};
	const renderer = createRenderer<Record<string, unknown>, Record<string, unknown>>({
		insert: () => {},
		remove: () => {},
		createElement: () => ({}),
		createText: () => ({}),
		createComment: () => ({}),
		setText: () => {},
		setElementText: () => {},
		parentNode: () => null,
		nextSibling: () => null,
		patchProp: () => {},
	});
	component.render = () => null;
	const mount = () => {
		const app = renderer.createApp(component as Component);
		app.provide(ssrContextKey, { modules: new Set() });
		app.use(router);
		app.use(i18n);
		const instance = app.mount({}) as ComponentPublicInstance;
		return { app, state: instance.$.setupState };
	};
	let mounted: ReturnType<typeof mount> | undefined;
	try {
		const editTarget = salonCollection.routes.edit({ company_id: 781898 });
		assert.deepEqual(editTarget.params, { company_id: "781898" });
		assert.equal(router.resolve(editTarget).path, "/admin/salons/781898");
		await router.push(editTarget);
		await router.isReady();
		mounted = mount();
		await flush();
		assert.equal(mounted.state.edit.mode.value, "edit");
		assert.equal(mounted.state.edit.model.value.company_id, 781898);
		assert.equal(mounted.state.edit.model.value.name, "Salon One");
		assert.deepEqual(mounted.state.edit.model.value.company, company);
		assert.deepEqual(
			mounted.state.edit.model.value.default_payment_profile,
			payment,
		);
		assert.deepEqual(
			mounted.state.edit.model.value.default_fiscal_profile,
			fiscal,
		);
		assert.deepEqual(
			mounted.state.edit.model.value.default_yclients_profile,
			yclients,
		);
		mounted.app.unmount();
		mounted = undefined;

		const copyTarget = salonCollection.routes.copy({ company_id: 781898 });
		assert.deepEqual(copyTarget.query, { copy_company_id: "781898" });
		await router.push(copyTarget);
		mounted = mount();
		await flush();
		assert.equal(mounted.state.edit.mode.value, "copy");
		assert.equal(mounted.state.edit.model.value.company_id, undefined);
		assert.ok(mounted.state.edit.model.value.name.startsWith("Salon One"));
		assert.equal(mounted.state.edit.model.value.default_payment_profile_id, null);
		assert.equal(mounted.state.edit.model.value.default_fiscal_profile_id, null);
		assert.equal(mounted.state.edit.model.value.default_yclients_profile_id, null);
		assert.deepEqual(requested, [
			"/admin/salons/781898",
			"/admin/salons/781898",
		]);
	} finally {
		mounted?.app.unmount();
		globalThis.fetch = originalFetch;
	}
});
