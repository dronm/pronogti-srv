<script setup lang="ts">
import { computed } from "vue";
import Tag from "primevue/tag";
import { paymentStatus } from "@/utils/paymentDisplay";

const props = defineProps<{ value: string | null }>();

const severity = computed(() => {
	switch (props.value) {
		case "paid":
		case "succeeded":
		case "issued":
		case "processed":
			return "success";
		case "failed":
		case "blocked":
			return "danger";
		case "unknown":
		case "pending":
		case "payment_pending":
			return "warn";
		default:
			return "secondary";
	}
});
</script>

<template>
	<Tag v-if="props.value" :value="paymentStatus(props.value)" :severity="severity" />
	<span v-else>—</span>
</template>
