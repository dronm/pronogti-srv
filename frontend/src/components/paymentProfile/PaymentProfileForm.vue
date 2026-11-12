<script setup lang="ts">
import { computed, watch } from "vue";
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
import { createPaymentProfileFormModel, type PaymentProfileFormModel } from "@/forms/paymentProfile.gen";
import { salonCompanySelectReference } from "@/references/salonSelectReferences";
import {
	PAYMENT_PROFILE_ENVIRONMENT_VALUES,
	type PaymentProfileEnvironment,
} from "@/types/enums/paymentProfileEnvironment.gen";
import type { PaymentProfileNew } from "@/types/paymentProfile.gen";

const props = withDefaults(
	defineProps<{
		model?: Partial<PaymentProfileFormModel>;
		mode?: "create" | "edit" | "copy";
		errors?: FormErrorsView;
		submitting?: boolean;
		credentialConfigured?: boolean;
		hasPayments?: boolean;
		hasCardPayments?: boolean;
	}>(),
	{
		model: () => ({}),
		mode: "create",
		submitting: false,
		credentialConfigured: false,
		hasPayments: false,
		hasCardPayments: false,
	},
);

const emit = defineEmits<{
	submit: [model: PaymentProfileNew];
	cancel: [];
	changed: [dirty: boolean];
}>();

const { t } = useI18n();
const bankURLs: Record<PaymentProfileEnvironment, string> = {
	mock: "http://127.0.0.1:8099",
	sandbox: "https://enter.tochka.com/sandbox/v2",
	production: "https://enter.tochka.com/uapi",
};
const { form } = useCollectionFormModel<PaymentProfileFormModel>({
	model: () => props.model,
	defaults: createPaymentProfileFormModel,
	fromModel: (model, defaults) => {
		const environment = model.environment ?? defaults.environment ?? "mock";
		return {
			...defaults,
			...model,
			environment,
			api_base_url: model.api_base_url || bankURLs[environment],
		};
	},
});

const editing = computed(() => props.mode === "edit");
const environmentOptions = computed(() =>
	PAYMENT_PROFILE_ENVIRONMENT_VALUES.map((value) => ({
		value,
		label: t(`PaymentProfileEnvironment.${value}`),
	})),
);
const setEnvironment = (environment: PaymentProfileEnvironment): void => {
	form.value.environment = environment;
	form.value.api_base_url = bankURLs[environment];
};

const fieldLabel = (field: keyof PaymentProfileFormModel): string => t(`PaymentProfile.fields.${field}`);
const controlID = (field: keyof PaymentProfileFormModel): string => `paymentProfile-${field}`;
const optionalText = (value: string | null | undefined): string | null => value?.trim() || null;
const formatDate = (value: Date | null | undefined): string => value?.toLocaleString("ru-RU") ?? "";
const identityFields = [
	{ field: "client_id", frozenAfterPayment: false },
	{ field: "customer_code", frozenAfterPayment: true },
	{ field: "sbp_merchant_id", frozenAfterPayment: true },
	{ field: "account_id", frozenAfterPayment: true },
] as const;
const bankDurationFields = [
	{ field: "qr_ttl_minutes", max: 129600 },
	{ field: "http_timeout_seconds", max: 300 },
] as const;
const redirectFields = ["card_redirect_url", "card_fail_redirect_url"] as const;
const timestampFields = ["created_at", "updated_at"] as const;

const mutation = (data: PaymentProfileFormModel): PaymentProfileNew => ({
	company_id: data.company_id ?? 0,
	name: data.name?.trim() ?? "",
	environment: data.environment ?? "mock",
	api_base_url: data.api_base_url?.trim() ?? "",
	client_id: optionalText(data.client_id),
	customer_code: optionalText(data.customer_code),
	sbp_merchant_id: optionalText(data.sbp_merchant_id),
	account_id: optionalText(data.account_id),
	qr_ttl_minutes: data.qr_ttl_minutes ?? 0,
	http_timeout_seconds: data.http_timeout_seconds ?? 0,
	enabled: editing.value && (data.enabled ?? false),
	cards_enabled: data.cards_enabled ?? false,
	acquiring_merchant_id: optionalText(data.acquiring_merchant_id),
	card_ttl_minutes: data.card_ttl_minutes ?? 0,
	card_redirect_url: optionalText(data.card_redirect_url),
	card_fail_redirect_url: optionalText(data.card_fail_redirect_url),
});

const dirty = computed(() => {
	if (!editing.value) {
		return false;
	}
	const current = mutation(form.value);
	const saved = mutation(props.model);
	return (Object.keys(current) as (keyof PaymentProfileNew)[]).some(
		(field) => current[field] !== saved[field],
	);
});
watch(dirty, (value) => emit("changed", value), { immediate: true });
</script>

