import * as v from "valibot";

import api from "@/api/http";
import { kioskApi } from "@/api/kiosk.gen";
import type { KioskKey, KioskNew } from "@/types/kiosk.gen";

const credentialSchema = v.object({
	id: v.pipe(v.number(), v.safeInteger(), v.minValue(1)),
	token: v.pipe(v.string(), v.regex(/^[0-9a-f]{64}$/)),
});

export type KioskCredential = v.InferOutput<typeof credentialSchema>;

const readCredential = (value: unknown): KioskCredential => {
	const result = v.safeParse(credentialSchema, value);
	if (!result.success) {
		throw new Error("Сервер вернул некорректный ответ о токене киоска.");
	}
	return result.output;
};

export const provisionKiosk = async (model: KioskNew): Promise<KioskCredential> => {
	// The generated CRUD client returns the full response, including the one-time token.
	return readCredential(await kioskApi.create(model));
};

export const rotateKioskToken = async ({ id }: KioskKey): Promise<KioskCredential> => {
	const result = readCredential(await api.post<unknown>(`/admin/kiosks/${id}/token/rotate`, {}));
	if (result.id !== id) {
		throw new Error("Ответ получен для другого киоска.");
	}
	return result;
};
