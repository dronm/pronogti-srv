<script setup lang="ts">
import { computed, ref, shallowRef, watch } from "vue";
import { RouterLink } from "vue-router";
import {
	CollectionGrid,
	FilterOperatorParam,
	SortDirect,
	errorText,
	type CollectionParams,
} from "@katren/vue-collection-lib";
import { paymentAttemptApi } from "@/api/paymentAttempt.gen";
import { paymentAttemptCollection } from "@/collections/paymentAttempt.gen";
import PaymentEventsGrid from "@/components/payment/PaymentEventsGrid.vue";
import PaymentStatusTag from "@/components/payment/PaymentStatusTag.vue";
import type { PaymentAttempt } from "@/types/paymentAttempt.gen";
import {
	paymentAmount,
	paymentDate,
	paymentEnvironment,
	paymentMethod,
	paymentOrderRoute,
	shortPaymentId,
} from "@/utils/paymentDisplay";

const props = defineProps<{
	orderId?: string;
	confirmedPaymentId?: string | null;
}>();

const api = { list: paymentAttemptApi.list };
const columns = computed(() => props.orderId
	? paymentAttemptCollection.columns.filter(({ field }) => field !== "order_id" && field !== "salon_name")
	: paymentAttemptCollection.columns,
);
const scope = computed<CollectionParams | undefined>(() => props.orderId
	? { filter: [{ f: { order_id: { o: FilterOperatorParam.E, v: props.orderId } } }] }
	: undefined,
);

const selection = ref<string | null>(null);
const refreshCount = ref(0);
const attempt = shallowRef<PaymentAttempt | null>(null);
const error = ref("");
const loading = ref(false);

watch([selection, refreshCount], async ([id], _, onCleanup) => {
	attempt.value = null;
	error.value = "";
	loading.value = id !== null;
	if (!id) {
		return;
	}

	let stale = false;
	onCleanup(() => {
		stale = true;
	});
	try {
		const result = await paymentAttemptApi.detail({ id });
		if (!stale) {
			attempt.value = result;
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
});

const detailFields = computed(() => {
	const row = attempt.value;
	return row ? [
		{ label: "Платёжный профиль", value: row.profile_name },
		{ label: "Статус банка", value: row.bank_status },
		{ label: "Номер операции эквайринга", value: row.acquiring_operation_id },
		{ label: "Идентификатор СБП", value: row.qrc_id },
		{ label: "Идентификатор транзакции", value: row.transaction_id },
		{ label: "Референс транзакции", value: row.ref_transaction_id },
		{ label: "Оплачен", value: paymentDate(row.paid_at) },
		{ label: "Срок действия", value: paymentDate(row.expires_at) },
		{ label: "Последняя проверка", value: paymentDate(row.last_checked_at) },
		{ label: "Следующая проверка", value: paymentDate(row.next_check_at) },
		{ label: "Обновлён", value: paymentDate(row.updated_at) },
		{ label: "Код ошибки", value: row.error_code },
	] : [];
});
</script>

<template>
	<CollectionGrid
		:api="api"
		:columns="columns"
		:commands="[{ name: 'search' }, { name: 'refresh' }]"
		commandsMode="replace"
		:showContextMenu="false"
		:baseParams="scope"
		:defaultSorter="[{ f: 'created_at', d: SortDirect.DESC }, { f: 'id', d: SortDirect.DESC }]"
		:dataKey="paymentAttemptCollection.dataKey"
		:getKey="paymentAttemptCollection.getKey"
		:pageSize="20"
		rowSelection
		@selection-change="selection = $event?.id ?? null"
		@refresh="refreshCount += 1"
	>
		<template #body-id="{ data }">
			<span :title="data.id" class="font-mono">{{ shortPaymentId(data.id) }}</span>
		</template>
		<template #body-order_id="{ data }">
			<RouterLink
				:to="paymentOrderRoute(data.order_id)"
				:title="data.order_id"
				class="font-mono text-primary underline"
				@click.stop
			>
				{{ shortPaymentId(data.order_id) }}
			</RouterLink>
		</template>
		<template #body-method="{ data }">{{ paymentMethod(data.method) }}</template>
		<template #body-environment="{ data }">{{ paymentEnvironment(data.environment) }}</template>
		<template #body-amount_kopecks="{ data }">{{ paymentAmount(data.amount_kopecks) }}</template>
		<template #body-state="{ data }">
			<span
				v-if="data.state === 'paid' && data.id === props.confirmedPaymentId"
				class="mr-2 text-emerald-700"
				title="Подтверждённая оплата"
			>
				<i class="pi pi-check-circle" aria-label="Подтверждённая оплата" />
			</span>
			<PaymentStatusTag :value="data.state" />
		</template>
	</CollectionGrid>

	<section class="mt-5 rounded-lg border border-slate-200 p-4">
		<p v-if="loading" role="status">Загрузка данных платежа…</p>
		<p v-else-if="error" role="alert" class="text-red-700">{{ error }}</p>
		<template v-else-if="attempt">
			<h3 class="text-base font-semibold">Сведения о платеже</h3>
			<p class="mt-1 break-all font-mono text-xs text-slate-600">{{ attempt.id }}</p>
			<dl class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
				<div v-for="field in detailFields" :key="field.label">
					<dt class="text-sm text-slate-600">{{ field.label }}</dt>
					<dd class="break-all">{{ field.value || "—" }}</dd>
				</div>
			</dl>
		</template>
		<p v-else class="text-sm text-slate-600">Выберите платёж, чтобы увидеть его сведения и события.</p>
	</section>

	<section v-if="selection" class="mt-5">
		<h3 class="mb-3 text-base font-semibold">События платежа</h3>
		<PaymentEventsGrid :key="selection" :paymentId="selection" />
	</section>
</template>
