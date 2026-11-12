<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Checkbox from "primevue/checkbox";
import InputNumber from "primevue/inputnumber";
import InputText from "primevue/inputtext";
import Select from "primevue/select";

import {
	CollectionForm,
	FormField,
	useCollectionFormModel,
	type FormErrorsView,
} from "@katren/vue-collection-lib";
import CatalogueReferenceSelect from "@/components/references/CatalogueReferenceSelect.vue";
import { createFiscalProfileFormModel, type FiscalProfileFormModel } from "@/forms/fiscalProfile.gen";
import {
	fiscalPaymentMethodOptions,
	fiscalTaxSystemOptions,
	fiscalVatOptions,
} from "@/forms/fiscalProfileOptions";
import { salonCompanySelectReference } from "@/references/salonSelectReferences";
import { CATALOGUE_EXECUTION_MODE_VALUES } from "@/types/enums/catalogueExecutionMode.gen";
import type { FiscalProfileNew } from "@/types/fiscalProfile.gen";

const props = withDefaults(
	defineProps<{
		model?: Partial<FiscalProfileFormModel>;
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
	submit: [model: FiscalProfileNew];
	cancel: [];
}>();

const { t } = useI18n();
const { form } = useCollectionFormModel<FiscalProfileFormModel>({
	model: () => props.model,
	defaults: createFiscalProfileFormModel,
});

const modeOptions = computed(() =>
	CATALOGUE_EXECUTION_MODE_VALUES.map((value) => ({
		value,
		label: t(`CatalogueExecutionMode.${value}`),
		disabled: value === "live" && !form.value.driver_base_url?.trim(),
	})),
);

const receiptSelectFields = [
	{ field: "tax_system", options: fiscalTaxSystemOptions },
	{ field: "service_vat_rate", options: fiscalVatOptions },
	{ field: "good_vat_rate", options: fiscalVatOptions },
	{ field: "payment_method", options: fiscalPaymentMethodOptions },
] as const;

const settlementTextFields = [
	{ field: "settlement_address", maxLength: 256 },
	{ field: "settlement_place", maxLength: 256 },
] as const;

const settlementFlagFields = ["internet_payment", "suppress_print"] as const;
const timestampFields = ["created_at", "updated_at"] as const;

const fieldLabel = (field: keyof FiscalProfileFormModel): string => t(`FiscalProfile.fields.${field}`);
const controlID = (field: keyof FiscalProfileFormModel): string => `fiscalProfile-${field}`;
const optionalText = (value: string | null | undefined): string | null => value?.trim() || null;
const formatDate = (value: Date | null | undefined): string => value?.toLocaleString("ru-RU") ?? "";

const submit = (): void => {
	const data = form.value;
	emit("submit", {
		company_id: data.company_id ?? 0,
		name: data.name?.trim() ?? "",
		enabled: data.enabled ?? true,
		mode: data.mode ?? "demo",
		driver_base_url: optionalText(data.driver_base_url),
		device_id: optionalText(data.device_id),
		bearer_token_env: optionalText(data.bearer_token_env),
		timeout_seconds: data.timeout_seconds ?? 0,
		cashier_name: data.cashier_name?.trim() ?? "",
		cashier_inn: optionalText(data.cashier_inn),
		tax_system: data.tax_system ?? 0,
		service_vat_rate: data.service_vat_rate ?? 0,
		good_vat_rate: data.good_vat_rate ?? 0,
		payment_method: data.payment_method ?? 0,
		settlement_address: optionalText(data.settlement_address),
		settlement_place: optionalText(data.settlement_place),
		internet_payment: data.internet_payment ?? false,
		suppress_print: data.suppress_print ?? false,
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
				field="company_id"
				:forId="controlID('company_id')"
				:label="fieldLabel('company_id')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<CatalogueReferenceSelect
					:id="controlID('company_id')"
					v-model="form.company_id"
					:reference="salonCompanySelectReference"
					:label="fieldLabel('company_id')"
					:invalid="invalid"
					:disabled="props.submitting"
					:showClear="false"
					required
				/>
			</FormField>
			<FormField
				field="name"
				:forId="controlID('name')"
				:label="fieldLabel('name')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('name')"
					v-model="form.name"
					:invalid="invalid"
					maxlength="200"
					required
				/>
			</FormField>
			<FormField
				field="mode"
				:forId="controlID('mode')"
				:label="fieldLabel('mode')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Select
					:labelId="controlID('mode')"
					:ariaLabel="fieldLabel('mode')"
					v-model="form.mode"
					:options="modeOptions"
					:invalid="invalid"
					:disabled="props.submitting"
					optionLabel="label"
					optionValue="value"
					optionDisabled="disabled"
				/>
				<small v-if="!form.driver_base_url?.trim()" class="text-slate-600"
					>Для рабочего режима укажите адрес в разделе «Подключение к ККТ».</small
				>
			</FormField>
			<FormField
				field="enabled"
				:forId="controlID('enabled')"
				:label="fieldLabel('enabled')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Checkbox :inputId="controlID('enabled')" v-model="form.enabled" :invalid="invalid" binary />
			</FormField>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Налогообложение и расчёт</legend>
			<FormField
				v-for="{ field, options } in receiptSelectFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Select
					:labelId="controlID(field)"
					:ariaLabel="fieldLabel(field)"
					v-model="form[field]"
					:options="options"
					:invalid="invalid"
					:disabled="props.submitting"
					optionLabel="label"
					optionValue="value"
				/>
			</FormField>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Кассир и реквизиты чека</legend>
			<FormField
				field="cashier_name"
				:forId="controlID('cashier_name')"
				:label="fieldLabel('cashier_name')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('cashier_name')"
					v-model="form.cashier_name"
					:invalid="invalid"
					maxlength="64"
					required
				/>
			</FormField>
			<FormField
				field="cashier_inn"
				:forId="controlID('cashier_inn')"
				:label="fieldLabel('cashier_inn')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('cashier_inn')"
					v-model="form.cashier_inn"
					:invalid="invalid"
					inputmode="numeric"
					maxlength="12"
					pattern="[0-9]{12}"
				/>
			</FormField>
			<FormField
				v-for="{ field, maxLength } in settlementTextFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID(field)"
					v-model="form[field]"
					:invalid="invalid"
					:maxlength="maxLength"
					:required="field === 'settlement_place' && !!form.internet_payment"
				/>
			</FormField>
			<FormField
				v-for="field in settlementFlagFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Checkbox :inputId="controlID(field)" v-model="form[field]" :invalid="invalid" binary />
			</FormField>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Подключение к ККТ</legend>
			<FormField
				field="driver_base_url"
				:forId="controlID('driver_base_url')"
				:label="fieldLabel('driver_base_url')"
				:errors="props.errors"
			>
				<InputText
					:id="controlID('driver_base_url')"
					v-model="form.driver_base_url"
					:invalid="props.errors?.has('driver_base_url') ?? false"
					inputmode="url"
					placeholder="http://127.0.0.1:8090"
					autocomplete="off"
					spellcheck="false"
					:required="form.mode === 'live'"
					aria-describedby="fiscalProfile-driver-help"
				/>
				<small id="fiscalProfile-driver-help" class="text-slate-600">
					Базовый URL сервиса TerminalFA из разрешённых подключений сервера.
				</small>
			</FormField>
			<FormField
				field="bearer_token_env"
				:forId="controlID('bearer_token_env')"
				:label="fieldLabel('bearer_token_env')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('bearer_token_env')"
					v-model="form.bearer_token_env"
					:invalid="invalid"
					placeholder="PRONOGTI_TERMINALFA_TOKEN"
					pattern="[A-Za-z_][A-Za-z0-9_]*"
					autocomplete="off"
					spellcheck="false"
					aria-describedby="fiscalProfile-token-help"
				/>
				<small id="fiscalProfile-token-help" class="text-slate-600">
					Имя переменной, заданной для этого адреса. Оставьте пустым, если TerminalFA работает без
					токена.
				</small>
			</FormField>
			<FormField
				field="device_id"
				:forId="controlID('device_id')"
				:label="fieldLabel('device_id')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('device_id')"
					v-model="form.device_id"
					:invalid="invalid"
					maxlength="128"
					:required="form.mode === 'live'"
				/>
			</FormField>
			<FormField
				field="timeout_seconds"
				:forId="controlID('timeout_seconds')"
				:label="fieldLabel('timeout_seconds')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputNumber
					:inputId="controlID('timeout_seconds')"
					v-model="form.timeout_seconds"
					:invalid="invalid"
					:min="1"
					:max="300"
					:useGrouping="false"
					:pt="{
						pcInputText: {
							root: {
								'aria-describedby': 'fiscalProfile-timeout-help',
							},
						},
					}"
					required
				/>
				<small id="fiscalProfile-timeout-help" class="text-slate-600"
					>Тайм-аут HTTP-запроса к сервису ККТ в секундах (1–300).</small
				>
			</FormField>
		</fieldset>

		<fieldset
			v-if="props.mode === 'edit'"
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-3"
		>
			<legend class="px-2 text-sm font-semibold">История</legend>
			<FormField field="id" :forId="controlID('id')" :label="fieldLabel('id')">
				<InputText :id="controlID('id')" :modelValue="form.id?.toString()" readonly />
			</FormField>
			<FormField
				v-for="field in timestampFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
			>
				<InputText :id="controlID(field)" :modelValue="formatDate(form[field])" readonly />
			</FormField>
		</fieldset>
	</CollectionForm>
</template>
