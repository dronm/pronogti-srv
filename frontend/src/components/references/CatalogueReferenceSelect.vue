<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import Select from "primevue/select";

import type {
	CatalogueSelectOption,
	CatalogueSelectReference,
} from "@/references/salonSelectReferences";

const props = withDefaults(defineProps<{
	id: string;
	reference: CatalogueSelectReference;
	modelValue?: number | null;
	projectedValue?: unknown;
	label?: string;
	invalid?: boolean;
	disabled?: boolean;
	required?: boolean;
	showClear?: boolean;
	showActions?: boolean;
	defaultOptionLabel?: string;
}>(), {
	modelValue: null,
	invalid: false,
	disabled: false,
	required: false,
	showClear: true,
	showActions: true,
});

const emit = defineEmits<{
	"update:modelValue": [value: number | null];
}>();

const { t } = useI18n();
const router = useRouter();
const loadedOptions = shallowRef<CatalogueSelectOption[]>([]);
const resolvedOption = shallowRef<CatalogueSelectOption | null>(null);
const loading = ref(false);
const loadFailed = ref(false);
const resolvingName = ref(false);
let requestVersion = 0;

const selectedID = computed(() => {
	const id = props.modelValue;
	return id !== null && id > 0 ? id : null;
});

const projectedOption = computed<CatalogueSelectOption | null>(() => {
	const id = selectedID.value;
	const projection = props.projectedValue;
	if (
		id !== null &&
		typeof projection === "object" && projection !== null &&
		"keys" in projection &&
		typeof projection.keys === "object" && projection.keys !== null &&
		"id" in projection.keys &&
		(projection.keys.id === id || projection.keys.id === String(id)) &&
		"descr" in projection && typeof projection.descr === "string" &&
		projection.descr.trim() !== ""
	) {
		return { value: id, label: projection.descr };
	}
	return null;
});

const listedOption = computed(() =>
	loadedOptions.value.find((option) => option.value === selectedID.value),
);
const options = computed(() => {
	const current = projectedOption.value ?? resolvedOption.value;
	const rows = !listedOption.value && current && current.value === selectedID.value
		? [current, ...loadedOptions.value]
		: loadedOptions.value;
	return props.defaultOptionLabel
		? [{ value: null, label: props.defaultOptionLabel }, ...rows]
		: rows;
});

const placeholder = computed(() => {
	if (selectedID.value === null) {
		return props.defaultOptionLabel ?? t("Salon.reference.select", "Выберите значение");
	}
	return resolvingName.value
		? t("Salon.reference.loadingName", "Загрузка названия…")
		: t("Salon.reference.nameUnavailable", "Название недоступно");
});

const collectionHref = computed(() => {
	const route = props.reference.collectionRoute;
	return route ? router.resolve(route).href : undefined;
});

const recordHref = computed(() => {
	const id = selectedID.value;
	const route = id === null ? undefined : props.reference.openRoute?.(id);
	return route ? router.resolve(route).href : undefined;
});

const loadOptions = async (): Promise<void> => {
	if (props.disabled || loading.value) {
		return;
	}

	const version = ++requestVersion;
	loadFailed.value = false;
	loading.value = true;
	try {
		const rows = await props.reference.loadOptions();
		if (version === requestVersion) {
			loadedOptions.value = rows;
		}
	} catch {
		if (version === requestVersion) {
			loadFailed.value = true;
		}
	} finally {
		if (version === requestVersion) {
			loading.value = false;
		}
	}
};

watch(() => props.reference, () => {
	++requestVersion;
	loadedOptions.value = [];
	loading.value = false;
	loadFailed.value = false;
});

watch(
	[selectedID, () => props.reference, projectedOption, listedOption],
	async ([id, reference, projection, listed], _, onCleanup) => {
		resolvedOption.value = null;
		resolvingName.value = false;
		if (id === null || projection || listed) {
			return;
		}

		let stale = false;
		onCleanup(() => {
			stale = true;
		});
		resolvingName.value = true;
		try {
			const option = await reference.loadOption(id);
			if (!stale && option?.value === id && option.label.trim() !== "") {
				resolvedOption.value = option;
			}
		} catch {
			// The dropdown can still load its catalogue when opened.
		} finally {
			if (!stale) {
				resolvingName.value = false;
			}
		}
	},
	{ immediate: true },
);

onBeforeUnmount(() => {
	++requestVersion;
});
</script>

<template>
	<div class="min-w-0">
		<div class="flex min-w-0 items-stretch gap-1">
			<Select
				:labelId="props.id"
				:modelValue="selectedID"
				:options="options"
				optionLabel="label"
				optionValue="value"
				:placeholder="placeholder"
				:ariaLabel="props.label"
				:pt="{ label: { 'aria-required': props.required || undefined } }"
				:invalid="props.invalid"
				:disabled="props.disabled"
				:loading="loading"
				class="min-w-0 flex-1"
				@before-show="loadOptions"
				@update:modelValue="emit('update:modelValue', $event)"
			/>
			<Button
				v-if="props.showActions && recordHref"
				as="a"
				:href="recordHref"
				target="_blank"
				rel="noopener noreferrer"
				icon="pi pi-external-link"
				severity="secondary"
				outlined
				class="shrink-0"
				:title="t('Salon.reference.openRecord', 'Открыть запись')"
				:aria-label="t('Salon.reference.openRecord', 'Открыть запись')"
			/>
			<Button
				v-if="props.showActions && collectionHref"
				as="a"
				:href="collectionHref"
				target="_blank"
				rel="noopener noreferrer"
				icon="pi pi-list"
				severity="secondary"
				outlined
				class="shrink-0"
				:title="t('Salon.reference.openCollection', 'Открыть справочник')"
				:aria-label="t('Salon.reference.openCollection', 'Открыть справочник')"
			/>
			<Button
				v-if="props.showActions && props.showClear"
				type="button"
				icon="pi pi-times"
				severity="secondary"
				outlined
				class="shrink-0"
				:disabled="props.disabled || selectedID === null"
				:title="t('Salon.reference.clear', 'Очистить')"
				:aria-label="t('Salon.reference.clear', 'Очистить')"
				@click="emit('update:modelValue', null)"
			/>
		</div>
		<small
			v-if="loadFailed"
			role="alert"
			class="mt-1 block text-red-600"
		>
			{{ t("Salon.reference.loadFailed", "Не удалось загрузить список. Откройте его ещё раз.") }}
		</small>
	</div>
</template>