<template>
	<CollectionForm
		:errors="props.errors"
		:submitting="props.submitting"
		@submit="emit('submit', mutation(form))"
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
					:disabled="editing || props.submitting"
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
				field="environment"
				:forId="controlID('environment')"
				:label="fieldLabel('environment')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Select
					:labelId="controlID('environment')"
					:ariaLabel="fieldLabel('environment')"
					:modelValue="form.environment"
					:options="environmentOptions"
					:invalid="invalid"
					:disabled="editing || props.submitting"
					optionLabel="label"
					optionValue="value"
					@update:modelValue="setEnvironment"
				/>
			</FormField>
			<FormField
				field="enabled"
				:forId="controlID('enabled')"
				:label="fieldLabel('enabled')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Checkbox
					:inputId="controlID('enabled')"
					v-model="form.enabled"
					:invalid="invalid"
					:disabled="
						!editing ||
						props.submitting ||
						(!props.credentialConfigured && !form.enabled)
					"
					binary
				/>
				<small v-if="!editing" class="text-slate-600"
					>Профиль создаётся выключенным. Сохраните его, добавьте JWT и включите
					профиль.</small
				>
				<small
					v-else-if="!props.credentialConfigured && !form.enabled"
					class="text-slate-600"
					>Для включения профиля настройте JWT.</small
				>
			</FormField>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Подключение к Точка Банку и СБП</legend>
			<FormField
				field="api_base_url"
				:forId="controlID('api_base_url')"
				:label="fieldLabel('api_base_url')"
				:errors="props.errors"
				containerClass="md:col-span-2"
			>
				<InputText
					:id="controlID('api_base_url')"
					v-model="form.api_base_url"
					:invalid="props.errors?.has('api_base_url') ?? false"
					:readonly="
						editing && (props.hasPayments || props.credentialConfigured)
					"
					inputmode="url"
					spellcheck="false"
					required
				/>
				<small
					v-if="editing && (props.hasPayments || props.credentialConfigured)"
					class="text-slate-600"
					>Адрес привязан к сохранённым данным подключения. Для нового адреса
					создайте другой профиль.</small
				>
			</FormField>
			<FormField
				v-for="{ field, frozenAfterPayment } in identityFields"
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
					:readonly="editing && props.hasPayments && frozenAfterPayment"
					spellcheck="false"
				/>
				<small v-if="field === 'account_id'" class="text-slate-600"
					>Укажите номер счёта и БИК через косую черту.</small
				>
			</FormField>
			<FormField
				v-for="{ field, max } in bankDurationFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputNumber
					:inputId="controlID(field)"
					v-model="form[field]"
					:invalid="invalid"
					:min="1"
					:max="max"
					:useGrouping="false"
					required
				/>
			</FormField>
			<small v-if="editing && props.hasPayments" class="text-slate-600 md:col-span-2">
				Реквизиты, использованные в платежах, сохраняются для сверки и возвратов. Для
				новых реквизитов создайте другой профиль.
			</small>
		</fieldset>

		<fieldset
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-2"
			:disabled="props.submitting"
		>
			<legend class="px-2 text-sm font-semibold">Оплата картой</legend>
			<FormField
				field="cards_enabled"
				:forId="controlID('cards_enabled')"
				:label="fieldLabel('cards_enabled')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<Checkbox
					:inputId="controlID('cards_enabled')"
					v-model="form.cards_enabled"
					:invalid="invalid"
					binary
				/>
			</FormField>
			<FormField
				field="acquiring_merchant_id"
				:forId="controlID('acquiring_merchant_id')"
				:label="fieldLabel('acquiring_merchant_id')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputText
					:id="controlID('acquiring_merchant_id')"
					v-model="form.acquiring_merchant_id"
					:invalid="invalid"
					:readonly="editing && props.hasCardPayments"
					:required="Boolean(form.cards_enabled)"
					inputmode="numeric"
					pattern="[0-9]{15}"
					maxlength="15"
				/>
				<small v-if="editing && props.hasCardPayments" class="text-slate-600"
					>ТСП закреплена за платежами по этому профилю.</small
				>
			</FormField>
			<FormField
				field="card_ttl_minutes"
				:forId="controlID('card_ttl_minutes')"
				:label="fieldLabel('card_ttl_minutes')"
				:errors="props.errors"
				v-slot="{ invalid }"
			>
				<InputNumber
					:inputId="controlID('card_ttl_minutes')"
					v-model="form.card_ttl_minutes"
					:invalid="invalid"
					:min="1"
					:max="44640"
					:useGrouping="false"
					required
				/>
			</FormField>
			<FormField
				v-for="field in redirectFields"
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
					inputmode="url"
					spellcheck="false"
					maxlength="1900"
				/>
			</FormField>
		</fieldset>

		<fieldset
			v-if="editing"
			class="mt-4 grid grid-cols-1 gap-4 rounded-lg border border-slate-200 p-4 md:grid-cols-4"
		>
			<legend class="px-2 text-sm font-semibold">История</legend>
			<FormField field="id" :forId="controlID('id')" :label="fieldLabel('id')">
				<InputText :id="controlID('id')" :modelValue="form.id?.toString()" readonly />
			</FormField>
			<FormField
				field="config_version"
				:forId="controlID('config_version')"
				:label="fieldLabel('config_version')"
			>
				<InputText
					:id="controlID('config_version')"
					:modelValue="form.config_version?.toString()"
					readonly
				/>
			</FormField>
			<FormField
				v-for="field in timestampFields"
				:key="field"
				:field="field"
				:forId="controlID(field)"
				:label="fieldLabel(field)"
			>
				<InputText
					:id="controlID(field)"
					:modelValue="formatDate(form[field])"
					readonly
				/>
			</FormField>
		</fieldset>
	</CollectionForm>
</template>
