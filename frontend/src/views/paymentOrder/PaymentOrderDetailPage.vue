<script setup lang="ts">
import { computed, ref, shallowRef, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import Button from "primevue/button";
import Tab from "primevue/tab";
import TabList from "primevue/tablist";
import TabPanel from "primevue/tabpanel";
import TabPanels from "primevue/tabpanels";
import Tabs from "primevue/tabs";
import { CollectionEditPage, errorText } from "@katren/vue-collection-lib";
import { paymentOrderApi } from "@/api/paymentOrder.gen";
import PaymentAttemptsGrid from "@/components/payment/PaymentAttemptsGrid.vue";
import PaymentStatusTag from "@/components/payment/PaymentStatusTag.vue";
import type { PaymentOrderDetail } from "@/types/paymentOrderDetail.gen";
import {
	paymentAmount,
	paymentDate,
	paymentEnvironment,
	paymentMethod,
} from "@/utils/paymentDisplay";

const route = useRoute();
const router = useRouter();
const order = shallowRef<PaymentOrderDetail | null>(null);
const loading = ref(false);
const error = ref("");
const copyMessage = ref("");

watch(() => route.params.id, async (id, _, onCleanup) => {
	order.value = null;
	error.value = "";
	copyMessage.value = "";
	if (typeof id !== "string" || id === "") {
		loading.value = false;
		error.value = "Заказ не найден.";
		return;
	}

	let stale = false;
	onCleanup(() => {
		stale = true;
	});
	loading.value = true;
	try {
		const result = await paymentOrderApi.detail({ id });
		if (!stale) {
			order.value = result;
		}
	} catch (cause: unknown) {
		if (!stale) {
			error.value = errorText(cause);
		}
	} finally {
		if (!stale) {
			loading.value = false;
		}
	}
}, { immediate: true });

const goBack = () => router.push({ name: "paymentOrders" });

const copyId = async (): Promise<void> => {
	const id = order.value?.id;
	if (!id) {
		return;
	}
	try {
		await navigator.clipboard.writeText(id);
		copyMessage.value = "ID скопирован.";
	} catch {
		copyMessage.value = "Выделите и скопируйте ID вручную.";
	}
};

const orderFields = computed(() => {
	const item = order.value;
	return item ? [
		{ label: "Салон", value: item.salon_name },
		{ label: "Источник", value: item.entry_mode === "public" ? "Онлайн" : (item.kiosk_name ?? "Киоск") },
		{ label: "Клиент YClients", value: item.client_id },
		{ label: "Визит YClients", value: item.visit_id },
		{ label: "Запись YClients", value: item.record_id },
		{ label: "Создан", value: paymentDate(item.created_at) },
		{ label: "Обновлён", value: paymentDate(item.updated_at) },
		{ label: "Действует до", value: paymentDate(item.expires_at) },
	] : [];
});

const itemText = (value: unknown): string =>
	(typeof value === "string" && value !== "") || typeof value === "number" ? String(value) : "—";

const itemKind = (value: unknown): string =>
	value === "service" ? "Услуга" : value === "good" ? "Товар" : itemText(value);
</script>

<template>
	<CollectionEditPage title="Заказ на оплату" :loading="loading" @back="goBack">
		<p v-if="error" role="alert" class="text-red-700">{{ error }}</p>
		<div v-else-if="order" :key="order.id">
			<div class="mb-4 flex flex-wrap items-center gap-2">
				<code class="break-all text-sm">{{ order.id }}</code>
				<Button
					type="button"
					icon="pi pi-copy"
					aria-label="Копировать ID заказа"
					title="Копировать ID заказа"
					severity="secondary"
					text
					@click="copyId"
				/>
				<span role="status" class="text-sm text-slate-600">{{ copyMessage }}</span>
			</div>

			<dl class="mb-4 flex flex-wrap gap-x-6 gap-y-3">
				<div>
					<dt class="text-sm text-slate-600">К оплате</dt>
					<dd class="text-lg font-semibold">{{ paymentAmount(order.payable_kopecks) }}</dd>
				</div>
				<div>
					<dt class="mb-1 text-sm text-slate-600">Заказ</dt>
					<dd><PaymentStatusTag :value="order.state" /></dd>
				</div>
				<div>
					<dt class="mb-1 text-sm text-slate-600">Оплата</dt>
					<dd class="flex flex-wrap items-center gap-1">
						<PaymentStatusTag :value="order.payment_state" />
						<span v-if="order.payment_method" class="text-sm">{{ paymentMethod(order.payment_method) }}</span>
						<span v-if="order.environment" class="text-sm text-slate-600">· {{ paymentEnvironment(order.environment) }}</span>
					</dd>
				</div>
				<div>
					<dt class="mb-1 text-sm text-slate-600">YClients</dt>
					<dd><PaymentStatusTag :value="order.yclients_sync_status" /></dd>
				</div>
				<div>
					<dt class="mb-1 text-sm text-slate-600">Чек</dt>
					<dd><PaymentStatusTag :value="order.fiscal_status" /></dd>
				</div>
			</dl>

			<Tabs value="order" lazy>
				<TabList>
					<Tab value="order">Заказ</Tab>
					<Tab value="attempts">Попытки оплаты</Tab>
					<Tab value="completion">YClients и чек</Tab>
				</TabList>
				<TabPanels>
					<TabPanel value="order">
						<dl class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
							<div v-for="field in orderFields" :key="field.label">
								<dt class="text-sm text-slate-600">{{ field.label }}</dt>
								<dd>{{ field.value ?? "—" }}</dd>
							</div>
						</dl>
						<div class="mt-5 overflow-x-auto">
							<table class="w-full border-collapse text-left text-sm">
								<caption class="mb-2 text-left text-base font-semibold">Состав заказа</caption>
								<thead>
									<tr class="border-b border-slate-200">
										<th scope="col" class="py-2 pr-4">Позиция</th>
										<th scope="col" class="py-2 pr-4">Исполнитель</th>
										<th scope="col" class="py-2 pr-4 text-right">Количество</th>
										<th scope="col" class="py-2 pr-4 text-right">Цена</th>
										<th scope="col" class="py-2 text-right">Сумма</th>
									</tr>
								</thead>
								<tbody>
									<tr v-for="(item, index) in order.items_snapshot" :key="index" class="border-b border-slate-100">
										<td class="py-2 pr-4">
											<div>{{ itemText(item.title) }}</div>
											<div class="text-xs text-slate-600">{{ itemKind(item.kind) }}</div>
										</td>
										<td class="py-2 pr-4">{{ itemText(item.staff_name) }}</td>
										<td class="py-2 pr-4 text-right">{{ itemText(item.quantity) }}</td>
										<td class="py-2 pr-4 text-right">{{ paymentAmount(item.unit_price_kopecks) }}</td>
										<td class="py-2 text-right">{{ paymentAmount(item.amount_kopecks) }}</td>
									</tr>
								</tbody>
							</table>
						</div>
						<dl class="mt-5 grid grid-cols-1 gap-3 sm:grid-cols-3">
							<div>
								<dt class="text-sm text-slate-600">Сумма до бонусов</dt>
								<dd>{{ paymentAmount(order.subtotal_kopecks) }}</dd>
							</div>
							<div>
								<dt class="text-sm text-slate-600">Списано бонусов</dt>
								<dd>{{ paymentAmount(order.bonus_kopecks) }}</dd>
							</div>
							<div>
								<dt class="text-sm text-slate-600">К оплате</dt>
								<dd class="font-semibold">{{ paymentAmount(order.payable_kopecks) }}</dd>
							</div>
						</dl>
					</TabPanel>
					<TabPanel value="attempts">
						<PaymentAttemptsGrid
							:key="order.id"
							:orderId="order.id"
							:confirmedPaymentId="order.payment_id"
						/>
					</TabPanel>
					<TabPanel value="completion">
						<div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
							<section class="rounded-md border border-slate-200 p-4">
								<h3 class="mb-3 text-base font-semibold">Синхронизация с YClients</h3>
								<dl class="space-y-2">
									<div>
										<dt class="text-sm text-slate-600">Статус</dt>
										<dd><PaymentStatusTag :value="order.yclients_sync_status" /></dd>
									</div>
									<div>
										<dt class="text-sm text-slate-600">Документы</dt>
										<dd>{{ order.yclients_document_ids || "—" }}</dd>
									</div>
									<div>
										<dt class="text-sm text-slate-600">Завершено</dt>
										<dd>{{ paymentDate(order.yclients_completed_at) }}</dd>
									</div>
									<div v-if="order.yclients_error_code">
										<dt class="text-sm text-slate-600">Код ошибки</dt>
										<dd class="break-all">{{ order.yclients_error_code }}</dd>
									</div>
								</dl>
							</section>
							<section class="rounded-md border border-slate-200 p-4">
								<h3 class="mb-3 text-base font-semibold">Фискальный чек</h3>
								<dl class="space-y-2">
									<div>
										<dt class="text-sm text-slate-600">Статус</dt>
										<dd><PaymentStatusTag :value="order.fiscal_status" /></dd>
									</div>
									<div>
										<dt class="text-sm text-slate-600">Номер фискального документа</dt>
										<dd>{{ order.fiscal_document_number || "—" }}</dd>
									</div>
									<div>
										<dt class="text-sm text-slate-600">Завершено</dt>
										<dd>{{ paymentDate(order.fiscal_completed_at) }}</dd>
									</div>
									<div v-if="order.fiscal_error_code">
										<dt class="text-sm text-slate-600">Код ошибки</dt>
										<dd class="break-all">{{ order.fiscal_error_code }}</dd>
									</div>
								</dl>
							</section>
						</div>
					</TabPanel>
				</TabPanels>
			</Tabs>
		</div>
	</CollectionEditPage>
</template>
