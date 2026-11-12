<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Checkbox from "primevue/checkbox";
import InputNumber from "primevue/inputnumber";
import InputText from "primevue/inputtext";

import {
	CollectionForm,
	FormField,
	useCollectionFormModel,
	type FormErrorsView,
} from "@katren/vue-collection-lib";
import CatalogueReferenceSelect from "@/components/references/CatalogueReferenceSelect.vue";
import { createSalonFormModel } from "@/forms/salon.gen";
import type { SalonEditModel } from "@/forms/salonEdit";
import {
	companySelectReference,
	createSalonProfileSelectReferences,
} from "@/references/salonSelectReferences";
import type { SalonNew } from "@/types/salon.gen";

type FormMode = "create" | "edit" | "copy";

const props = withDefaults(defineProps<{
	model?: SalonEditModel;
	mode?: FormMode;
	errors?: FormErrorsView;
	submitting?: boolean;
}>(), {
	model: () => ({}),
	mode: "create",
	errors: undefined,
	submitting: false,
});

const emit = defineEmits<{
	submit: [model: SalonNew];
	cancel: [];
}>();

const { t } = useI18n();
const { form } = useCollectionFormModel<SalonEditModel>({
	model: () => props.model,
	defaults: createSalonFormModel,
});
const profileCompanyID = computed(() => {
	const id = form.value.company_id;
	return props.mode === "edit" && typeof id === "number" && id > 0 ? id : null;
});
const profileReferences = computed(() => createSalonProfileSelectReferences(profileCompanyID.value));

const updateCompanyID = (id: number | null): void => {
	if (props.mode === "edit" || form.value.company_id === id) {
		return;
	}

	form.value.company_id = id;
	form.value.company = undefined;
	form.value.default_payment_profile_id = null;
	form.value.default_payment_profile = null;
	form.value.default_fiscal_profile_id = null;
	form.value.default_fiscal_profile = null;
	form.value.default_yclients_profile_id = null;
	form.value.default_yclients_profile = null;
};

const formatDate = (value: unknown): string => value instanceof Date
	? value.toLocaleString("ru-RU")
	: "";

const submit = (): void => {
	const model: SalonNew = {
		company_id: form.value.company_id ?? 0,
		slug: form.value.slug?.trim() ?? "",
		name: form.value.name?.trim() ?? "",
		is_active: form.value.is_active ?? false,
		sort_order: form.value.sort_order ?? 0,
		default_payment_profile_id: form.value.default_payment_profile_id ?? null,
		default_fiscal_profile_id: form.value.default_fiscal_profile_id ?? null,
		default_yclients_profile_id: form.value.default_yclients_profile_id ?? null,
	};
	emit("submit", model);
};
</script>

