<script setup lang="ts">
import { RouterLink } from "vue-router";
import { CollectionGrid, SortDirect } from "@katren/vue-collection-lib";
import { paymentOrderCollection } from "@/collections/paymentOrder.gen";
import PaymentStatusTag from "@/components/payment/PaymentStatusTag.vue";
import {
	paymentAmount,
	paymentEnvironment,
	paymentMethod,
	paymentOrderRoute,
	shortPaymentId,
} from "@/utils/paymentDisplay";

const api = { list: paymentOrderCollection.api.list };
</script>

<template>
	<section class="py-4">
		<h1 class="mb-4 text-2xl font-semibold">Заказы на оплату</h1>
		<CollectionGrid
			:api="api"
			:columns="paymentOrderCollection.columns"
			:commands="[{ name: 'search' }, { name: 'refresh' }]"
			commandsMode="replace"
			:showContextMenu="false"
			:defaultSorter="[{ f: 'created_at', d: SortDirect.DESC }, { f: 'id', d: SortDirect.DESC }]"
			:dataKey="paymentOrderCollection.dataKey"
			:getKey="paymentOrderCollection.getKey"
			:stateKey="paymentOrderCollection.stateKey"
			:pageSize="paymentOrderCollection.pageSize"
		>
			<template #body-id="{ data }">
				<RouterLink
					:to="paymentOrderRoute(data.id)"
					:title="data.id"
					class="font-mono text-primary underline"
					@click.stop
				>
					{{ shortPaymentId(data.id) }}
				</RouterLink>
			</template>
			<template #body-kiosk_name="{ data }">
				{{ data.entry_mode === "public" ? "Онлайн" : (data.kiosk_name ?? "Киоск") }}
			</template>
			<template #body-payable_kopecks="{ data }">{{ paymentAmount(data.payable_kopecks) }}</template>
			<template #body-state="{ data }"><PaymentStatusTag :value="data.state" /></template>
			<template #body-payment_state="{ data }">
				<div class="flex flex-wrap items-center gap-1">
					<span v-if="data.payment_method">{{ paymentMethod(data.payment_method) }}</span>
					<PaymentStatusTag :value="data.payment_state" />
				</div>
			</template>
			<template #body-environment="{ data }">{{ paymentEnvironment(data.environment) }}</template>
			<template #body-yclients_sync_status="{ data }"><PaymentStatusTag :value="data.yclients_sync_status" /></template>
			<template #body-fiscal_status="{ data }"><PaymentStatusTag :value="data.fiscal_status" /></template>
		</CollectionGrid>
	</section>
</template>
