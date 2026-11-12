<script setup lang="ts" generic="TModel extends object, TKey, TNew, TUpd extends object, TDetailModel">
import { computed } from "vue";
import { useRoute } from "vue-router";
import {
	CollectionListPage,
	FilterOperatorParam,
	type CollectionDefinition,
} from "@katren/vue-collection-lib";

const props = defineProps<{
	collection: CollectionDefinition<TModel, TKey, TNew, TUpd, TDetailModel>;
}>();

const route = useRoute();
const recordID = computed(() => {
	const value = route.query.id;
	if (typeof value !== "string" || !/^[1-9]\d*$/.test(value)) {
		return null;
	}

	const id = Number(value);
	return Number.isSafeInteger(id) ? id : null;
});

const collection = computed(() => {
	const definition = props.collection;
	if (recordID.value === null) {
		return definition;
	}

	return {
		...definition,
		stateKey: undefined,
		commands: definition.commands?.filter(({ name }) => name !== "create" && name !== "copy"),
		baseParams: {
			...definition.baseParams,
			filter: [
				...(definition.baseParams?.filter ?? []),
				{ f: { id: { o: FilterOperatorParam.E, v: recordID.value } } },
			],
		},
	};
});
</script>

<template>
	<CollectionListPage :key="recordID ?? 'collection'" :collection="collection" />
</template>