<template>
	<CollectionForm
		:errors="props.errors"
		:submitting="props.submitting"
		@submit="submit"
		@cancel="emit('cancel')"
	>
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
			<FormField
				field="company_id"
				forId="salonCompanyID"
				:label="t('Salon.form.company', 'Компания YClients')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					id="salonCompanyID"
					:modelValue="form.company_id"
					:projectedValue="form.company"
					:label="t('Salon.form.company', 'Компания YClients')"
					:reference="companySelectReference"
					:invalid="invalid"
					:disabled="props.mode === 'edit' || props.submitting"
					:showClear="props.mode !== 'edit'"
					required
					@update:modelValue="updateCompanyID"
				/>
			</FormField>
			<FormField
				field="name"
				forId="salonName"
				:label="t('Salon.fields.name')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<InputText
					id="salonName"
					v-model="form.name"
					:invalid="invalid"
					:disabled="false"
					required
				/>
			</FormField>
			<FormField
				field="slug"
				forId="salonSlug"
				:label="t('Salon.fields.slug')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<InputText
					id="salonSlug"
					v-model="form.slug"
					:invalid="invalid"
					:disabled="false"
					required
				/>
			</FormField>
			<FormField
				field="sort_order"
				forId="salonSortOrder"
				:label="t('Salon.fields.sort_order')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<InputNumber
					id="salonSortOrder"
					v-model="form.sort_order"
					:invalid="invalid"
					:disabled="false"
					:useGrouping="false"
					required
				/>
			</FormField>
			<p v-if="props.mode !== 'edit'" class="text-sm text-slate-500 md:col-span-2">
				{{ t("Salon.form.saveBeforeProfiles", "Сохраните салон, чтобы выбрать профили.") }}
			</p>
			<FormField
				field="default_payment_profile_id"
				forId="salonDefaultPaymentProfileID"
				:label="t('Salon.fields.default_payment_profile_id')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					:key="profileCompanyID ?? 0"
					id="salonDefaultPaymentProfileID"
					v-model="form.default_payment_profile_id"
					:projectedValue="form.default_payment_profile"
					:label="t('Salon.fields.default_payment_profile_id')"
					:reference="profileReferences.payment"
					:invalid="invalid"
					:disabled="profileCompanyID === null || props.submitting"
				/>
			</FormField>
			<FormField
				field="default_fiscal_profile_id"
				forId="salonDefaultFiscalProfileID"
				:label="t('Salon.fields.default_fiscal_profile_id')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					:key="profileCompanyID ?? 0"
					id="salonDefaultFiscalProfileID"
					v-model="form.default_fiscal_profile_id"
					:projectedValue="form.default_fiscal_profile"
					:label="t('Salon.fields.default_fiscal_profile_id')"
					:reference="profileReferences.fiscal"
					:invalid="invalid"
					:disabled="profileCompanyID === null || props.submitting"
				/>
			</FormField>
			<FormField
				field="default_yclients_profile_id"
				forId="salonDefaultYclientsProfileID"
				:label="t('Salon.fields.default_yclients_profile_id')"
				:errors="props.errors"
				containerClass=""
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					:key="profileCompanyID ?? 0"
					id="salonDefaultYclientsProfileID"
					v-model="form.default_yclients_profile_id"
					:projectedValue="form.default_yclients_profile"
					:label="t('Salon.fields.default_yclients_profile_id')"
					:reference="profileReferences.yclients"
					:invalid="invalid"
					:disabled="profileCompanyID === null || props.submitting"
				/>
			</FormField>
		</div>
		<fieldset class="mt-6 rounded-lg border border-slate-200 px-4 pb-4 pt-2">
			<legend class="px-2 text-sm font-semibold">
				{{ t("Salon.form.statusAndHistory", "Состояние и история") }}
			</legend>
			<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
				<FormField
					field="is_active"
					forId="salonIsActive"
					:label="t('Salon.fields.is_active')"
					:errors="props.errors"
					containerClass=""
					v-slot="{ invalid }"
				>
					<Checkbox
						v-model="form.is_active"
						inputId="salonIsActive"
						:invalid="invalid"
						:disabled="props.submitting"
						binary
					/>
				</FormField>
				<FormField
					field="session_version"
					forId="salonSessionVersion"
					:label="t('Salon.fields.session_version')"
					:errors="props.errors"
					containerClass=""
				>
					<InputNumber
						id="salonSessionVersion"
						:modelValue="form.session_version"
						:useGrouping="false"
						disabled
					/>
				</FormField>
				<FormField
					field="created_at"
					forId="salonCreatedAt"
					:label="t('Salon.fields.created_at')"
					:errors="props.errors"
					containerClass=""
				>
					<InputText
						id="salonCreatedAt"
						:modelValue="formatDate(form.created_at)"
						disabled
					/>
				</FormField>
				<FormField
					field="updated_at"
					forId="salonUpdatedAt"
					:label="t('Salon.fields.updated_at')"
					:errors="props.errors"
					containerClass=""
				>
					<InputText
						id="salonUpdatedAt"
						:modelValue="formatDate(form.updated_at)"
						disabled
					/>
				</FormField>
			</div>
		</fieldset>
	</CollectionForm>
</template>
