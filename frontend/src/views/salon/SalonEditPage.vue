<script setup lang="ts">
import { useI18n } from "vue-i18n";

import {
	CollectionEditPage,
	useCollectionEditPage,
} from "@katren/vue-collection-lib";
import { salonApi } from "@/api/salon.gen";
import SalonForm from "@/components/salon/SalonForm.vue";
import { useSalonSchemas } from "@/composables/schemas/useSalonSchemas.gen";
import {
	createSalonFormModel,
	salonFormMutationFields,
} from "@/forms/salon.gen";
import type { SalonEditModel } from "@/forms/salonEdit";
import type {
	SalonKey,
	SalonNew,
	SalonUpd,
} from "@/types/salon.gen";
import type { SalonDetail } from "@/types/salonDetail.gen";

const { t } = useI18n();
const schemas = useSalonSchemas();
const edit = useCollectionEditPage<
	SalonEditModel,
	SalonKey,
	SalonNew,
	SalonUpd,
	SalonDetail
>({
	api: salonApi,
	createRouteName: "salonCreate",
	listRoute: { name: "salons" },
	keyFromRoute: (route) => ({
		company_id: Number(route.params.company_id ?? 0),
	}),
	copyKeyFromRoute: (route) => route.query.copy_company_id
		? ({
			company_id: Number(route.query.copy_company_id ?? 0),
		})
		: null,
	createModel: createSalonFormModel,
	copyModel: (detail) => ({
		...detail,
		company_id: undefined,
		company: undefined,
		default_payment_profile_id: null,
		default_payment_profile: null,
		default_fiscal_profile_id: null,
		default_fiscal_profile: null,
		default_yclients_profile_id: null,
		default_yclients_profile: null,
		name: `${detail.name} - ${t("Grid.copySuffix")}`,
	}),
	fields: salonFormMutationFields,
	createSchema: schemas.SalonNewSchema,
	updateSchema: schemas.SalonUpdSchema,
	success: { mode: "back" },
});

const submit = async (model: SalonNew): Promise<void> => {
	await edit.submit(model);
};
</script>

<template>
	<CollectionEditPage
		:title="t(`Salon.form.${edit.mode.value}`)"
		:loading="edit.loading.value"
		@back="edit.goBack"
	>
		<SalonForm
			:model="edit.model.value"
			:mode="edit.mode.value"
			:errors="edit.errors"
			:submitting="edit.submitting.value"
			@submit="submit"
			@cancel="edit.goBack"
		/>
	</CollectionEditPage>
</template>
