<script setup lang="ts">
import { ref } from "vue";
import Checkbox from "primevue/checkbox";
import PaymentEventsGrid from "@/components/payment/PaymentEventsGrid.vue";

const unmatchedOnly = ref(false);
</script>

<template>
	<section class="py-4">
		<div class="mb-4 flex flex-wrap items-center justify-between gap-3">
			<h1 class="text-2xl font-semibold">События платежей</h1>
			<div class="flex items-center gap-2">
				<Checkbox v-model="unmatchedOnly" inputId="unmatched-payment-events" binary />
				<label for="unmatched-payment-events">Без связанного платежа</label>
			</div>
		</div>
		<PaymentEventsGrid :unmatchedOnly="unmatchedOnly" />
	</section>
</template>
