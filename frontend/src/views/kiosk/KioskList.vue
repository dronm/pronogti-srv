<script setup lang="ts">
import { CollectionListPage } from "@katren/vue-collection-lib";

import { kioskCollection } from "@/collections/kiosk.gen";

const collection = {
	...kioskCollection,
	commands: kioskCollection.commands?.filter(({ name }) => name !== "copy"),
};
</script>

<template>
	<CollectionListPage :collection="collection">
		<template #header>
			<p class="text-sm text-slate-600">
				Удаление отключает киоск и отзывает токен. История платежей сохраняется.
			</p>
		</template>
	</CollectionListPage>
</template>
