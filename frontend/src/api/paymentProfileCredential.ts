import * as v from "valibot";

import api from "@/api/http";

const credentialStatusSchema = v.object({
	configured: v.boolean(),
	has_payments: v.boolean(),
	has_card_payments: v.boolean(),
});

export type PaymentProfileCredentialStatus = v.InferOutput<typeof credentialStatusSchema>;

const path = (id: number): string => `/admin/payment-profiles/${id}/credential`;

export const getPaymentProfileCredential = async (
	id: number,
	signal?: AbortSignal,
): Promise<PaymentProfileCredentialStatus> =>
	v.parse(credentialStatusSchema, await api.get<unknown>(path(id), undefined, { signal }));

export const setPaymentProfileCredential = async (
	id: number,
	jwt: string,
): Promise<PaymentProfileCredentialStatus> =>
	v.parse(credentialStatusSchema, await api.post<unknown>(path(id), { jwt }));
