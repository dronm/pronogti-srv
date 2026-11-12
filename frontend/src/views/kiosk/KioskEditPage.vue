<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import Button from "primevue/button";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import { CollectionEditPage, errorText, useCollectionEditPage } from "@katren/vue-collection-lib";
import { APIError } from "@katren/vue-collection-lib/api/Conn";

import { kioskApi } from "@/api/kiosk.gen";
import { provisionKiosk, rotateKioskToken, type KioskCredential } from "@/api/kioskCredentials";
import KioskForm from "@/components/kiosk/KioskForm.vue";
import { useKioskSchemas } from "@/composables/schemas/useKioskSchemas.gen";
import { createKioskFormModel, kioskFormMutationFields, type KioskFormModel } from "@/forms/kiosk.gen";
import type { Kiosk, KioskKey, KioskNew, KioskUpd } from "@/types/kiosk.gen";

const { t } = useI18n();
const schemas = useKioskSchemas();
const credential = shallowRef<KioskCredential | null>(null);
const copyMessage = ref("");
const rotationError = ref("");
const confirmingRotation = ref(false);
const rotating = ref(false);
const settingsChanged = ref(false);
const createUncertain = ref(false);
let disposed = false;

onScopeDispose(() => {
	disposed = true;
	credential.value = null;
});

const showCredential = (result: KioskCredential): void => {
	if (!disposed) {
		copyMessage.value = "";
		credential.value = result;
	}
};

const edit = useCollectionEditPage<KioskFormModel, KioskKey, KioskNew, KioskUpd, Kiosk>({
	api: {
		...kioskApi,
		create: async (model) => {
			try {
				const result = await provisionKiosk(model);
				showCredential(result);
				return { id: result.id };
			} catch (cause: unknown) {
				if (
					!(
						cause instanceof APIError &&
						cause.status !== undefined &&
						[400, 401, 403, 404, 422].includes(cause.status)
					)
				) {
					createUncertain.value = true;
				}
				throw cause;
			}
		},
	},
	createRouteName: "kioskCreate",
	listRoute: { name: "kiosks" },
	keyFromRoute: (route) => ({ id: Number(route.params.id ?? 0) }),
	createModel: createKioskFormModel,
	fields: kioskFormMutationFields,
	createSchema: schemas.KioskNewSchema,
	updateSchema: schemas.KioskUpdSchema,
	success: { mode: "stay", editRoute: ({ id }) => ({ name: "kioskEdit", params: { id } }) },
});

const loaded = computed(
	() => edit.mode.value === "edit" && edit.model.value.id === edit.key.value.id && edit.key.value.id > 0,
);
const canRotate = computed(
	() =>
		loaded.value &&
		!settingsChanged.value &&
		!edit.submitting.value &&
		!rotating.value &&
		!credential.value,
);

const rotate = async (): Promise<void> => {
	if (!canRotate.value) {
		return;
	}
	const key = edit.key.value;
	rotating.value = true;
	rotationError.value = "";
	try {
		const result = await rotateKioskToken(key);
		confirmingRotation.value = false;
		if (!disposed && edit.key.value.id === key.id) {
			showCredential(result);
			await edit.load();
		}
	} catch (cause: unknown) {
		rotationError.value = errorText(cause);
	} finally {
		rotating.value = false;
	}
};

const copyToken = async (): Promise<void> => {
	const value = credential.value;
	if (!value) {
		return;
	}
	try {
		await navigator.clipboard.writeText(value.token);
		if (credential.value === value) {
			copyMessage.value = "Токен скопирован.";
		}
	} catch {
		if (credential.value === value) {
			copyMessage.value = "Выделите и скопируйте токен вручную.";
		}
	}
};
</script>

<template>
	<CollectionEditPage
		:title="t(`Kiosk.form.${edit.mode.value}`)"
		:loading="edit.loading.value"
		@back="edit.goBack"
	>
		<p v-if="createUncertain" role="alert" class="text-amber-800">
			Не удалось подтвердить создание киоска. Проверьте список киосков. Если запись появилась, откройте
			её и выпустите новый токен.
		</p>
		<KioskForm
			v-else-if="edit.mode.value !== 'edit' || loaded"
			:model="edit.model.value"
			:mode="edit.mode.value"
			:errors="edit.errors"
			:submitting="edit.submitting.value || rotating"
			@submit="edit.submit"
			@cancel="edit.goBack"
			@changed="settingsChanged = $event"
		/>
		<p v-else role="alert" class="text-red-700">
			{{ edit.errors.formErrors[0] ?? "Не удалось загрузить киоск." }}
		</p>
		<section v-if="loaded" class="mt-5 rounded-md border border-slate-200 p-4">
			<h3 class="mb-2 font-semibold">Подключение киоска</h3>
			<p class="mb-3 text-sm text-slate-600">
				Токен вводится на киоске по адресу <code>/kiosk/setup</code>.
			</p>
			<Button
				type="button"
				label="Выпустить новый токен"
				icon="pi pi-key"
				severity="secondary"
				:disabled="!canRotate"
				@click="
					rotationError = '';
					confirmingRotation = true;
				"
			/>
			<p v-if="settingsChanged" class="mt-2 text-sm text-slate-600">
				Сохраните настройки перед выпуском токена.
			</p>
		</section>
	</CollectionEditPage>

	<Dialog
		v-model:visible="confirmingRotation"
		modal
		header="Выпустить новый токен?"
		:closable="!rotating"
		:closeOnEscape="!rotating"
		class="w-full max-w-md"
	>
		<p>Текущий токен и сессии киоска будут отозваны. На устройстве нужно будет снова ввести токен.</p>
		<p v-if="rotationError" role="alert" class="mt-3 text-red-700">{{ rotationError }}</p>
		<template #footer>
			<Button
				type="button"
				label="Отмена"
				severity="secondary"
				:disabled="rotating"
				@click="confirmingRotation = false"
			/>
			<Button
				type="button"
				label="Выпустить"
				:loading="rotating"
				:disabled="!canRotate"
				@click="rotate"
			/>
		</template>
	</Dialog>

	<Dialog
		:visible="credential !== null"
		modal
		header="Токен подключения киоска"
		class="w-full max-w-xl"
		@update:visible="credential = null"
	>
		<div v-if="credential" class="space-y-3">
			<p>
				Скопируйте токен и введите его на киоске в <code>/kiosk/setup</code>. После закрытия окна
				токен больше не отображается.
			</p>
			<label for="kiosk-setup-token" class="block text-sm">Токен киоска №{{ credential.id }}</label>
			<InputText
				id="kiosk-setup-token"
				:modelValue="credential.token"
				class="w-full font-mono"
				readonly
				autocomplete="off"
				spellcheck="false"
			/>
			<p role="status" class="text-sm text-slate-600">{{ copyMessage }}</p>
		</div>
		<template #footer>
			<Button type="button" label="Закрыть" severity="secondary" @click="credential = null" />
			<Button type="button" label="Копировать" icon="pi pi-copy" @click="copyToken" />
		</template>
	</Dialog>
</template>
