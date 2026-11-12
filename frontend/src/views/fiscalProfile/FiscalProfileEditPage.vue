<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { CollectionEditPage, useCollectionEditPage } from "@katren/vue-collection-lib";

import { fiscalProfileApi } from "@/api/fiscalProfile.gen";
import FiscalProfileForm from "@/components/fiscalProfile/FiscalProfileForm.vue";
import { useFiscalProfileSchemas } from "@/composables/schemas/useFiscalProfileSchemas.gen";
import {
	createFiscalProfileFormModel,
	fiscalProfileFormMutationFields,
	type FiscalProfileFormModel,
} from "@/forms/fiscalProfile.gen";
import type {
	FiscalProfile,
	FiscalProfileKey,
	FiscalProfileNew,
	FiscalProfileUpd,
} from "@/types/fiscalProfile.gen";

const { t } = useI18n();
const schemas = useFiscalProfileSchemas();
const edit = useCollectionEditPage<
	FiscalProfileFormModel,
	FiscalProfileKey,
	FiscalProfileNew,
	FiscalProfileUpd,
	FiscalProfile
>({
	api: fiscalProfileApi,
	createRouteName: "fiscalProfileCreate",
	listRoute: { name: "fiscalProfiles" },
	keyFromRoute: (route) => ({ id: Number(route.params.id ?? 0) }),
	copyKeyFromRoute: (route) => (route.query.copy_id ? { id: Number(route.query.copy_id) } : null),
	createModel: createFiscalProfileFormModel,
	copyModel: (detail) => ({
		...detail,
		id: undefined,
		name: `${detail.name} - ${t("Grid.copySuffix")}`,
		mode: "demo",
		driver_base_url: null,
		bearer_token_env: null,
	}),
	fields: fiscalProfileFormMutationFields,
	createSchema: schemas.FiscalProfileNewSchema,
	updateSchema: schemas.FiscalProfileUpdSchema,
	success: { mode: "back" },
});
</script>

<template>
	<CollectionEditPage
		:title="t(`FiscalProfile.form.${edit.mode.value}`)"
		:loading="edit.loading.value"
		@back="edit.goBack"
	>
		<FiscalProfileForm
			v-if="edit.mode.value !== 'edit' || edit.model.value.id"
			:model="edit.model.value"
			:mode="edit.mode.value"
			:errors="edit.errors"
			:submitting="edit.submitting.value"
			@submit="edit.submit"
			@cancel="edit.goBack"
		/>
		<p v-else role="alert" class="text-red-700">
			{{ edit.errors.formErrors[0] ?? "Не удалось загрузить фискальный профиль." }}
		</p>
	</CollectionEditPage>
</template>
