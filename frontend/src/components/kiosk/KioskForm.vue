<script setup lang="ts">
import { computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import Checkbox from "primevue/checkbox";
import InputText from "primevue/inputtext";
import {
	CollectionForm,
	FormField,
	useCollectionFormModel,
	type FormErrorsView,
} from "@katren/vue-collection-lib";

import CatalogueReferenceSelect from "@/components/references/CatalogueReferenceSelect.vue";
import { createKioskFormModel, kioskFormMutationFields, type KioskFormModel } from "@/forms/kiosk.gen";
import {
	createSalonProfileSelectReferences,
	salonCompanySelectReference,
} from "@/references/salonSelectReferences";
import type { KioskNew } from "@/types/kiosk.gen";

const props = withDefaults(
	defineProps<{
		model?: Partial<KioskFormModel>;
		mode?: "create" | "edit" | "copy";
		errors?: FormErrorsView;
		submitting?: boolean;
	}>(),
	{
		model: () => ({}),
		mode: "create",
		errors: undefined,
		submitting: false,
	},
);

const emit = defineEmits<{
	submit: [model: KioskNew];
	cancel: [];
	changed: [dirty: boolean];
}>();

const { t } = useI18n();
const { form } = useCollectionFormModel<KioskFormModel>({
	model: () => props.model,
	defaults: createKioskFormModel,
});

const profileFields = [
	{ field: "payment_profile_id", kind: "payment" },
	{ field: "fiscal_profile_id", kind: "fiscal" },
	{ field: "yclients_profile_id", kind: "yclients" },
] as const;
const companyId = computed(() => {
	const value = form.value.company_id;
	return typeof value === "number" && value > 0 ? value : null;
});
const profileReferences = computed(() => createSalonProfileSelectReferences(companyId.value));
const companyChanged = computed(
	() => props.mode === "edit" && companyId.value !== null && companyId.value !== props.model.company_id,
);
const dirty = computed(
	() =>
		props.mode === "edit" &&
		kioskFormMutationFields.some((field) => form.value[field] !== props.model[field]),
);
watch(dirty, (changed) => emit("changed", changed), { immediate: true });

const updateCompany = (id: number | null): void => {
	if (id === companyId.value) {
		return;
	}
	form.value.company_id = id;
	for (const { field } of profileFields) {
		form.value[field] = null;
	}
};

const fieldLabel = (field: keyof KioskFormModel): string => t(`Kiosk.fields.${field}`);
const formatDate = (value: Date | null | undefined): string => value?.toLocaleString("ru-RU") ?? "";

const submit = (): void => {
	emit("submit", {
		name: form.value.name?.trim() ?? "",
		company_id: companyId.value ?? 0,
		is_active: form.value.is_active ?? true,
		payment_profile_id: form.value.payment_profile_id ?? null,
		fiscal_profile_id: form.value.fiscal_profile_id ?? null,
		yclients_profile_id: form.value.yclients_profile_id ?? null,
	});
};
</script>

<template>
	<CollectionForm
		:errors="props.errors"
		:submitting="props.submitting"
		@submit="submit"
		@cancel="emit('cancel')"
	>
		<fieldset
			class="grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Основные настройки</legend>
			<FormField
				field="name"
				forId="kiosk-name"
				:label="fieldLabel('name')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText id="kiosk-name" v-model="form.name" :invalid="invalid" maxlength="200" required />
			</FormField>
			<FormField
				field="company_id"
				forId="kiosk-company"
				:label="fieldLabel('company_id')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					id="kiosk-company"
					:modelValue="companyId"
					:reference="salonCompanySelectReference"
					:label="fieldLabel('company_id')"
					:invalid="invalid"
					:disabled="props.submitting"
					:showActions="false"
					required
					@update:modelValue="updateCompany"
				/>
			</FormField>
			<p
				v-if="companyChanged"
				role="status"
				class="rounded bg-amber-50 p-3 text-sm text-amber-900 md:col-span-2"
			>
				При сохранении новой компании текущие сеансы и токен подключения будут отозваны. Выпустите
				новый токен и подключите киоск заново.
			</p>
			<FormField
				field="is_active"
				forId="kiosk-active"
				:label="fieldLabel('is_active')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Checkbox inputId="kiosk-active" v-model="form.is_active" :invalid="invalid" binary />
			</FormField>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Профили киоска</legend>
			<FormField
				v-for="{ field, kind } in profileFields"
				:key="field"
				:field="field"
				:forId="`kiosk-${field}`"
				:label="fieldLabel(field)"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					:id="`kiosk-${field}`"
					v-model="form[field]"
					:reference="profileReferences[kind]"
					:label="fieldLabel(field)"
					:invalid="invalid"
					:disabled="companyId === null || props.submitting"
					:showActions="false"
					defaultOptionLabel="По умолчанию салона"
				/>
			</FormField>
		</fieldset>

		<fieldset
			v-if="props.mode === 'edit'"
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2 lg:grid-cols-4"
		>
			<legend class="px-2 text-sm font-semibold">Служебные данные</legend>
			<FormField field="id" forId="kiosk-id" :label="fieldLabel('id')">
				<InputText id="kiosk-id" :modelValue="form.id?.toString()" readonly />
			</FormField>
			<FormField
				field="session_version"
				forId="kiosk-session-version"
				:label="fieldLabel('session_version')"
			>
				<InputText
					id="kiosk-session-version"
					:modelValue="form.session_version?.toString()"
					readonly
				/>
			</FormField>
			<FormField
				v-for="field in ['created_at', 'updated_at'] as const"
				:key="field"
				:field="field"
				:forId="`kiosk-${field}`"
				:label="fieldLabel(field)"
			>
				<InputText :id="`kiosk-${field}`" :modelValue="formatDate(form[field])" readonly />
			</FormField>
		</fieldset>
	</CollectionForm>
</template>
