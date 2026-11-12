import type { RouteRecordRaw } from "vue-router";
import type { ApplicationRouteManifestItem } from "@katren/vue-business-app/application-routes";

import Main from "@/views/Main.vue";
import UserProfile from "@/views/user/UserProfile.vue";
import { generatedRouteManifest } from "./routeManifest.gen";

const MainMenuConstructor = () =>
	import("@katren/vue-business-app/menu").then(
		(module) => module.MainMenuConstructorView,
	);
const ApplicationRoutes = () =>
	import("@katren/vue-business-app/application-routes").then(
		(module) => module.ApplicationRoutesView,
	);

const manualRouteManifest = [
	{
		route: { path: "/", name: "main", component: Main },
		registry: {
			name: "main",
			path: "/",
			descr: "Главная",
			section: "Формы",
			icon: "pi pi-home",
			menu_available: true,
		},
	},
	{
		route: {
			path: "/main-menu-constructor",
			name: "mainMenuConstructor",
			component: MainMenuConstructor,
			meta: { roleId: "admin" },
		},
		registry: {
			name: "mainMenuConstructor",
			path: "/main-menu-constructor",
			descr: "Конструктор главного меню",
			section: "Администрирование",
			icon: "pi pi-sitemap",
			menu_available: false,
		},
	},
	{
		route: {
			path: "/application-routes",
			name: "applicationRoutes",
			component: ApplicationRoutes,
			meta: { roleId: "admin" },
		},
		registry: {
			name: "applicationRoutes",
			path: "/application-routes",
			descr: "Все формы",
			section: "Формы",
			icon: "pi pi-th-large",
			menu_available: true,
		},
	},
	{
		route: {
			path: "/admin/payment-orders/:id",
			name: "paymentOrderDetail",
			component: () => import("@/views/paymentOrder/PaymentOrderDetailPage.vue"),
			meta: { roleId: "admin" },
		},
		registry: {
			name: "paymentOrderDetail",
			path: "/admin/payment-orders/:id",
			descr: "Заказ на оплату",
			section: "Платежи",
			icon: "pi pi-list",
			menu_available: false,
		},
	},
	{
		route: {
			path: "/profile",
			name: "userProfile",
			component: UserProfile,
		},
		registry: {
			name: "userProfile",
			path: "/profile",
			descr: "Мой профиль",
			section: "Формы",
			icon: "pi pi-user",
			menu_available: false,
		},
	},
] satisfies Array<{
	route: RouteRecordRaw;
	registry: ApplicationRouteManifestItem;
}>;

export const applicationRouteManifest: ApplicationRouteManifestItem[] = [
	...manualRouteManifest.map((item) => item.registry),
	...generatedRouteManifest.map((item) => item.registry),
];

export const applicationRouteRecords: RouteRecordRaw[] = [
	...manualRouteManifest.map((item) => item.route),
	...generatedRouteManifest.map((item) => item.route),
];
