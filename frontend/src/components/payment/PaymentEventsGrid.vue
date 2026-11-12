<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import {
	CollectionGrid,
	FilterOperatorParam,
	SortDirect,
	type CollectionParams,
} from "@katren/vue-collection-lib";
import { paymentEventCollection } from "@/collections/paymentEvent.gen";
import PaymentStatusTag from "@/components/payment/PaymentStatusTag.vue";
import {
	paymentEnvironment,
	paymentOrderRoute,
	shortPaymentId,
} from "@/utils/paymentDisplay";

const props = defineProps<{
	paymentId?: string;
	unmatchedOnly?: boolean;
}>();

const api = { list: paymentEventCollection.api.list };
const columns = computed(() => props.paymentId
	? paymentEventCollection.columns.filter(({ field }) => field !== "order_id" && field !== "payment_id")
	: paymentEventCollection.columns,
);
const scope = computed<CollectionParams | undefined>(() => {
	if (props.paymentId) {
		return { filter: [{ f: { payment_id: { o: FilterOperatorParam.E, v: props.paymentId } } }] };
	}
	if (props.unmatchedOnly) {
		return { filter: [{ f: { payment_id: { o: FilterOperatorParam.I, v: null } } }] };
	}
	return undefined;
});
</script>

<template>
	<CollectionGrid
		:key="props.paymentId ?? (props.unmatchedOnly ? 'unmatched' : 'all')"
		:api="api"
		:columns="columns"
		:commands="[{ name: 'search' }, { name: 'refresh' }]"
		commandsMode="replace"
		:showContextMenu="false"
		:baseParams="scope"
		:defaultSorter="[{ f: 'received_at', d: SortDirect.DESC }, { f: 'id', d: SortDirect.DESC }]"
		:dataKey="paymentEventCollection.dataKey"
		:getKey="paymentEventCollection.getKey"
		:pageSize="20"
	>
		<template #body-order_id="{ data }">
			<RouterLink
				v-if="data.order_id"
				:to="paymentOrderRoute(data.order_id)"
				:title="data.order_id"
				class="font-mono text-primary underline"
				@click.stop
			>
				{{ shortPaymentId(data.order_id) }}
			</RouterLink>
			<span v-else>—</span>
		</template>
		<template #body-payment_id="{ data }">
			<span v-if="data.payment_id" :title="data.payment_id" class="font-mono">
				{{ shortPaymentId(data.payment_id) }}
			</span>
			<span v-else>—</span>
		</template>
		<template #body-source="{ data }">
			{{ data.source === "webhook" ? "Вебхук" : data.source === "reconciliation" ? "Сверка" : data.source }}
		</template>
		<template #body-environment="{ data }">{{ paymentEnvironment(data.environment) }}</template>
		<template #body-processing_state="{ data }"><PaymentStatusTag :value="data.processing_state" /></template>
	</CollectionGrid>
</template>
